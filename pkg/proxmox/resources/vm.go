package resources

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/openctl/openctl/pkg/protocol"
	"github.com/openctl/openctl/pkg/proxmox/client"
)

// VMToResource converts a Proxmox VM to a protocol Resource
func VMToResource(vm *client.VM, config *client.VMConfig) *protocol.Resource {
	return VMToResourceWithIP(vm, config, "")
}

// VMToResourceWithIP converts a Proxmox VM to a protocol Resource with an optional IP address
func VMToResourceWithIP(vm *client.VM, config *client.VMConfig, ip string) *protocol.Resource {
	spec := map[string]any{
		"node": vm.Node,
	}

	if config != nil {
		spec["cpu"] = map[string]any{
			"cores":   config.Cores,
			"sockets": config.Sockets,
		}
		spec["memory"] = map[string]any{
			"size": config.Memory,
		}
	} else {
		spec["cpu"] = map[string]any{
			"cores": vm.CPUs,
		}
		spec["memory"] = map[string]any{
			"size": vm.MaxMem / 1024 / 1024,
		}
	}

	status := map[string]any{
		"vmid":    vm.VMID,
		"state":   vm.Status,
		"uptime":  vm.Uptime,
		"cpuUsed": vm.CPU,
		"memUsed": vm.Mem,
	}

	if ip != "" {
		status["ip"] = ip
	}

	return &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata: protocol.ResourceMetadata{
			Name: vm.Name,
		},
		Spec:   spec,
		Status: status,
	}
}

// TemplateToResource converts a Proxmox Template to a protocol Resource
func TemplateToResource(t *client.Template) *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "Template",
		Metadata: protocol.ResourceMetadata{
			Name: t.Name,
		},
		Spec: map[string]any{
			"node": t.Node,
			"vmid": t.VMID,
		},
		Status: map[string]any{
			"state": t.Status,
		},
	}
}

// VMSpec represents the spec for a VirtualMachine
type VMSpec struct {
	Node          string         `json:"node"`
	Template      *TemplateRef   `json:"template"`
	Image         *ImageRef      `json:"image"`
	CloudImage    *CloudImageRef `json:"cloudImage"`
	CPU           *CPUSpec       `json:"cpu"`
	Memory        *MemorySpec    `json:"memory"`
	Disks         []DiskSpec     `json:"disks"`
	Networks      []NetworkSpec  `json:"networks"`
	CloudInit     *CloudInitSpec `json:"cloudInit"`
	StartOnCreate bool           `json:"startOnCreate"`
	OSType        string         `json:"osType"`
	BIOS          string         `json:"bios"`
	Machine       string         `json:"machine"`
	Agent         *AgentSpec     `json:"agent"`
	// HostPCI attaches host PCI devices (e.g. a GPU) to the VM. Requires
	// machine=q35 + bios=ovmf + an EFIDisk for a working GPU passthrough VM.
	HostPCI []HostPCISpec `json:"hostPCI"`
	// EFIDisk allocates the OVMF EFI vars disk. Needed whenever bios=ovmf
	// (UEFI) — without it OVMF has nowhere to persist boot config.
	EFIDisk *EFIDiskSpec `json:"efiDisk"`
}

// TemplateRef references a template
type TemplateRef struct {
	Name string `json:"name"`
	VMID int    `json:"vmid"`
}

// ImageRef references a disk image in storage (legacy - use CloudImageRef instead)
type ImageRef struct {
	// Storage is the Proxmox storage ID where the image is located (e.g., "local", "nfs-images")
	Storage string `json:"storage"`
	// File is the filename of the image (e.g., "ubuntu-22.04-cloudimg-amd64.img")
	// Can also be a full volume ID (e.g., "local:import/image.img")
	File string `json:"file"`
	// ContentType is the storage content type where the image is located.
	// Common values: "iso", "images", "import". Defaults to "images".
	ContentType string `json:"contentType"`
	// Format is the disk format (qcow2, raw, vmdk). If empty, auto-detected from extension.
	Format string `json:"format"`
	// TargetStorage is where to store the imported disk. If empty, uses Storage.
	TargetStorage string `json:"targetStorage"`
	// TargetFormat is the format for the imported disk. If empty, uses Format or defaults to raw for block storage.
	TargetFormat string `json:"targetFormat"`
}

