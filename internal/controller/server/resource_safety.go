package server

import (
	"context"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/refs"
	"github.com/openctl/openctl/pkg/protocol"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CheckDelete revalidates top-level deletion at admission and execution.
func (h *resourceHandler) CheckDelete(ctx context.Context, av, kind, name string) error {
	if av == "" || kind == "" || name == "" {
		return status.Error(codes.InvalidArgument, "api_version, kind, and name are required")
	}
	if h.manifests == nil {
		return status.Error(codes.FailedPrecondition, "deletion requires managed resource tracking")
	}
	target, err := h.manifests.Load(ctx, av, kind, name)
	if err != nil {
		return status.Error(codes.FailedPrecondition, "cannot verify managed resource tracking")
	}
	if target == nil {
		return status.Error(codes.FailedPrecondition, "resource is not tracked; adopt it before deletion")
	}
	ownerKind, ownerName, owned, err := h.registry.CheckedOwnerOf(ctx, kind, name)
	if err != nil {
		return status.Error(codes.FailedPrecondition, "cannot verify resource ownership")
	}
	if owned {
		return status.Errorf(codes.FailedPrecondition, "%s %q is owned by %s %q; delete the owner instead", kind, name, ownerKind, ownerName)
	}
	if target.Metadata.Labels[providers.LabelOwnerKind] != "" || target.Metadata.Labels[providers.LabelOwnerName] != "" {
		return status.Error(codes.FailedPrecondition, "resource has a recorded owner; delete the owner instead")
	}
	return h.checkManagedDependents(ctx, av, kind, name)
}

func (h *resourceHandler) checkManagedDependents(ctx context.Context, av, kind, name string) error {
	all, err := h.manifests.ListAll(ctx)
	if err != nil {
		return status.Error(codes.FailedPrecondition, "cannot verify managed dependents")
	}
	for _, ref := range all {
		if ref.APIVersion == av && ref.Kind == kind && ref.Name == name {
			continue
		}
		other, err := h.manifests.Load(ctx, ref.APIVersion, ref.Kind, ref.Name)
		if err != nil || other == nil {
			return status.Error(codes.FailedPrecondition, "cannot verify managed dependents")
		}
		if !validReferences(other.Spec) {
			return status.Error(codes.FailedPrecondition, "managed dependency metadata is malformed; cannot prove deletion or adoption is safe")
		}
		if referencesResource(other, av, kind, name) {
			return status.Errorf(codes.FailedPrecondition, "%s %q references this resource; remove the dependent first", ref.Kind, ref.Name)
		}
	}
	return nil
}

func referencesResource(resource *protocol.Resource, av, kind, name string) bool {
	for _, ref := range refs.Collect(resource.Spec) {
		if ref.APIVersion == av && ref.Kind == kind && ref.Name == name {
			return true
		}
	}
	return false
}

// Collect is best-effort for display; mutation admission rejects malformed
// markers instead of silently treating them as an absent dependency.
func validReferences(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if raw, present := v["$ref"]; present {
			ref, ok := raw.(map[string]any)
			if !ok {
				return false
			}
			for _, key := range []string{"apiVersion", "kind", "name"} {
				if text, ok := ref[key].(string); !ok || text == "" {
					return false
				}
			}
		}
		for _, child := range v {
			if !validReferences(child) {
				return false
			}
		}
	case []any:
		for _, child := range v {
			if !validReferences(child) {
				return false
			}
		}
	}
	return true
}

func (h *resourceHandler) checkManifestAuthority() error {
	if h.manifestMode == "gitops" {
		return status.Error(codes.FailedPrecondition, "GitOps is the manifest authority; edit the upstream repository instead")
	}
	return nil
}
