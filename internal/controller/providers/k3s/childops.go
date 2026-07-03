package k3s

import (
	"context"
	"encoding/json"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/pkg/protocol"
)

// runChildVMApply records a child op around a VM Apply call. The recorder
// is fetched once per parent at the top of provider.Apply; for a no-op
// recorder (CLI direct invocation or unit tests) Begin returns "" and End
// is a no-op, so this stays cheap.
func runChildVMApply(ctx context.Context, rec operations.ChildRecorder, vm *protocol.Resource, vms VMApplier) error {
	manifestJSON, _ := json.Marshal(vm)
	childID, _ := rec.Begin(ctx, &operations.Operation{
		Type:         operations.TypeApply,
		APIVersion:   vm.APIVersion,
		Kind:         vm.Kind,
		ResourceName: vm.Metadata.Name,
		ManifestJSON: string(manifestJSON),
	})
	result, err := vms.Apply(ctx, vm)
	if err != nil {
		_ = rec.End(ctx, childID, false, err.Error(), "")
		return wrapVMErr(vm.Metadata.Name, err)
	}
	var resultJSON []byte
	if result != nil {
		resultJSON, _ = json.Marshal(result)
	}
	_ = rec.End(ctx, childID, true, "", string(resultJSON))
	return nil
}

// runChildStep records a child op of type "step" around an opaque
// sub-operation (e.g. installing k3s on a cluster's worth of nodes). The
// label is the human-readable description; resourceName names the step
// itself (e.g. "install-k3s") so List filters can find it.
func runChildStep(ctx context.Context, rec operations.ChildRecorder, clusterName, stepName, label string, fn func() (any, error)) (any, error) {
	childID, _ := rec.Begin(ctx, &operations.Operation{
		Type:         operations.TypeStep,
		APIVersion:   "k3s.openctl.io/v1",
		Kind:         "Cluster",
		ResourceName: clusterName + "/" + stepName,
		Label:        label,
	})
	result, err := fn()
	if err != nil {
		_ = rec.End(ctx, childID, false, err.Error(), "")
		return nil, err
	}
	_ = rec.End(ctx, childID, true, "", "")
	return result, nil
}

// wrapVMErr keeps the previous error wrapping shape for callers that grep
// for the original message ("apply VM <name>: ...").
func wrapVMErr(name string, err error) error {
	return &vmApplyError{Name: name, Err: err}
}

type vmApplyError struct {
	Name string
	Err  error
}

func (e *vmApplyError) Error() string { return "apply VM " + e.Name + ": " + e.Err.Error() }
func (e *vmApplyError) Unwrap() error { return e.Err }