// CloudImageRef references a cloud image to download and use
type CloudImageRef struct {
	// URL is the download URL for the cloud image (e.g., https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img)
	URL string `json:"url"`
	// Storage is where to store the downloaded image and create the template
	Storage string `json:"storage"`
	// Checksum is the expected checksum of the image (optional, format: "sha256:abc123..." or "sha512:...")
	Checksum string `json:"checksum"`
	// TemplateName is the name for the template VM. If empty, auto-generated from URL.
	TemplateName string `json:"templateName"`
	// DiskStorage is where to store the VM disk. If empty, uses Storage.
	DiskStorage string `json:"diskStorage"`
}

// AgentSpec configures the QEMU guest agent
type AgentSpec struct {
	Enabled bool `json:"enabled"`
}

// CPUSpec defines CPU configuration
type CPUSpec struct {
	Cores   int `json:"cores"`
	Sockets int `json:"sockets"`
	// Type is the Proxmox CPU model (e.g. "host" to expose the physical CPU's
	// full feature set — AVX etc. — which compute/GPU workloads want). Empty
	// leaves the Proxmox default (kvm64).
	Type string `json:"type"`
}

// HostPCISpec attaches one host PCI device (e.g. a GPU) to the VM, emitted as
// Proxmox `hostpciN`. Provide exactly one of Device (raw PCI address like
// "0000:01:00" or "0000:01:00.0") or Mapping (a Proxmox resource-mapping id,
// the portable choice across hosts). The flags mirror Proxmox's hostpci
// options and matter for GPU passthrough.
type HostPCISpec struct {
	Device  string `json:"device"`
	Mapping string `json:"mapping"`
	// PCIE exposes the device as PCIe rather than legacy PCI (q35 only).
	PCIE bool `json:"pcie"`
	// PrimaryGPU marks this as the primary VGA (Proxmox x-vga=1).
	PrimaryGPU bool `json:"primaryGPU"`
	// ROMBAR is on by default in Proxmox; set false to emit rombar=0 (some GPUs
	// need this). Pointer so absent ≠ explicit false.
	ROMBAR *bool `json:"rombar,omitempty"`
	// MDev requests a mediated device (vGPU) of the given type.
	MDev string `json:"mdev"`
	// ROMFile names a custom device ROM under Proxmox's /usr/share/kvm.
	ROMFile string `json:"romfile"`
}

// configString renders the Proxmox `hostpciN` value, e.g.
// "0000:01:00,pcie=1,x-vga=1" or "mapping=gpu,pcie=1".
func (h HostPCISpec) configString() string {
	head := h.Device
	if h.Mapping != "" {
		head = "mapping=" + h.Mapping
	}
	parts := []string{head}
	if h.PCIE {
		parts = append(parts, "pcie=1")
	}
	if h.PrimaryGPU {
		parts = append(parts, "x-vga=1")
	}
	// ROMBAR defaults on in Proxmox; only emit when explicitly disabled.
	if h.ROMBAR != nil && !*h.ROMBAR {
		parts = append(parts, "rombar=0")
	}
	if h.MDev != "" {
		parts = append(parts, "mdev="+h.MDev)
	}
	if h.ROMFile != "" {
		parts = append(parts, "romfile="+h.ROMFile)
	}
	return strings.Join(parts, ",")
}

// EFIDiskSpec allocates the OVMF EFI vars disk (Proxmox `efidisk0`).
type EFIDiskSpec struct {
	// Storage is the Proxmox storage ID to allocate the vars disk on
	// (e.g. "local-lvm"). Required.
	Storage string `json:"storage"`
	// Type is the OVMF firmware size: "4m" (default, needed for secure boot /
	// larger var stores) or "2m".
	Type string `json:"type"`
	// PreEnrolledKeys enrolls Microsoft's secure-boot keys. Defaults OFF —
	// GPU-passthrough / Linux guests generally run without secure boot.
	PreEnrolledKeys *bool `json:"preEnrolledKeys,omitempty"`
}

