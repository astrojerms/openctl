package proxmox

import (
	"context"
	"fmt"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
	pmclient "github.com/openctl/openctl/pkg/proxmox/client"
)

var _ providers.MigrationIdentityReader = (*Provider)(nil)

// MigrationIdentity binds the selected endpoint's real node, VMID and SMBIOS
// UUID. It never applies configuration, and never invents a missing UUID.
func (p *Provider) MigrationIdentity(ctx context.Context, manifest *protocol.Resource) (string, error) {
	if manifest == nil || manifest.Kind != kindVM || manifest.Metadata.Name == "" {
		return "", fmt.Errorf("migration requires a named VirtualMachine")
	}
	contextName, _ := manifest.Spec["context"].(string)
	h, resolved, err := p.handlerForContext(contextName)
	if err != nil {
		return "", err
	}
	boundHandler, bound, id, err := p.boundVM(ctx, manifest.Metadata.Name)
	if err != nil {
		return "", err
	}
	var vm *pmclient.VM
	if id != nil {
		if resolved != id.Context || boundHandler != h {
			return "", fmt.Errorf("migration context differs from recorded identity")
		}
		vm = bound
	} else {
		vm, err = uniqueVM(ctx, h, manifest.Metadata.Name)
		if err != nil {
			return "", err
		}
	}
	if vm == nil {
		return "", providers.NotFound(kindVM, manifest.Metadata.Name)
	}
	node, _ := manifest.Spec["node"].(string)
	if node == "" || node != vm.Node {
		return "", fmt.Errorf("migration VM node does not match desired node")
	}
	raw, err := h.Client().GetVMConfigRaw(ctx, vm.Node, vm.VMID)
	if err != nil {
		return "", fmt.Errorf("read migration VM configuration: %w", err)
	}
	return vmIdentity(vm, raw)
}
