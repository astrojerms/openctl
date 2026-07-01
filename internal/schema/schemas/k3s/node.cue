package k3s

import "openctl.io/schemas/base"

// #K3sNode is one k3s install (server or agent) on one VM. Phase 8
// step 2: introduces the resource kind, with a fully-declarative
// spec that uses ResourceRefs to point at its dependencies (the VM
// it runs on and, for non-first nodes, another K3sNode to pull the
// join token from). No composite Cluster.Plan yet — users author
// K3sNode manifests directly.
#K3sNode: base.#Resource & {
	apiVersion: "k3s.openctl.io/v1"
	kind:       "K3sNode"
	spec:       #K3sNodeSpec
}

#K3sNodeSpec: {
	// Reference to the VM this k3s installs on. The provider reads
	// status.ip from the target to figure out where to SSH — the VM
	// must have run and reported its IP (either static or via QEMU
	// guest agent) before the K3sNode's apply can complete.
	vmRef: base.#Ref

	// Role: "server" is a control-plane node; "agent" is a worker.
	// First-server semantics (initialize the cluster vs join an
	// existing one) are derived from joinFrom: absent → initialize,
	// present → join.
	role: "server" | "agent"

	// Reference to an existing K3sNode (must be a server) to pull
	// the join token from. Resolved to that node's
	// status.nodeToken. Empty on the very first server; required
	// for every other node.
	joinFrom?: base.#Ref

	// Reference to the same node (a server) whose IP is used as
	// the k3s control-plane endpoint. Usually the same as
	// joinFrom, but exposed separately in case a load balancer or
	// alternative address is preferred later. If empty, defaults
	// to the joinFrom target's IP.
	joinURLFrom?: base.#Ref

	// Optional k3s version tag (e.g. "v1.29.0+k3s1"). Empty
	// installs the latest.
	version?: string

	// Extra args passed to the k3s install (e.g.
	// ["--cluster-cidr=10.42.0.0/16", "--disable=traefik"]).
	extraArgs?: [...string]

	// Optional deterministic IP for the target VM. When set, the
	// provider skips polling vmRef.status.ip and SSHes directly
	// here. Cluster.Plan populates this for static-IP clusters so
	// the QGA-based IP-wait isn't required. Standalone K3sNode
	// authors typically leave this blank and let vmRef.status.ip
	// carry the address.
	vmIP?: string
	// SSH credentials for reaching the target VM. Matches the
	// shape used by Cluster.spec.ssh so users can share values.
	ssh: {
		user:           string | *"ubuntu"
		privateKeyPath: string
	}
}
