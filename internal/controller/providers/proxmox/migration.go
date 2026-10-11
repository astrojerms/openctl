package proxmox

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

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
	vms, err := h.Client().ListVMs(ctx)
	if err != nil {
		return "", fmt.Errorf("locate migration VM: %w", err)
	}
	var vm *pmclient.VM
	for _, candidate := range vms {
		if candidate.Name != manifest.Metadata.Name {
			continue
		}
		if vm != nil {
			return "", fmt.Errorf("migration VM name is ambiguous on selected endpoint")
		}
		vm = candidate
	}
	if vm == nil {
		return "", providers.NotFound(kindVM, manifest.Metadata.Name)
	}
	if vm.Template != 0 || vm.VMID <= 0 || vm.Node == "" {
		return "", fmt.Errorf("migration requires an existing non-template VM with node and VMID")
	}
	node, _ := manifest.Spec["node"].(string)
	if node == "" || node != vm.Node {
		return "", fmt.Errorf("migration VM node does not match desired node")
	}
	config, err := h.Client().GetVMConfigRaw(ctx, vm.Node, vm.VMID)
	if err != nil {
		return "", fmt.Errorf("read migration VM configuration: %w", err)
	}
	smbios, _ := config["smbios1"].(string)
	var rawUUID string
	for part := range strings.SplitSeq(smbios, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && key == "uuid" {
			if rawUUID != "" {
				return "", fmt.Errorf("migration VM has duplicate SMBIOS UUIDs")
			}
			rawUUID = value
		}
	}
	id, err := uuid.Parse(rawUUID)
	if err != nil || id == uuid.Nil {
		return "", fmt.Errorf("migration VM has missing or invalid smbios1 UUID")
	}
	// Seed normal Get/action routing only after verifying this exact context.
	p.index.Store(manifest.Metadata.Name, resolved)
	return fmt.Sprintf("proxmox:%s:%d:%s", vm.Node, vm.VMID, id.String()), nil
}
