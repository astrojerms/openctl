// Homelab k3s cluster: an HA-ish control plane plus two purpose-built worker
// pools — a GPU pool for local models (Ollama/Open WebUI) and a storage pool
// prepped for Longhorn (open-iscsi installed via nodePrep).
//
// Validate this file with:  openctl validate -f examples/homelab/01-cluster.cue
//
// The cluster publishes status.outputs.kubeconfigPath, which the workload
// manifests (Platform, HelmReleases) $ref to target this cluster — that ref
// also DAG-orders them after the cluster is Ready.
import "openctl.io/schemas/k3s"

k3s.#Cluster & {
	metadata: name: "home"
	spec: {
		compute: {
			provider: "proxmox"
			// A cloud image is downloaded once into a template; nodes clone it.
			image: {
				url:         "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"
				storage:     "local"     // where the downloaded image + snippets live
				diskStorage: "local-lvm" // where node disks are allocated
			}
			default: {cpus: 2, memoryMB: 4096, diskGB: 40}
		}
		nodes: {
			controlPlane: count: 1

			workers: [
				// General-purpose workloads (blogs, MinIO, Authentik, Jellyfin).
				{name: "general", count: 2, size: {cpus: 4, memoryMB: 8192, diskGB: 60}},

				// GPU pool for a local model. Pinned to the host with the card;
				// the pool's VMs get q35 + OVMF + the GPU passed through. Pair
				// with the nvidiaDevicePlugin Platform component so workloads can
				// request nvidia.com/gpu.
				{
					name:  "gpu"
					count: 1
					nodes: ["pve-gpu"]
					size: {cpus: 8, memoryMB: 24576, diskGB: 80}
					gpu: {
						efiStorage: "local-lvm"
						devices: [
							{mapping: "rtx4090", primaryGPU: true},
						]
					}
				},

				// Storage pool for Longhorn replicated block storage. nodePrep
				// installs open-iscsi (Longhorn's node prerequisite) on first
				// boot via cloud-init.
				{
					name:  "storage"
					count: 3
					size: {cpus: 4, memoryMB: 8192, diskGB: 200}
					nodePrep: {
						packages: ["open-iscsi"]
						runcmd: ["systemctl enable --now iscsid"]
					}
				},
			]
		}
		network: staticIPs: {
			startIP: "192.168.1.100"
			gateway: "192.168.1.1"
			netmask: "24"
		}
		ssh: {
			privateKeyPath: "~/.ssh/id_ed25519"
			publicKeys: ["ssh-ed25519 AAAA... you@homelab"]
		}
	}
}
