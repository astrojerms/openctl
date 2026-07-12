package provider

// helmReleaseSchema is the CUE schema advertised for the HelmRelease kind. It is
// compiled standalone by the controller (no openctl module imports) and stays
// open with a trailing `...`. spec.kubeconfig is typically written as a
// `$secret` reference in the manifest; openctl resolves it to the concrete
// kubeconfig string before the plugin sees it. Likewise spec.values entries may
// use `$secret`/`valueFrom`.
const helmReleaseSchema = `
#HelmRelease: {
	apiVersion: "k8s.openctl.io/v1"
	kind:       "HelmRelease"
	metadata: {
		name: string
		...
	}
	spec: {
		// Target cluster credentials — supply exactly one:
		//  - kubeconfigPath: a path on the controller host. Typically a k3s
		//    Cluster's kubeconfig resolved via $ref, e.g.
		//      kubeconfigPath: {$ref: {apiVersion: "k3s.openctl.io/v1", kind:
		//        "Cluster", name: "edge", field: "status.outputs.kubeconfigPath"}}
		//    openctl resolves the $ref (and DAG-orders this release after the
		//    cluster) before the provider runs; only the path is stored.
		//  - kubeconfig: inline content for an external cluster (usually a
		//    $secret).
		kubeconfigPath?: string
		kubeconfig?:     string
		// namespace to install into. Defaults to "default".
		namespace?: string
		// createNamespace creates the namespace if absent.
		createNamespace?: bool
		// releaseName defaults to metadata.name.
		releaseName?: string
		chart: {
			// repo is an HTTP repo URL ("https://…") or an OCI ref ("oci://…").
			repo: string
			// name is the chart name (omit when an OCI repo already names it).
			name?: string
			// version pins the chart version (recommended).
			version?: string
		}
		// values is the Helm values overlay; entries may use $secret/valueFrom.
		values?: {...}
		// wait blocks until the release's resources report ready.
		wait?: bool
		// timeout for wait, as a Go duration (e.g. "5m"). Defaults to 5m.
		timeout?: string
	}
	...
}
`

// manifestSchema is the CUE schema for the Manifest kind: server-side apply of
// raw Kubernetes YAML. The credential fields mirror HelmRelease (kubeconfigPath
// via $ref, or inline kubeconfig via $secret).
const manifestSchema = `
#Manifest: {
	apiVersion: "k8s.openctl.io/v1"
	kind:       "Manifest"
	metadata: {
		name: string
		...
	}
	spec: {
		// See HelmRelease: supply exactly one of kubeconfigPath ($ref to a
		// Cluster's status.outputs.kubeconfigPath) or kubeconfig (inline, $secret).
		kubeconfigPath?: string
		kubeconfig?:     string
		// manifest is one or more Kubernetes objects as a YAML document (multi-doc
		// with "---" is supported). Server-side-applied with field manager
		// "openctl-k8s"; objects that leave the manifest on a later apply are pruned.
		manifest: string
	}
	...
}
`

// platformSchema is the CUE schema for the opt-in Platform composite: a curated,
// infra-coupled platform layer. Nothing is enabled by default. Each component
// installs a Helm release; disabling a previously-enabled one uninstalls it.
const platformSchema = `
#Platform: {
	apiVersion: "k8s.openctl.io/v1"
	kind:       "Platform"
	metadata: {
		name: string
		...
	}
	spec: {
		// Target cluster (same as HelmRelease): kubeconfigPath ($ref to a
		// Cluster's status.outputs.kubeconfigPath) or inline kubeconfig ($secret).
		kubeconfigPath?: string
		kubeconfig?:     string
		// Component toggles — opt in explicitly; each has optional overrides:
		//   { enabled: bool, namespace?: string, wait?: bool,
		//     chart?: {repo?, name?, version?}, values?: {...} }
		// Traefik is the ingress. cloudflared wires a Cloudflare Tunnel; put its
		// run token in cloudflared.values as a $secret (openctl has no
		// action-output→secret bridge yet, so run the Tunnel's get-token once and
		// store the token).
		traefik?: {...}
		cloudflared?: {...}
		// argocd installs Argo CD (bootstrap). Pair with an ArgoApplications
		// resource to aggregate its Applications into openctl.
		argocd?: {...}
	}
	...
}
`

// argoApplicationsSchema is the CUE schema for the read-only ArgoApplications
// aggregation kind: it surfaces a cluster's Argo CD Applications (name + health
// + sync) into openctl for the unified view. Creating Applications is done via
// the Manifest kind.
const argoApplicationsSchema = `
#ArgoApplications: {
	apiVersion: "k8s.openctl.io/v1"
	kind:       "ArgoApplications"
	metadata: {
		name: string
		...
	}
	spec: {
		// Target cluster (same as HelmRelease): kubeconfigPath ($ref) or inline
		// kubeconfig ($secret).
		kubeconfigPath?: string
		kubeconfig?:     string
		// namespace Argo CD runs in. Defaults to "argocd".
		namespace?: string
	}
	...
}
`
