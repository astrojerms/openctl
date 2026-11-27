package apps

import "openctl.io/schemas/base"

#Target: {
    host: string | base.#Ref
    port?: int & >=1 & <=65535
    user: string
    privateKeyFile: string
    knownHostsFile?: string
    sudo?: bool
}

// Live, independently probed readiness. No endpoint is created by observation.
#ApplicationStatus: {
    url: string
    phase: string
    state: string
    ready: bool
    message: string
    checks: {
        [string]: {ready: bool, message: string}
    }
    containers?: [...{service: string, state: string, health: string, exitCode: int}]
    ...
}

#ComposeApplication: base.#Resource & {
    apiVersion: "apps.openctl.io/v1"
    kind: "ComposeApplication"
    status?: #ApplicationStatus
    spec: {
        // An explicit SSH guest. Unknown host keys are never accepted.
        target: #Target
        // Docker Compose YAML. Use a secret reference if it contains credentials.
        compose: string | base.#Secret
        // Optional dotenv file; use $secret for credentials. Never sourced as shell.
        environment?: base.#Secret
        project?: =~"^[a-z0-9][a-z0-9_-]*$"
        directory?: =~"^/[^\\r\\n]+$"
        // Probed from the guest, not assumed from a running VM/container.
        probe: {url: =~"^https?://[^@ ]+$"}
        // Optional read-only HTTP observation; never creates a public endpoint.
        publicEndpoint?: =~"^https?://[^@ ]+$"
    }
}
