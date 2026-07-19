package proxmox

import "openctl.io/schemas/base"

#VirtualMachine: base.#Resource & {
	apiVersion: "proxmox.openctl.io/v1"
	kind:       "VirtualMachine"
	spec:       #VMSpec
	status?:    #VMStatus
}

// #VMStatus documents the observed fields a VirtualMachine exposes. Notably
// status.ip is $ref-able (e.g. a K3sNode reads its VM's IP). Open + all-optional
// so it describes without constraining the provider's output or rejecting a
// manifest.
#VMStatus: {
	// Proxmox numeric VM id.
	vmid?: int
	// Power state, e.g. "running", "stopped".
	state?: string
	// Guest IP address (from the QEMU guest agent). Empty until the agent
	// reports it; a common $ref target.
	ip?: string
	// Uptime in seconds.
	uptime?: int
	// Fractional CPU usage (0–1) and used memory in bytes.
	cpuUsed?: number
	memUsed?: int
	...
}

#VMSpec: {
	// Provider context selecting which configured Proxmox endpoint hosts
	// this VM. Omit to use the controller's default context. Lets a single
	// set of resources (e.g. a k3s cluster) span multiple Proxmox endpoints.
	context?: string

	// Proxmox node (host) the VM lives on, e.g. "pve1". The UI resolves
	// this against the ProxmoxNode kind (observed from the Proxmox API)
	// and renders a dropdown of available nodes.
	node: string @options(kind="ProxmoxNode")

	// Source for VM creation. Pick exactly one via the picker; the
	// three alternatives share the "image-source" @oneOf group so the
	// form editor renders a radio at the top and only reveals the
	// sub-form for the chosen source.
	// Clones from a Proxmox template by name or vmid.
	template?: {
		// Template name in Proxmox (preferred over vmid).
		name?: string
		// Template vmid; used when the name isn't unique.
		vmid?: int
	} @oneOf(group="image-source")
	// Downloads a cloud image URL and provisions a reusable template
	// behind the scenes, then clones it. The template-name is generated
	// from the URL unless overridden.
	cloudImage?: {
		// HTTP(S) URL of the cloud image (e.g. an Ubuntu .img).
		url: string
		// Proxmox storage that will host the downloaded image and template VM disk.
		storage: string
		// Optional checksum to verify the download (e.g. "sha256:abc123").
		checksum?: string
		// Override the auto-generated template name (otherwise derived from URL).
		templateName?: string
		// Where to place cloned VM disks. Defaults to storage.
		diskStorage?: string
	} @oneOf(group="image-source")
	// Imports an existing disk image from Proxmox storage.
	image?: {
		// Storage that contains the source image.
		storage: string
		// Image filename, or full volume ID like "local:import/image.qcow2".
		file: string
		// Storage content type. Use "import" for downloaded cloud images.
		contentType?: "images" | "iso" | "import"
		// Source disk format if not inferable from extension.
		format?: "qcow2" | "raw" | "vmdk"
		// Storage to import the disk into. Defaults to source storage.
		targetStorage?: string
		// Format to convert to during import.
		targetFormat?: "qcow2" | "raw"
	} @oneOf(group="image-source")

	// CPU configuration. Total vCPUs = cores * sockets.
	cpu?: {
		// Cores per socket.
		cores: int & >=1 | *2
		// Number of CPU sockets exposed to the guest.
		sockets?: int & >=1 | *1
		// Proxmox CPU model. "host" exposes the physical CPU's full feature
		// set (AVX etc.) — what compute / GPU / local-model workloads want.
		// Omit for the Proxmox default (kvm64).
		type?: string
	}
	// Memory size in MiB.
	memory?: {
		// Allocated RAM in MiB (minimum 512).
		size: int & >=512
	}

	// Linux guest kernel/OS hint; "l26" covers modern Linux. Windows uses
	// the win* values.
	osType?: "l24" | "l26" | "other" | "wxp" | "w2k" | "w2k3" | "w2k8" | "wvista" | "win7" | "win8" | "win10" | "win11" | "solaris"
	// Firmware. ovmf (UEFI) is needed for secure boot / GPT-only guests;
	// seabios is the traditional BIOS.
	bios?: "seabios" | "ovmf"
	// Virtual machine type. q35 is recommended for modern guests with
	// PCIe; pc/i440fx is the legacy default. q35 is required for PCIe
	// passthrough (hostPCI with pcie=true).
	machine?: "pc" | "q35" | "i440fx"

	// EFI vars disk for OVMF/UEFI firmware. Set this whenever bios=ovmf —
	// without it OVMF has nowhere to persist boot config. Also a prerequisite
	// for a working GPU-passthrough VM (q35 + ovmf + efiDisk + hostPCI).
	efiDisk?: {
		// Proxmox storage ID to allocate the EFI vars volume on (e.g. "local-lvm").
		storage: string @options(kind="ProxmoxNode", field="status.storages", dependsOn="spec.node")
		// OVMF firmware size. "4m" (default) supports secure boot / larger var
		// stores; "2m" is the legacy smaller image.
		type?: "4m" | "2m"
		// Enroll Microsoft's secure-boot keys. Off by default — GPU-passthrough
		// and Linux guests generally run without secure boot.
		preEnrolledKeys?: bool | *false
	}

	// Host PCI device passthrough (e.g. a GPU for a local model). Needs
	// machine=q35, bios=ovmf, and an efiDisk. Give exactly one of device (a
	// raw PCI address) or mapping (a Proxmox resource-mapping id — portable
	// across hosts). The VM must be pinned (spec.node) to the host that owns
	// the device.
	hostPCI?: [...{
		// Raw host PCI address, e.g. "0000:01:00" or "0000:01:00.0".
		device?: string @oneOf(group="pci-source")
		// Proxmox resource-mapping id (preferred — survives host moves).
		mapping?: string @oneOf(group="pci-source")
		// Expose as PCIe rather than legacy PCI. Requires machine=q35.
		pcie?: bool | *false
		// Mark as the primary GPU (Proxmox x-vga=1). Disables the emulated VGA.
		primaryGPU?: bool | *false
		// Map the device ROM BAR. On by default in Proxmox; set false for GPUs
		// that misbehave with it mapped.
		rombar?: bool
		// Request a mediated device (vGPU) of this type instead of the whole card.
		mdev?: string
		// Custom device ROM filename under Proxmox's /usr/share/kvm.
		romfile?: string
	}]

	// QEMU guest agent. Enabling needs qemu-guest-agent installed in the
	// guest; openctl uses it for IP detection.
	agent?: {
		// Whether QEMU guest agent is enabled.
		enabled: bool | *false
	}

	// Disks to attach. Names are Proxmox bus-slot strings (e.g. "scsi0",
	// "virtio0"). For cloned VMs, listing a disk with size= resizes it
	// and applies any flags below to the existing disk config.
	disks?: [...{
		// Bus and slot, e.g. "scsi0" or "virtio0". Bus is parsed from the
		// prefix; common buses are scsi (recommended for cloud images),
		// virtio (fastest), sata, and ide.
		name: string
		// Proxmox storage ID (e.g. "local-lvm", "nfs-vmstore").
		storage: string @options(kind="ProxmoxNode", field="status.storages", dependsOn="spec.node")
		// Target disk size with unit suffix, e.g. "50G", "1T".
		size: string
		// Advertise the disk as an SSD to the guest. Lets the OS issue
		// TRIM and pick SSD-friendly schedulers.
		ssd?: bool | *false
		// Enable TRIM/UNMAP passthrough so freed blocks are reclaimed
		// in the underlying storage (zfs/lvm-thin).
		discard?: bool | *false
		// Run this disk's I/O on its own thread. Improves throughput
		// on virtio-scsi-single and virtio with multi-disk VMs.
		iothread?: bool | *false
		// Include this disk in vzdump backups. Defaults to Proxmox's
		// own default (true). Set false to skip a scratch disk.
		backup?: bool
		// Cache mode. Proxmox default is "none" (safest). "writeback"
		// is fastest but risks data loss on host crash.
		cache?: "none" | "writethrough" | "writeback" | "unsafe" | "directsync"
	}]

	// Network interfaces. Names are Proxmox-style "net0", "net1", ...
	networks?: [...{
		// Interface name, e.g. "net0".
		name: string
		// Proxmox bridge to attach to.
		bridge: string | *"vmbr0" @options(kind="ProxmoxNode", field="status.bridges", dependsOn="spec.node")
		// NIC model. virtio is fastest; e1000 is most-compatible for old guests.
		model?: "virtio" | "e1000" | "rtl8139" | "vmxnet3" | "e1000e"
		// VLAN tag (1-4094). Untagged when omitted.
		vlan?: int & >=1 & <=4094
		// Whether the Proxmox firewall is enabled on this NIC.
		firewall?: bool | *false
		// Optional MAC address override. Proxmox auto-assigns when empty.
		macAddress?: string
	}]

	// Cloud-init configuration injected at first boot. Requires the guest
	// to be a cloud-init image.
	cloudInit?: {
		// Default user created in the guest.
		user?: string
		// Initial password. Prefer a secret reference (base.#Secret) so the
		// value stays out of the git-synced manifest; a bare string is still
		// accepted for back-compat but is committed verbatim. Prefer SSH keys
		// over passwords entirely.
		password?: (string | base.#Secret) @secret()
		// SSH public keys for the default user.
		sshKeys?: [...string]
		// DNS search domain (e.g. "lan").
		searchDomain?: string
		// DNS resolver addresses to write to /etc/resolv.conf.
		nameservers?: [...string]
		// Per-interface IP configuration. Key is the interface name (e.g.
		// "net0"). Use "dhcp" as ip to request DHCP, or "<addr>/<cidr>"
		// for static with optional gateway.
		ipConfig?: {[string]: {
			// IP address with CIDR (e.g. "192.168.1.10/24"), or "dhcp".
			ip: string
			// Default gateway. Required for static IPs that need outbound routing.
			gateway?: string
		}}
	}

	// Whether to power the VM on after creation.
	startOnCreate?: bool | *true
}
