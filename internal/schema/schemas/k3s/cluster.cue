package k3s

import "openctl.io/schemas/base"

#Cluster: base.#Resource & {
	apiVersion: "k3s.openctl.io/v1"
	kind:       "Cluster"
	spec:       #ClusterSpec
}

#ClusterSpec: {
	compute: {
		provider:  string
		context?:  string
		image: {url?: string, template?: string, storage?: string, diskStorage?: string}
		default?: {cpus?: int, memoryMB?: int, diskGB?: int}
	}
	nodes: {
		controlPlane: {count: int & >=1 | *1, size?: _}
		workers?: [...{name: string, count: int, size?: _}]
	}
	network?: {
		bridge?: string | *"vmbr0"
		dhcp:    bool | *true
		staticIPs?: {startIP: base.#IPv4, gateway: base.#IPv4, netmask: string}
	}
	k3s?: {version?: string, clusterCIDR?: string, serviceCIDR?: string, extraArgs?: [...string]}
	ssh: {user: string | *"ubuntu", privateKeyPath: string, publicKeys?: [...string]}
}
