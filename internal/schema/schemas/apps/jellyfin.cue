package apps

import "openctl.io/schemas/base"

#Jellyfin: base.#Resource & {
    apiVersion: "apps.openctl.io/v1"
    kind: "Jellyfin"
    status?: #ApplicationStatus
    spec: {
        target: #Target
        // Official image digest is explicit; retries never resolve latest.
        image: =~"^(docker\\.io/)?jellyfin/jellyfin@sha256:[a-f0-9]{64}$"
        // Opt in only on a dedicated Ubuntu 24.04 amd64 guest with passed GPU.
        // Installs Docker/NVIDIA/CIFS using bundled assets. Never reboots itself.
        bootstrap?: bool
        // Requires explicit public-access consent; never opens router ports.
        allowRemoteAccess?: bool
        nas: {
            server: =~"^[a-zA-Z0-9][a-zA-Z0-9._-]*$"
            share: =~"^[a-zA-Z0-9][a-zA-Z0-9._-]*$"
            // Empty means share root. Missing subdirectories are never created.
            mediaPath?: string & !~"^/" & !~"(^|/)\\.\\.?(/|$)" & !~"//" & !~"/$" & !~"[\\r\\n\\\\]"
            // File-format SMB username/password/domain credentials, guest-only.
            credentials: base.#Secret
        }
        admin: {
            username: string & !=""
            password: base.#Secret
        }
        viewer: {
            username: string & !=""
            password: base.#Secret
        }
        publicEndpoint?: =~"^https?://[^@ ]+$"
    }
}
