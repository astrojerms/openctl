package k3s

import "openctl.io/schemas/base"

#Cluster: base.#Resource & {
	apiVersion: "k3s.openctl.io/v1"
	kind:       "Cluster"
	spec:       #ClusterSpec
	status?:    #ClusterStatus
}

// #ClusterStatus documents the observed fields a Cluster exposes once it's up —
// notably status.outputs, the stable results other resources $ref (e.g. a
// HelmRelease's kubeconfigPath). Descriptive, not prescriptive: every field is
// optional and each level is open (...), so declaring it never rejects a
// manifest or constrains what the provider actually emits — it just tells
// tooling what you can reference.
#ClusterStatus: {
	// Coarse lifecycle phase (e.g. "Ready", "Provisioning").
	phase?: string
	// Human-readable detail about the current phase.
	message?: string
	// outputs are the stable, $ref-able results of standing up the cluster.
	outputs?: {
		// Path on the controller host to the cluster's kubeconfig file. The
		// canonical $ref target for deploying workloads onto this cluster.
		kubeconfigPath?: string
		// IP address of the first control-plane node (the API server).
		serverIP?: string
		// Per-node openctl-k3s-agent connection details.
		agent?: {
			// Directory holding the cluster's agent cert bundle.
			bundleDir?: string
			// Path to the agent CA certificate.
			caPath?: string
			// Path to the agent client certificate / key.
			clientCertPath?: string
			clientKeyPath?:  string
			// Agent mTLS port.
			port?: int
			// node name → IP address.
			endpoints?: {[string]: string}
			...
		}
		...
	}
	...
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

// #PlacementTarget is one {endpoint, host} slot a node pool spreads across.
// A pool's nodes are assigned to its targets round-robin. Naming different
// contexts spreads the pool across provider endpoints — a control plane
// spread this way keeps etcd quorum when a whole endpoint fails.
#PlacementTarget: {
	// Provider context (endpoint). Empty inherits the pool's / cluster's.
	context?: string
	// Host within that endpoint (e.g. a Proxmox node name). Empty uses the
	// endpoint's default host.
	node?: string
}

// #GPU requests PCI/GPU passthrough for every node in a pool (e.g. a one-node
// pool that runs a local model). Setting it builds the pool's VMs for
// passthrough: q35 + OVMF + an EFI vars disk + cpu type "host". Pin the pool
// to the host(s) that own the device via the pool's nodes/targets.
#GPU: {
	// Host PCI devices attached to each node in the pool. Give exactly one of
	// device or mapping per entry.
	devices: [...{
		// Raw host PCI address, e.g. "0000:01:00" or "0000:01:00.0".
		device?: string
		// Proxmox resource-mapping id (preferred — portable across hosts).
		mapping?: string
		// Mark as the primary GPU (Proxmox x-vga=1).
		primaryGPU?: bool | *false
		// Request a mediated device (vGPU) of this type instead of the whole card.
		mdev?: string
	}]
	// Proxmox storage to allocate the OVMF EFI vars disk on (e.g. "local-lvm").
	// Required — UEFI/passthrough needs the vars disk.
	efiStorage: string
	// Proxmox CPU model. Defaults to "host" (full physical CPU feature set).
	cpuType?: string | *"host"
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
		// Cluster-wide default pool of provider hosts (e.g. Proxmox node
		// names) to spread VMs across, round-robin within each node pool.
		// Omit to use the provider's single configured host. A per-pool
		// nodes list overrides this for that pool.
		nodes?: [...string]
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
			// Provider endpoint for the control-plane pool. Overrides
			// compute.context; places all CP VMs on this endpoint.
			context?: string
			// Provider hosts to spread control-plane VMs across, round-robin.
			// Overrides compute.nodes for this pool. Three replicas over
			// three hosts land one each, keeping etcd quorum across hosts.
			nodes?: [...string]
			// General placement: {context, node} slots the control plane is
			// spread across round-robin. Use this to spread the CP across
			// endpoints (cross-endpoint HA quorum). Overrides context/nodes.
			targets?: [...#PlacementTarget]
			// GPU/PCI passthrough for the control-plane VMs. Rare (GPUs usually
			// belong on workers) but supported for symmetry.
			gpu?: #GPU
		}
		// Worker (agent) node pools. Each pool can have its own size.
		workers?: [...{
			// Pool name; used as the node-name suffix (e.g. "<cluster>-gpu-0").
			name: string
			// Number of nodes in this pool.
			count: int & >=1
			// Optional size override for this pool.
			size?: #NodeSize
			// Provider endpoint for this worker pool. Overrides compute.context.
			context?: string
			// Provider hosts to spread this pool's VMs across, round-robin.
			// Overrides compute.nodes for this pool.
			nodes?: [...string]
			// General placement: {context, node} slots this pool is spread
			// across round-robin. Overrides context/nodes.
			targets?: [...#PlacementTarget]
			// GPU/PCI passthrough for every node in this pool. Pin the pool to
			// the host(s) with the device via nodes/targets.
			gpu?: #GPU
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
		// Per-endpoint network config for spreading one cluster across
		// endpoints on DIFFERENT subnets (separate-L2 spread). Keyed by
		// placement context; a node inherits the block for the context it
		// lands on. When set, dhcp is forced off and each node's IP is
		// allocated from its own context's range. Omit for single-L2
		// clusters. See docs/k3s-separate-l2-spread.md.
		perContext?: [string]: {
			// Proxmox bridge for nodes on this endpoint.
			bridge?: string
			// Static-IP range for nodes on this endpoint.
			staticIPs?: {
				startIP: base.#IPv4
				gateway?: base.#IPv4
				netmask?: string
			}
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
