package cluster

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/openctl/openctl-k3s/internal/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// Deleter handles cluster deletion
type Deleter struct {
	name   string
	spec   *resources.ClusterSpec
}

// NewDeleter creates a new cluster deleter
func NewDeleter(name string, spec *resources.ClusterSpec) *Deleter {
	return &Deleter{
		name: name,
		spec: spec,
	}
}

// GenerateDispatchRequests generates VM deletion dispatch requests
func (d *Deleter) GenerateDispatchRequests() []*protocol.DispatchRequest {
	cpNodes, workerNodes := resources.NodeNames(d.name, d.spec)
	allNodes := append(cpNodes, workerNodes...)

	requests := make([]*protocol.DispatchRequest, 0, len(allNodes))

	for _, nodeName := range allNodes {
		requests = append(requests, &protocol.DispatchRequest{
			ID:           fmt.Sprintf("vm-%s", nodeName),
			Provider:     d.spec.Compute.Provider,
			Action:       protocol.ActionDelete,
			ResourceType: "VirtualMachine",
			ResourceName: nodeName,
		})
	}

	return requests
}

// Cleanup removes local files associated with the cluster
func (d *Deleter) Cleanup() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	clusterDir := filepath.Join(homeDir, ".openctl", "k3s", d.name)
	return os.RemoveAll(clusterDir)
}

// ValidateResults checks if all VMs were deleted successfully
func (d *Deleter) ValidateResults(results []*protocol.DispatchResult) []string {
	var errors []string

	for _, result := range results {
		if result.Status != protocol.StatusSuccess {
			// NOT_FOUND is acceptable - VM may already be deleted
			if result.Error != nil && result.Error.Code != protocol.ErrorCodeNotFound {
				errors = append(errors, fmt.Sprintf("%s: %s", result.ID, result.Error.Message))
			}
		}
	}

	return errors
}