// configString renders the Proxmox `efidisk0` value. The ":1" tells Proxmox to
// allocate a fresh EFI vars volume on the storage (the size is fixed by the
// efitype regardless of the number).
func (e EFIDiskSpec) configString() string {
	efitype := e.Type
	if efitype == "" {
		efitype = "4m"
	}
	pek := "0"
	if e.PreEnrolledKeys != nil && *e.PreEnrolledKeys {
		pek = "1"
	}
	return fmt.Sprintf("%s:1,efitype=%s,pre-enrolled-keys=%s", e.Storage, efitype, pek)
}

// MemorySpec defines memory configuration
type MemorySpec struct {
	Size int `json:"size"`
}

// DiskSpec defines disk configuration
type DiskSpec struct {
	Name     string `json:"name"`
	Storage  string `json:"storage"`
	Size     string `json:"size"`
	SSD      bool   `json:"ssd"`
	Discard  bool   `json:"discard"`
	IOThread bool   `json:"iothread"`
	// Backup uses a pointer so absent (Proxmox default = on) is
	// distinguishable from explicit false (opt out of backup).
	Backup *bool  `json:"backup,omitempty"`
	Cache  string `json:"cache"`
}

// Options returns the disk flag key=value pairs (e.g. ssd=1,discard=on)
// suitable for merging into a Proxmox disk config string. Excludes the
// volume reference and `size` — those are handled separately by the
// resize/import paths.
func (d *DiskSpec) Options() map[string]string {
	opts := map[string]string{}
	if d.SSD {
		opts["ssd"] = "1"
	}
	if d.Discard {
		opts["discard"] = "on"
	}
	if d.IOThread {
		opts["iothread"] = "1"
	}
	if d.Backup != nil {
		if *d.Backup {
			opts["backup"] = "1"
		} else {
			opts["backup"] = "0"
		}
	}
	if d.Cache != "" {
		opts["cache"] = d.Cache
	}
	return opts
}

// NetworkSpec defines network configuration
type NetworkSpec struct {
	Name       string `json:"name"`
	Bridge     string `json:"bridge"`
	Model      string `json:"model"`
	VLAN       int    `json:"vlan"`
	Firewall   bool   `json:"firewall"`
	MACAddress string `json:"macAddress"`
}

// CloudInitSpec defines cloud-init configuration
type CloudInitSpec struct {
	User         string              `json:"user"`
	Password     string              `json:"password"`
	SSHKeys      []string            `json:"sshKeys"`
	SearchDomain string              `json:"searchDomain"`
	Nameservers  []string            `json:"nameservers"`
	IPConfig     map[string]IPConfig `json:"ipConfig"`
	// PackageUpgrade controls Proxmox's cloud-init `ciupgrade` — whether
	// the guest runs a full `apt dist-upgrade` on first boot. Proxmox VE
	// 8.2+ defaults this ON, which makes every fresh clone do a slow,
	// network-dependent upgrade (and snap refresh) during cloud-init's
	// final phase; on a flaky link that can wedge cloud-init indefinitely
	// and stall provisioning. openctl defaults it OFF (nil ⇒ ciupgrade=0)
	// so clones boot fast and deterministically. Set true to opt back in.
	// Requires Proxmox VE 8.2 or newer (the param didn't exist before).
	PackageUpgrade *bool `json:"packageUpgrade"`
	// Packages installs host packages on first boot via cloud-init
	// (`packages:` in the emitted vendor-data, with `package_update: true`
	// so the index is refreshed first). Used for node prerequisites such as
	// `open-iscsi` for Longhorn. Rendered into a per-VM cloud-init vendor
	// snippet (see the handler), not a Proxmox native `ci*` option — Proxmox
	// has none for arbitrary packages.
	Packages []string `json:"packages"`
	// RunCmd runs first-boot shell commands via cloud-init (`runcmd:` in the
	// emitted vendor-data), after the qemu-guest-agent enablement commands.
	// General node tuning / prerequisite hooks. Rendered into the same per-VM
	// vendor snippet as Packages.
	RunCmd []string `json:"runcmd"`
}

// IPConfig defines IP configuration
type IPConfig struct {
	IP      string `json:"ip"`
	Gateway string `json:"gateway"`
}

