package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// ArgoApplications aggregates a cluster's Argo CD Applications into openctl — the
// read/observe baseline for the deployment model's GitOps layer. It is a "view"
// resource: Apply and Get both read the live Application CRs (no mutation);
// Delete is a no-op. Creating Applications declaratively is done via the
// Manifest kind (apply an Application CR), so this kind stays read-only.
const kindArgoApplications = "ArgoApplications"

// argoAppGVR is the Argo CD Application custom resource.
var argoAppGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// list enumerates a namespaced custom resource by GVR.
func (k *kubeClient) list(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]unstructured.Unstructured, error) {
	ul, err := k.dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return ul.Items, nil
}

// argoApp is the summarized view of one Argo Application.
type argoApp struct {
	Name   string `json:"name"`
	Health string `json:"health"`
	Sync   string `json:"sync"`
}

func summarizeArgoApp(u unstructured.Unstructured) argoApp {
	health, _, _ := unstructured.NestedString(u.Object, "status", "health", "status")
	sync, _, _ := unstructured.NestedString(u.Object, "status", "sync", "status")
	return argoApp{Name: u.GetName(), Health: health, Sync: sync}
}

func argoNamespace(spec map[string]any) string {
	if ns := specString(spec, "namespace"); ns != "" {
		return ns
	}
	return "argocd"
}

func (p *provider) applyArgoApplications(ctx context.Context, m *protocol.Resource) (*pluginproto.ApplyResult, error) {
	content, path, err := kubeconfigFromSpec(m.Spec, m.Metadata.Name)
	if err != nil {
		return nil, err
	}
	ns := argoNamespace(m.Spec)
	apps, err := p.readArgoApps(ctx, content, ns)
	if err != nil {
		return nil, fmt.Errorf("ArgoApplications %q: %w", m.Metadata.Name, err)
	}
	st, _ := json.Marshal(argoState{kubeconfigState: kubeconfigStateOf(content, path), Namespace: ns})
	r := argoObserved(m.Metadata.Name, apps)
	r.Spec = map[string]any{"namespace": ns}
	return &pluginproto.ApplyResult{Resource: r, State: st}, nil
}

func (p *provider) getArgoApplications(ctx context.Context, req pluginproto.GetParams) (*pluginproto.GetResult, error) {
	var st argoState
	if len(req.State) > 0 {
		_ = json.Unmarshal(req.State, &st)
	}
	if !st.present() {
		return nil, pluginproto.NotFound(fmt.Sprintf("ArgoApplications %q has no prior state", req.Name))
	}
	kc, err := st.loadKubeconfig()
	if err != nil {
		return nil, err
	}
	ns := st.Namespace
	if ns == "" {
		ns = "argocd"
	}
	apps, err := p.readArgoApps(ctx, kc, ns)
	if err != nil {
		return nil, err
	}
	return &pluginproto.GetResult{Resource: argoObserved(req.Name, apps), State: req.State}, nil
}

// readArgoApps lists and summarizes the Argo Applications in a namespace.
func (p *provider) readArgoApps(ctx context.Context, kubeconfig []byte, namespace string) ([]argoApp, error) {
	client, err := newKubeClient(kubeconfig)
	if err != nil {
		return nil, err
	}
	items, err := client.list(ctx, argoAppGVR, namespace)
	if err != nil {
		return nil, fmt.Errorf("list argo applications: %w", err)
	}
	apps := make([]argoApp, 0, len(items))
	for _, it := range items {
		apps = append(apps, summarizeArgoApp(it))
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}

type argoState struct {
	kubeconfigState
	Namespace string `json:"namespace"`
}

// argoObserved renders the Applications into an observed resource. healthy
// counts apps reporting Healthy; the phase is Ready when all are Healthy.
func argoObserved(name string, apps []argoApp) *protocol.Resource {
	list := make([]any, 0, len(apps))
	healthy := 0
	for _, a := range apps {
		list = append(list, map[string]any{"name": a.Name, "health": a.Health, "sync": a.Sync})
		if a.Health == "Healthy" {
			healthy++
		}
	}
	phase := "Ready"
	if healthy < len(apps) {
		phase = "Degraded"
	}
	r := &protocol.Resource{APIVersion: apiVersion, Kind: kindArgoApplications, Status: map[string]any{
		"applications": list,
		"count":        len(apps),
		"healthy":      healthy,
		"phase":        phase,
	}}
	r.Metadata.Name = name
	return r
}
