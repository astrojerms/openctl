package k3s

import "openctl.io/schemas/base"

// #AgentInstall installs the openctl-k3s-agent (mTLS control plane
// used by the reconciler to reach a node) on a single VM. Phase 8
// step 3: introduces the resource kind so agent install becomes a
// declarative unit that can be authored on its own or emitted by a
// future Cluster.Plan.
//
// The agent needs mTLS material from a per-cluster CA. Rather than
// introducing a CertificateAuthority resource kind, AgentInstall
// piggybacks on the CA bundle that the k3s Cluster provider persists
// at ~/.openctl/state/k3s/<clusterName>/ and mints a fresh server
// cert for this node if one isn't already in the bundle. That means
// AgentInstall today requires an existing Cluster to exist for the
// bundle to load from; standalone K3sNode installs (step 2) do not
// currently ship an agent.
#AgentInstall: base.#Resource & {
	apiVersion: "k3s.openctl.io/v1"
	kind:       "AgentInstall"
	spec:       #AgentInstallSpec
}

#AgentInstallSpec: {
	// Reference to the VM the agent installs on. The provider reads
	// status.ip and metadata.name from the target.
	vmRef: base.#Ref

	// Name of the k3s Cluster whose CA bundle backs this agent's
	// mTLS material. Must match an existing Cluster's metadata.name
	// — the provider looks up the bundle at
	// ~/.openctl/state/k3s/<clusterName>/.
	clusterName: string

	// Optional deterministic IP for the target VM. Same semantics
	// as K3sNode.spec.vmIP — Cluster.Plan populates this for
	// static-IP clusters so the QGA-based IP wait is skipped.
	vmIP?: string

	// SSH credentials for reaching the target VM. Matches the shape
	// used by K3sNode.spec.ssh so users can share values.
	ssh: {
		user:           string | *"ubuntu"
		privateKeyPath: string
	}
}
