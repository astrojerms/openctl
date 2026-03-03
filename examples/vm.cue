import "openctl.io/schemas/proxmox"

proxmox.#VirtualMachine & {
	metadata: name: "web-01"
	spec: {
		node: "pve1"
		template: name: "ubuntu-22.04"
		cpu: cores: 4
		memory: size: 8192
		cloudInit: {
			user: "ubuntu"
			sshKeys: ["ssh-ed25519 AAAA... user@host"]
			ipConfig: net0: ip: "dhcp"
		}
	}
}