// ParseVMSpec parses the spec from a Resource into a VMSpec
func ParseVMSpec(r *protocol.Resource) (*VMSpec, error) {
	spec := &VMSpec{}

	if r.Spec == nil {
		return spec, nil
	}

	if node, ok := r.Spec["node"].(string); ok {
		spec.Node = node
	}

	if startOnCreate, ok := r.Spec["startOnCreate"].(bool); ok {
		spec.StartOnCreate = startOnCreate
	}

	if tmpl, ok := r.Spec["template"].(map[string]any); ok {
		spec.Template = &TemplateRef{}
		if name, ok := tmpl["name"].(string); ok {
			spec.Template.Name = name
		}
		if vmid, ok := tmpl["vmid"].(float64); ok {
			spec.Template.VMID = int(vmid)
		}
	}

	if img, ok := r.Spec["image"].(map[string]any); ok {
		spec.Image = &ImageRef{}
		if storage, ok := img["storage"].(string); ok {
			spec.Image.Storage = storage
		}
		if file, ok := img["file"].(string); ok {
			spec.Image.File = file
		}
		if contentType, ok := img["contentType"].(string); ok {
			spec.Image.ContentType = contentType
		}
		if format, ok := img["format"].(string); ok {
			spec.Image.Format = format
		}
		if targetStorage, ok := img["targetStorage"].(string); ok {
			spec.Image.TargetStorage = targetStorage
		}
		if targetFormat, ok := img["targetFormat"].(string); ok {
			spec.Image.TargetFormat = targetFormat
		}
	}

	if ci, ok := r.Spec["cloudImage"].(map[string]any); ok {
		spec.CloudImage = &CloudImageRef{}
		if url, ok := ci["url"].(string); ok {
			spec.CloudImage.URL = url
		}
		if storage, ok := ci["storage"].(string); ok {
			spec.CloudImage.Storage = storage
		}
		if checksum, ok := ci["checksum"].(string); ok {
			spec.CloudImage.Checksum = checksum
		}
		if templateName, ok := ci["templateName"].(string); ok {
			spec.CloudImage.TemplateName = templateName
		}
		if diskStorage, ok := ci["diskStorage"].(string); ok {
			spec.CloudImage.DiskStorage = diskStorage
		}
	}

	if osType, ok := r.Spec["osType"].(string); ok {
		spec.OSType = osType
	}
	if bios, ok := r.Spec["bios"].(string); ok {
		spec.BIOS = bios
	}
	if machine, ok := r.Spec["machine"].(string); ok {
		spec.Machine = machine
	}

	if agent, ok := r.Spec["agent"].(map[string]any); ok {
		spec.Agent = &AgentSpec{}
		if enabled, ok := agent["enabled"].(bool); ok {
			spec.Agent.Enabled = enabled
		}
	}

	if cpu, ok := r.Spec["cpu"].(map[string]any); ok {
		spec.CPU = &CPUSpec{}
		if cores, ok := cpu["cores"].(float64); ok {
			spec.CPU.Cores = int(cores)
		}
		if sockets, ok := cpu["sockets"].(float64); ok {
			spec.CPU.Sockets = int(sockets)
		}
		if cpuType, ok := cpu["type"].(string); ok {
			spec.CPU.Type = cpuType
		}
	}

	if devices, ok := r.Spec["hostPCI"].([]any); ok {
		for _, d := range devices {
			dev, ok := d.(map[string]any)
			if !ok {
				continue
			}
			h := HostPCISpec{}
			if device, ok := dev["device"].(string); ok {
				h.Device = device
			}
			if mapping, ok := dev["mapping"].(string); ok {
				h.Mapping = mapping
			}
			if pcie, ok := dev["pcie"].(bool); ok {
				h.PCIE = pcie
			}
			if primary, ok := dev["primaryGPU"].(bool); ok {
				h.PrimaryGPU = primary
			}
			if rombar, ok := dev["rombar"].(bool); ok {
				b := rombar
				h.ROMBAR = &b
			}
			if mdev, ok := dev["mdev"].(string); ok {
				h.MDev = mdev
			}
			if romfile, ok := dev["romfile"].(string); ok {
				h.ROMFile = romfile
			}
			spec.HostPCI = append(spec.HostPCI, h)
		}
	}

	if efi, ok := r.Spec["efiDisk"].(map[string]any); ok {
		spec.EFIDisk = &EFIDiskSpec{}
		if storage, ok := efi["storage"].(string); ok {
			spec.EFIDisk.Storage = storage
		}
		if t, ok := efi["type"].(string); ok {
			spec.EFIDisk.Type = t
		}
		if pek, ok := efi["preEnrolledKeys"].(bool); ok {
			b := pek
			spec.EFIDisk.PreEnrolledKeys = &b
		}
	}

	if mem, ok := r.Spec["memory"].(map[string]any); ok {
		spec.Memory = &MemorySpec{}
		if size, ok := mem["size"].(float64); ok {
			spec.Memory.Size = int(size)
		}
	}

	if disks, ok := r.Spec["disks"].([]any); ok {
		for _, d := range disks {
			if disk, ok := d.(map[string]any); ok {
				diskSpec := DiskSpec{}
				if name, ok := disk["name"].(string); ok {
					diskSpec.Name = name
				}
				if storage, ok := disk["storage"].(string); ok {
					diskSpec.Storage = storage
				}
				if size, ok := disk["size"].(string); ok {
					diskSpec.Size = size
				}
				if ssd, ok := disk["ssd"].(bool); ok {
					diskSpec.SSD = ssd
				}
				if discard, ok := disk["discard"].(bool); ok {
					diskSpec.Discard = discard
				}
				if iothread, ok := disk["iothread"].(bool); ok {
					diskSpec.IOThread = iothread
				}
				if backup, ok := disk["backup"].(bool); ok {
					b := backup
					diskSpec.Backup = &b
				}
				if cache, ok := disk["cache"].(string); ok {
					diskSpec.Cache = cache
				}
				spec.Disks = append(spec.Disks, diskSpec)
			}
		}
	}

	if networks, ok := r.Spec["networks"].([]any); ok {
		for _, n := range networks {
			if net, ok := n.(map[string]any); ok {
				netSpec := NetworkSpec{}
				if name, ok := net["name"].(string); ok {
					netSpec.Name = name
				}
				if bridge, ok := net["bridge"].(string); ok {
					netSpec.Bridge = bridge
				}
				if model, ok := net["model"].(string); ok {
					netSpec.Model = model
				}
				if vlan, ok := net["vlan"].(float64); ok {
					netSpec.VLAN = int(vlan)
				}
				if firewall, ok := net["firewall"].(bool); ok {
					netSpec.Firewall = firewall
				}
				if mac, ok := net["macAddress"].(string); ok {
					netSpec.MACAddress = mac
				}
				spec.Networks = append(spec.Networks, netSpec)
			}
		}
	}

	if ci, ok := r.Spec["cloudInit"].(map[string]any); ok {
		spec.CloudInit = &CloudInitSpec{
			IPConfig: make(map[string]IPConfig),
		}
		if user, ok := ci["user"].(string); ok {
			spec.CloudInit.User = user
		}
		if password, ok := ci["password"].(string); ok {
			spec.CloudInit.Password = password
		}
		if sshKeys, ok := ci["sshKeys"].([]any); ok {
			for _, k := range sshKeys {
				if key, ok := k.(string); ok {
					spec.CloudInit.SSHKeys = append(spec.CloudInit.SSHKeys, key)
				}
			}
		}
		if pu, ok := ci["packageUpgrade"].(bool); ok {
			spec.CloudInit.PackageUpgrade = &pu
		}
		if pkgs, ok := ci["packages"].([]any); ok {
			for _, p := range pkgs {
				if pkg, ok := p.(string); ok {
					spec.CloudInit.Packages = append(spec.CloudInit.Packages, pkg)
				}
			}
		}
		if cmds, ok := ci["runcmd"].([]any); ok {
			for _, c := range cmds {
				if cmd, ok := c.(string); ok {
					spec.CloudInit.RunCmd = append(spec.CloudInit.RunCmd, cmd)
				}
			}
		}
		if sd, ok := ci["searchDomain"].(string); ok {
			spec.CloudInit.SearchDomain = sd
		}
		if ns, ok := ci["nameservers"].([]any); ok {
			for _, s := range ns {
				if server, ok := s.(string); ok {
					spec.CloudInit.Nameservers = append(spec.CloudInit.Nameservers, server)
				}
			}
		}
		if ipConfig, ok := ci["ipConfig"].(map[string]any); ok {
			for iface, cfg := range ipConfig {
				if ipCfg, ok := cfg.(map[string]any); ok {
					ic := IPConfig{}
					if ip, ok := ipCfg["ip"].(string); ok {
						ic.IP = ip
					}
					if gw, ok := ipCfg["gateway"].(string); ok {
						ic.Gateway = gw
					}
					spec.CloudInit.IPConfig[iface] = ic
				}
			}
		}
	}

	return spec, nil
}

