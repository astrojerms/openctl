package k3s

import "openctl.io/schemas/base"

#Cluster: base.#Resource & {
	apiVersion: "k3s.openctl.io/v1"
	kind:       "Cluster"
	spec:       #ClusterSpec
}

// #NodeSize captures the per-VM sizing knobs. Used both as the default
// applied to every node and as an override on individual node pools.
// Each field is optional so callers can override only what they need
// and inherit the rest.
#NodeSize: {
	// vCPUs per node.
	cpus?: int & >=1
	// RAM per node in MiB. Minimum 512 matches the VM schema.
	memoryMB?: int & >=512
	// Root disk size per node in GiB.
	diskGB?: int & >=1
}

#ClusterSpec: {
	// Which infrastructure provider runs the VMs. Currently only
	// "proxmox" is implemented; other providers may follow.
	compute: {
		// Provider name; only "proxmox" is wired today.
		provider: string
		// Provider-specific context (e.g. a named Proxmox endpoint
		// from the controller config). Defaults to the controller's
		// single configured provider when omitted.
		context?: string
		// Where the k3s node VMs come from. Either a cloud-image URL
		// to download and templatize, or the name of an existing
		// Proxmox template. Exactly one of url/template is required.
		image: {
			// Cloud-image URL (e.g. an Ubuntu cloud image .img).
			url?: string
			// Existing Proxmox template name to clone from.
			template?: string
			// Storage hosting the downloaded image and the template VM.
			storage?: string
			// Storage for the cloned node disks. Defaults to storage.
			diskStorage?: string
		}
		// Default node size applied to every control-plane and worker
		// VM unless overridden on the pool.
		default?: #NodeSize
	}

	// Node topology of the cluster.
	nodes: {
		// Control-plane (server) nodes. Three or five gives HA; one
		// works for dev clusters.
		controlPlane: {
			// Number of control-plane nodes. 1 is single-node; 3/5 give HA.
			count: int & >=1 | *1
			// Optional size override for the control-plane pool.
			size?: #NodeSize
		}
		// Worker (agent) node pools. Each pool can have its own size.
		workers?: [...{
			// Pool name; used as the node-name suffix (e.g. "<cluster>-gpu-0").
			name: string
			// Number of nodes in this pool.
			count: int & >=1
			// Optional size override for this pool.
			size?: #NodeSize
		}]
	}

	// Network configuration for node VMs.
	network?: {
		// Proxmox bridge attached to each node's NIC.
		bridge?: string | *"vmbr0"
		// Use DHCP for node IPs. Set false and provide staticIPs to
		// allocate from a known pool.
		dhcp: bool | *true
		// Static IP allocation. When set, dhcp is forced off and node
		// IPs are assigned sequentially starting from startIP.
		staticIPs?: {
			// First IP allocated to a node. Successive nodes increment
			// the last octet.
			startIP: base.#IPv4
			// Default gateway.
			gateway: base.#IPv4
			// Netmask in CIDR-suffix form (e.g. "24").
			netmask: string
		}
	}

	// k3s installer configuration.
	k3s?: {
		// k3s version tag (e.g. "v1.29.0+k3s1"). Empty installs latest.
		version?: string
		// Pod CIDR passed to k3s. Defaults to k3s's own default.
		clusterCIDR?: string
		// Service CIDR passed to k3s. Defaults to k3s's own default.
		serviceCIDR?: string
		// Extra args appended to the k3s install command on every node.
		extraArgs?: [...string]
	}

	// SSH configuration the controller uses to reach each node for
	// provisioning. The private key must already exist on the controller
	// host; the corresponding public key is normally listed in publicKeys.
	ssh: {
		// SSH user inside the guest. Cloud images typically default to
		// "ubuntu" or "debian".
		user: string | *"ubuntu"
		// Absolute path on the controller host to the SSH private key.
		privateKeyPath: string
		// Public keys to inject into each node via cloud-init. Include
		// the matching public key for privateKeyPath here.
		publicKeys?: [...string]
	}
}
