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
