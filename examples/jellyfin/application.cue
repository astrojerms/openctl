// Fill the abstract fields through a private values file on the controller:
// openctl ctl apply -f application.cue --values /secure/jellyfin-values.cue
import "openctl.io/schemas/apps"

apps.#Jellyfin & {
	metadata: name: "jellyfin"
	spec: {
		target: {
			host: "$ref": {
				apiVersion: "proxmox.openctl.io/v1"
				kind:       "VirtualMachine"
				name:       "jellyfin"
				field:      "status.ip"
			}
			user:           "ubuntu"
			privateKeyFile: string
			knownHostsFile: string
			sudo:           true
		}
		// Only a dedicated Ubuntu 24.04 amd64 guest with a passed NVIDIA GPU.
		bootstrap: true
		// Set an official jellyfin/jellyfin@sha256: digest, not a moving tag.
		image:             string
		allowRemoteAccess: false
		nas: {
			server:    string
			share:     string
			mediaPath: *"" | string
			credentials: "$secret": {provider: "file", key: string}
		}
		admin: {
			username: string
			password: "$secret": {provider: "file", key: string}
		}
		viewer: {
			username: string
			password: "$secret": {provider: "file", key: string}
		}
	}
}
