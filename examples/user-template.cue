// Example user-authored template.
//
// Drop a copy of this file (edited) into ~/.openctl/templates/ and it will
// show up in the controller's TemplateService — the UI's Templates picker
// and `openctl` — alongside the compiled-in starters. Files must end in
// `.cue`. A malformed file is logged and skipped, not fatal.
//
// Contract:
//   - `template:` is concrete metadata (must NOT reference `params`).
//   - `params:` is the struct the user's values are filled into at render.
//   - `resource:` is the manifest to apply; it must become fully concrete
//     once `params` are filled. Reference user values as `params.<name>`.
//
// A user template whose `template.name` matches a built-in overrides it.

template: {
	name:        "dev-vm"
	displayName: "Dev VM"
	description: "A small Ubuntu VM for development"
	apiVersion:  "proxmox.openctl.io/v1"
	kind:        "VirtualMachine"
	parameters: [
		{name: "hostname", type: "string", description: "VM name / hostname", required: true},
		{name: "node", type: "string", description: "Proxmox node", optionsKind: "ProxmoxNode", required: true},
		{name: "cores", type: "int", description: "vCPUs", default: 2},
		{name: "memoryMB", type: "int", description: "RAM in MB", default: 4096},
	]
}

params: {...}

resource: {
	apiVersion: "proxmox.openctl.io/v1"
	kind:       "VirtualMachine"
	metadata: {
		name: params.hostname
		annotations: "openctl.io/template": "dev-vm"
	}
	spec: {
		node:   params.node
		cpu: cores:    params.cores
		memory: size:  params.memoryMB
		startOnCreate: true
	}
}
