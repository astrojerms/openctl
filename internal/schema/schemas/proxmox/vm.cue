package proxmox

import "openctl.io/schemas/base"

#VirtualMachine: base.#Resource & {
	apiVersion: "proxmox.openctl.io/v1"
	kind:       "VirtualMachine"
	spec:       #VMSpec
}

#VMSpec: {
	node:        string
	template?:   {name?: string, vmid?: int}
	cloudImage?: {url: string, storage: string, ...}
	image?:      {storage: string, file: string, ...}
	cpu?:        {cores: int & >=1 | *2, sockets?: int}
	memory?:     {size: int & >=512}
	disks?:      [...{name: string, storage: string, size: string}]
	networks?:   [...{name: string, bridge: string | *"vmbr0", model?: string}]
	cloudInit?:  {user?: string, sshKeys?: [...string], ipConfig?: _}
	agent?:      {enabled: bool}
	startOnCreate?: bool | *true
}
