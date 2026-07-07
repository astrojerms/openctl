// A parameterized VM manifest (Part B: CUE --values). Fields left abstract
// (node, cpu.cores) or with defaults (memory.size) are filled by a values
// file at apply time:
//
//   openctl ctl apply -f vm-parameterized.cue --values vm-values.cue
//
// CUE unifies the two: a concrete value in the values file satisfies an
// abstract constraint here, an override replaces a default, and a conflict
// (two different concrete values) fails loudly instead of silently winning.
apiVersion: "proxmox.openctl.io/v1"
kind:       "VirtualMachine"
metadata: name: "app-01"
spec: {
	node: string // required — must be supplied by a values file
	template: name: "ubuntu-22.04-cloudinit"
	cpu: {
		cores:   int | *2 // default 2, override in values
		sockets: 1
	}
	memory: size: int | *2048
	disks: [{
		name:    "scsi0"
		storage: string // required
		size:    "20G"
	}]
	networks: [{
		name:   "net0"
		bridge: string | *"vmbr0"
	}]
}
