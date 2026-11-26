// Supply local context, node, template, storage, PCI mapping, network, and SSH
// values with: openctl ctl apply -f vm.cue --values /path/to/local-values.cue
import "openctl.io/schemas/proxmox"

proxmox.#VirtualMachine & {
	metadata: {
		name: "jellyfin"
		labels: role: "media-server"
	}
	spec: {
		context: string
		node: string
		template: vmid: int
		cpu: {cores: 4, sockets: 1, type: "host"}
		memory: size: 4096
		osType: "l26"
		bios: "ovmf"
		machine: "q35"
		efiDisk: {storage: string, type: "4m", preEnrolledKeys: false}
		hostPCI: [{mapping: string, pcie: true}]
		agent: enabled: true
		disks: [{
			name: "scsi0"
			storage: string
			size: "32G"
			ssd: true
			discard: true
			iothread: true
		}]
		networks: [{name: "net0", bridge: string, model: "virtio"}]
		cloudInit: {
			user: "ubuntu"
			sshKeys: [string, ...string]
			packageUpgrade: false
			packages: ["qemu-guest-agent"]
			ipConfig: net0: {ip: string, gateway: string}
			nameservers: [string, ...string]
		}
		startOnCreate: true
	}
}