// ToProxmoxConfig converts a VMSpec to Proxmox configuration parameters
func (s *VMSpec) ToProxmoxConfig() map[string]any {
	params := make(map[string]any)

	if s.CPU != nil {
		if s.CPU.Cores > 0 {
			params["cores"] = s.CPU.Cores
		}
		if s.CPU.Sockets > 0 {
			params["sockets"] = s.CPU.Sockets
		}
		if s.CPU.Type != "" {
			params["cpu"] = s.CPU.Type
		}
	}

	if s.Memory != nil && s.Memory.Size > 0 {
		params["memory"] = s.Memory.Size
	}

	if s.OSType != "" {
		params["ostype"] = s.OSType
	}
	if s.BIOS != "" {
		params["bios"] = s.BIOS
	}
	if s.Machine != "" {
		params["machine"] = s.Machine
	}
	if s.Agent != nil && s.Agent.Enabled {
		params["agent"] = "1"
	}

	// EFI vars disk for OVMF/UEFI (and a prerequisite for GPU passthrough).
	if s.EFIDisk != nil && s.EFIDisk.Storage != "" {
		params["efidisk0"] = s.EFIDisk.configString()
	}

	// Host PCI passthrough devices → hostpci0, hostpci1, …
	i := 0
	for _, h := range s.HostPCI {
		if h.Device == "" && h.Mapping == "" {
			continue // nothing to attach
		}
		params[fmt.Sprintf("hostpci%d", i)] = h.configString()
		i++
	}

	for _, net := range s.Networks {
		model := net.Model
		if model == "" {
			model = "virtio"
		}
		// Proxmox format: <model>[=<mac>],bridge=<br>[,tag=<n>][,firewall=1]
		head := model
		if net.MACAddress != "" {
			head = fmt.Sprintf("%s=%s", model, net.MACAddress)
		}
		val := fmt.Sprintf("%s,bridge=%s", head, net.Bridge)
		if net.VLAN > 0 {
			val += fmt.Sprintf(",tag=%d", net.VLAN)
		}
		if net.Firewall {
			val += ",firewall=1"
		}
		params[net.Name] = val
	}

	if s.CloudInit != nil {
		// Default first-boot package upgrade OFF (see CloudInitSpec.PackageUpgrade).
		if s.CloudInit.PackageUpgrade != nil && *s.CloudInit.PackageUpgrade {
			params["ciupgrade"] = 1
		} else {
			params["ciupgrade"] = 0
		}
		if s.CloudInit.User != "" {
			params["ciuser"] = s.CloudInit.User
		}
		if s.CloudInit.Password != "" {
			params["cipassword"] = s.CloudInit.Password
		}
		if len(s.CloudInit.SSHKeys) > 0 {
			// Proxmox expects sshkeys to be URL-encoded with %20 for spaces (not +).
			// QueryEscape encodes everything properly but uses + for spaces,
			// so we replace + with %20 after encoding.
			encoded := url.QueryEscape(strings.Join(s.CloudInit.SSHKeys, "\n"))
			encoded = strings.ReplaceAll(encoded, "+", "%20")
			params["sshkeys"] = encoded
		}
		if s.CloudInit.SearchDomain != "" {
			params["searchdomain"] = s.CloudInit.SearchDomain
		}
		if len(s.CloudInit.Nameservers) > 0 {
			// Proxmox `nameserver` is space-separated.
			params["nameserver"] = strings.Join(s.CloudInit.Nameservers, " ")
		}

		for iface, cfg := range s.CloudInit.IPConfig {
			key := fmt.Sprintf("ipconfig%s", strings.TrimPrefix(iface, "net"))
			if cfg.IP == "dhcp" {
				params[key] = "ip=dhcp"
			} else if cfg.IP != "" {
				val := fmt.Sprintf("ip=%s", cfg.IP)
				if cfg.Gateway != "" {
					val += fmt.Sprintf(",gw=%s", cfg.Gateway)
				}
				params[key] = val
			}
		}
	}

	return params
}
