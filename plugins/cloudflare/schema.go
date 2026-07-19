package main

// dnsRecordSchema is the CUE schema advertised for the DNSRecord kind. It is
// compiled standalone by the controller (no openctl module imports) and stays
// open with a trailing `...` so controller-managed fields (labels, status)
// aren't rejected. apiVersion must match `<providerName>.openctl.io/v1`.
const dnsRecordSchema = `
// #ref is the {$ref: {...}} marker shape (openctl's ResourceRef). Inlined here
// because this schema compiles standalone (no base import). Allowed on content
// so a record can point at another resource's status — e.g. a Tunnel's
// status.cnameTarget — instead of a hand-copied literal.
#ref: {"$ref": {apiVersion: string, kind: string, name: string, field?: string}}
#DNSRecord: {
	apiVersion: "cloudflare.openctl.io/v1"
	kind:       "DNSRecord"
	metadata: {
		name: string
		...
	}
	spec: {
		// zoneId is the Cloudflare zone the record lives in. Optional when the
		// provider config sets defaults.zoneId.
		zoneId?: string
		// type is the DNS record type: A, AAAA, CNAME, TXT, MX, NS, SRV, ...
		type: string
		// name is the record name (FQDN, e.g. "www.example.com", or "@").
		name: string
		// content is the record value (an IP for A/AAAA, target for CNAME, ...),
		// or a $ref to another resource's status field (e.g. a Tunnel's
		// status.cnameTarget).
		content: string | #ref
		// ttl in seconds; 1 means "automatic". Defaults to Cloudflare's default.
		ttl?: int
		// proxied routes the record through Cloudflare's proxy (orange cloud).
		proxied?: bool
		// priority is used by MX/SRV records.
		priority?: int
		comment?: string
	}
	...
}
`

// tunnelSchema is the CUE schema for the Tunnel kind. The Cloudflare tunnel is
// named after metadata.name. Ingress rules are optional; a catch-all is added
// automatically when omitted from the final rule.
const tunnelSchema = `
#Tunnel: {
	apiVersion: "cloudflare.openctl.io/v1"
	kind:       "Tunnel"
	metadata: {
		name: string
		...
	}
	spec: {
		// accountId is the Cloudflare account. Optional when the provider config
		// sets defaults.accountId.
		accountId?: string
		// ingress maps public hostnames to local services (cloudflared config).
		// A final catch-all rule is appended automatically if the last rule is
		// host-scoped.
		ingress?: [...{
			// hostname is the public name routed to this service; omit on the
			// final catch-all rule.
			hostname?: string
			// service is the local origin, e.g. "http://localhost:8080" or
			// "http_status:404".
			service: string
			path?: string
		}]
	}
	// status documents the observed fields a Tunnel exposes — the values other
	// resources $ref. Open + all-optional, so it describes without constraining
	// the provider's output.
	status?: {
		// Cloudflare tunnel id.
		id?: string
		// Ready-to-use CNAME target (<id>.cfargotunnel.com) — $ref this from a
		// DNSRecord's content to route a hostname through the tunnel.
		cnameTarget?: string
		// Tunnel name (mirrors metadata.name).
		name?: string
		// Connection status reported by Cloudflare (e.g. "healthy", "down").
		connectionStatus?: string
		// Creation timestamp.
		createdAt?: string
		...
	}
	...
}
`
