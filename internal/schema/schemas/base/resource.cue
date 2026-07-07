package base

#Resource: {
	apiVersion: =~"^[a-z0-9]+\\.openctl\\.io/v[0-9]+.*$"
	kind:       string
	metadata:   #Metadata
	spec?:      _
	status?:    _
}

#Metadata: {
	name:         =~"^[a-z0-9][a-z0-9-]*[a-z0-9]$" | =~"^[a-z0-9]$"
	namespace?:   string
	labels?:      {[string]: string}
	annotations?: {[string]: string}
}

#IPv4: =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}$"
#CIDR: =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/[0-9]{1,2}$"

// #Ref is the CUE helper for authoring ResourceRefs — spec-level
// placeholders that the controller resolves pre-Apply by calling
// Get on the referenced resource. Use in any spec field where the
// value should come from another resource's status (e.g. a k3s
// worker's join token from cp-0.status.nodeToken).
//
// Wire shape matches what refs.Resolver expects: {$ref: {...}}.
// The doubled-dollar-signs are CUE syntax that avoids conflict
// with the shell-style interpolation in template docs.
//
// Example (in a K3sNode spec):
//   joinToken: base.#Ref & {
//       $ref: {
//           apiVersion: "k3s.openctl.io/v1"
//           kind:       "K3sNode"
//           name:       "cp-0"
//           field:      "status.nodeToken"
//       }
//   }
#Ref: {
	"$ref": {
		apiVersion: string
		kind:       string
		name:       string
		// Dotted path within the resolved resource (e.g.
		// "status.nodeToken"). Empty resolves to the whole resource.
		field?: string
	}
}

// #Secret is the CUE helper for authoring secret references — a spec-level
// placeholder the controller resolves at Apply time to a real secret value,
// then hands to the provider WITHOUT persisting the value: the stored
// manifest (SQLite, on-disk mirror, git) keeps the marker, never the secret.
// Use it in any field a schema marks `@secret` instead of writing a plaintext
// value that would be committed to the manifest store.
//
// Canonical form names a secret provider + a provider-specific key:
//
//	password: base.#Secret & {
//	    $secret: {provider: "vault", key: "secret/data/db#password"}
//	}
//
// The two built-in providers get terse sugar (choose exactly one field):
//
//	password: base.#Secret & {$secret: {file: "db-01.pw"}}   // <state-dir>/secrets/db-01.pw, 0600
//	password: base.#Secret & {$secret: {env: "DB01_PASSWORD"}}
#Secret: {
	"$secret": {
		// Built-in sugar: set exactly one of file / env, OR the
		// canonical provider+key pair below.
		file?: string
		env?:  string
		// Canonical form for any registered provider (built-in or
		// configured backend / plugin).
		provider?: string
		key?:      string
	}
}
