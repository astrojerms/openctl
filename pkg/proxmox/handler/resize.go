package handler

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/openctl/openctl/pkg/proxmox/client"
	"github.com/openctl/openctl/pkg/proxmox/resources"
)

// resizeVM applies in-place changes to an existing VM for the fields Proxmox
// can update live: memory, CPU (cores/sockets), and disk growth. Other spec
// differences (template, networks, cloud-init) are NOT touched here — changing
// them still requires delete + re-apply, and they surface as drift via Get.
//
// This realizes the CONTROLLER.md decision to update atomic resources in place
// for resizable fields rather than no-op. Idempotent: re-applying an unchanged
// spec sends the same config (Proxmox no-ops on identical values) and skips any
// disk already at its target size.
func (h *Handler) resizeVM(ctx context.Context, vm *client.VM, spec *resources.VMSpec) error {
	params := map[string]any{}
	if spec.Memory != nil && spec.Memory.Size > 0 {
		params["memory"] = spec.Memory.Size
	}
	if spec.CPU != nil {
		if spec.CPU.Cores > 0 {
			params["cores"] = spec.CPU.Cores
		}
		if spec.CPU.Sockets > 0 {
			params["sockets"] = spec.CPU.Sockets
		}
	}
	if len(params) > 0 {
		if err := h.client.ConfigureVM(ctx, vm.Node, vm.VMID, params); err != nil {
			return fmt.Errorf("update VM %q config: %w", vm.Name, err)
		}
	}

	if len(spec.Disks) > 0 {
		if err := h.resizeDisks(ctx, vm, spec.Disks); err != nil {
			return err
		}
	}
	return nil
}

// resizeDisks grows disks whose desired size exceeds their current size.
// A disk already at (or above) its target is left alone; a request to shrink is
// rejected with a clear error rather than passed to Proxmox, which cannot shrink
// a disk. The current config is read at most once, and only if a sized disk is
// actually present.
func (h *Handler) resizeDisks(ctx context.Context, vm *client.VM, disks []resources.DiskSpec) error {
	var raw map[string]any
	for _, disk := range disks {
		if disk.Size == "" {
			continue
		}
		desired, err := parseProxmoxSize(disk.Size)
		if err != nil {
			return fmt.Errorf("disk %s: %w", disk.Name, err)
		}
		if raw == nil {
			raw, err = h.client.GetVMConfigRaw(ctx, vm.Node, vm.VMID)
			if err != nil {
				return fmt.Errorf("read VM %q config for disk resize: %w", vm.Name, err)
			}
		}
		// If we can read the current size, guard shrink and skip no-ops. If we
		// can't (disk not in config / unparseable), fall through and let Proxmox
		// apply the absolute size — it grows and refuses shrink on its own.
		if cur, ok := currentDiskSize(raw, disk.Name); ok {
			switch {
			case desired == cur:
				continue
			case desired < cur:
				return fmt.Errorf("disk %s: cannot shrink from %s to %s — Proxmox does not support shrinking a disk; delete + re-apply to resize down",
					disk.Name, humanizeBytes(cur), disk.Size)
			}
		}
		if err := h.client.ResizeVMDisk(ctx, vm.Node, vm.VMID, disk.Name, disk.Size); err != nil {
			return fmt.Errorf("grow disk %s on VM %q: %w", disk.Name, vm.Name, err)
		}
	}
	return nil
}

// parseProxmoxSize parses a Proxmox size string (e.g. "50G", "512M", "1T",
// "1024K", or a bare byte count) into bytes. Units are binary (G = GiB), matching
// Proxmox's own interpretation.
func parseProxmoxSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	digits := s
	switch s[len(s)-1] {
	case 'T', 't':
		mult = 1 << 40
		digits = s[:len(s)-1]
	case 'G', 'g':
		mult = 1 << 30
		digits = s[:len(s)-1]
	case 'M', 'm':
		mult = 1 << 20
		digits = s[:len(s)-1]
	case 'K', 'k':
		mult = 1 << 10
		digits = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(digits), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("negative size %q", s)
	}
	return n * mult, nil
}

// currentDiskSize extracts a disk's current size (in bytes) from a raw Proxmox
// VM config. Disk entries look like "local-lvm:vm-100-disk-0,size=32G,ssd=1".
// Returns false when the disk is absent or carries no parseable size= field.
func currentDiskSize(raw map[string]any, disk string) (int64, bool) {
	line, ok := raw[disk].(string)
	if !ok {
		return 0, false
	}
	for part := range strings.SplitSeq(line, ",") {
		if after, found := strings.CutPrefix(part, "size="); found {
			n, err := parseProxmoxSize(after)
			if err != nil {
				return 0, false
			}
			return n, true
		}
	}
	return 0, false
}

// humanizeBytes renders a byte count as a rounded binary-unit string for error
// messages. Approximate — human display only.
func humanizeBytes(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%dT", n/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%dG", n/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%dM", n/(1<<20))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
