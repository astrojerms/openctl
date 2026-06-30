package proxmox

import "openctl.io/schemas/base"

// #ProxmoxNode is observed-only: openctl discovers nodes from the Proxmox
// API rather than provisioning them. The empty spec keeps the schema
// surface consistent with applyable kinds so the form editor and validator
// don't need a special case.
#ProxmoxNode: base.#Resource & {
	apiVersion: "proxmox.openctl.io/v1"
	kind:       "ProxmoxNode"
	spec:       #ProxmoxNodeSpec
}

#ProxmoxNodeSpec: {}
