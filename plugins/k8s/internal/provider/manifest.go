package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// fieldManager identifies openctl as the owner of the fields it server-side
// applies, so repeated applies converge and conflicts are attributable.
const fieldManager = "openctl-k8s"

// objectRef is enough to Get/Delete an applied object later (Get/Delete carry
// no spec). Persisted in the Manifest's state so openctl can prune objects that
// leave the manifest and clean up on delete.
type objectRef struct {
	Group     string `json:"group"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

func (o objectRef) gvr() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: o.Group, Version: o.Version, Resource: o.Resource}
}

func (o objectRef) id() string {
	return strings.Join([]string{o.Group, o.Version, o.Kind, o.Namespace, o.Name}, "/")
}

func (o objectRef) String() string {
	if o.Namespace != "" {
		return fmt.Sprintf("%s %s/%s", o.Kind, o.Namespace, o.Name)
	}
	return fmt.Sprintf("%s %s", o.Kind, o.Name)
}

// kubeClient bundles a dynamic client and a discovery-backed RESTMapper so it
// can apply/get/delete arbitrary object kinds by GVK.
type kubeClient struct {
	dyn    dynamic.Interface
	mapper meta.RESTMapper
}

func newKubeClient(kubeconfig []byte) (*kubeClient, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
	}
	groups, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, fmt.Errorf("discover api groups: %w", err)
	}
	return &kubeClient{dyn: dyn, mapper: restmapper.NewDiscoveryRESTMapper(groups)}, nil
}

// resourceFor resolves an object's GVK to a namespaced-or-cluster dynamic
// resource interface plus a fully-qualified objectRef.
func (k *kubeClient) resourceFor(obj *unstructured.Unstructured) (dynamic.ResourceInterface, objectRef, error) {
	gvk := obj.GroupVersionKind()
	if gvk.Kind == "" || gvk.Version == "" {
		return nil, objectRef{}, fmt.Errorf("object missing apiVersion/kind")
	}
	mapping, err := k.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, objectRef{}, fmt.Errorf("rest mapping for %s: %w", gvk, err)
	}
	ref := objectRef{
		Group:    mapping.Resource.Group,
		Version:  mapping.Resource.Version,
		Resource: mapping.Resource.Resource,
		Kind:     gvk.Kind,
		Name:     obj.GetName(),
	}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := obj.GetNamespace()
		if ns == "" {
			ns = "default"
		}
		ref.Namespace = ns
		return k.dyn.Resource(mapping.Resource).Namespace(ns), ref, nil
	}
	return k.dyn.Resource(mapping.Resource), ref, nil
}

// apply server-side-applies one object and returns its ref.
func (k *kubeClient) apply(ctx context.Context, obj *unstructured.Unstructured) (objectRef, error) {
	ri, ref, err := k.resourceFor(obj)
	if err != nil {
		return objectRef{}, err
	}
	if ref.Name == "" {
		return objectRef{}, fmt.Errorf("%s object has no metadata.name", ref.Kind)
	}
	data, err := obj.MarshalJSON()
	if err != nil {
		return objectRef{}, err
	}
	force := true
	if _, err := ri.Patch(ctx, ref.Name, types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: fieldManager,
		Force:        &force,
	}); err != nil {
		return objectRef{}, fmt.Errorf("apply %s: %w", ref, err)
	}
	return ref, nil
}

func (k *kubeClient) get(ctx context.Context, ref objectRef) (*unstructured.Unstructured, error) {
	ri := k.resourceInterface(ref)
	return ri.Get(ctx, ref.Name, metav1.GetOptions{})
}

// delete removes an object. Idempotent: an already-absent object is nil.
func (k *kubeClient) delete(ctx context.Context, ref objectRef) error {
	ri := k.resourceInterface(ref)
	err := ri.Delete(ctx, ref.Name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (k *kubeClient) resourceInterface(ref objectRef) dynamic.ResourceInterface {
	if ref.Namespace != "" {
		return k.dyn.Resource(ref.gvr()).Namespace(ref.Namespace)
	}
	return k.dyn.Resource(ref.gvr())
}

// parseObjects decodes a (possibly multi-document) YAML/JSON string into
// unstructured objects, skipping empty documents.
func parseObjects(manifest string) ([]*unstructured.Unstructured, error) {
	dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	var out []*unstructured.Unstructured
	for {
		raw := map[string]any{}
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode manifest: %w", err)
		}
		if len(raw) == 0 {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: raw})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("manifest contains no objects")
	}
	return out, nil
}

// prunedRefs returns the objects present in prior but absent from current — the
// objects openctl owned that have left the manifest and should be deleted.
func prunedRefs(prior, current []objectRef) []objectRef {
	keep := make(map[string]bool, len(current))
	for _, r := range current {
		keep[r.id()] = true
	}
	var out []objectRef
	for _, r := range prior {
		if !keep[r.id()] {
			out = append(out, r)
		}
	}
	return out
}
