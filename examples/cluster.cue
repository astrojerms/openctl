import "openctl.io/schemas/k3s"

k3s.#Cluster & {
	metadata: name: "dev"
	spec: {
		compute: {
			provider: "proxmox"
			image: url: "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"
			default: {cpus: 2, memoryMB: 4096, diskGB: 32}
		}
		nodes: {
			controlPlane: count: 1
			workers: [{name: "worker", count: 2}]
		}
		network: staticIPs: {
			startIP: "192.168.1.100"
			gateway: "192.168.1.1"
			netmask: "24"
		}
		ssh: {
			privateKeyPath: "~/.ssh/id_ed25519"
			publicKeys: ["ssh-ed25519 AAAA... user@host"]
		}
	}
}
