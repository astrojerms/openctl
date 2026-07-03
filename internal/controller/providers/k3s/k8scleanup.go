package k3s

import (
	"fmt"
	"log"
	"strings"
	"time"

	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/k3s/ssh"
)

// k8sDeleteNodeCommand is the (sudo-less) command run on a surviving control
// plane to evict a departed node's Kubernetes Node object. k3s bundles
// kubectl and reads the admin kubeconfig at /etc/rancher/k3s/k3s.yaml (root),
// so this runs via RunSudo. --ignore-not-found makes it a no-op if the node
// was already gone from the API.
func k8sDeleteNodeCommand(node string) string {
	return fmt.Sprintf("k3s kubectl delete node %s --ignore-not-found=true", node)
}

// deleteDepartedK8sNodes removes the Kubernetes Node objects for nodes that
// were just torn down in a scale-down, so the cluster doesn't retain them as
// NotReady. It SSHes a surviving control plane and runs `k3s kubectl delete
// node` for each.
//
// Best-effort by design: the VMs and per-node state are already gone by this
// point, so a failure here (unreachable CP, transient SSH/API error) is
// logged, not fatal — a lingering Node object is trivially recoverable by
// hand, whereas failing the whole converge after the destroys would leave a
// far messier partial state. Homelab-validated; there's no in-cluster path
// to unit-test the SSH/kubectl round-trip (only the command shape is tested).
func (p *Provider) deleteDepartedK8sNodes(name string, spec *k3sresources.ClusterSpec, departed []string, current []childRef, removed map[string]bool) {
	if len(departed) == 0 {
		return
	}
	cpName, err := survivingControlPlane(name, current, removed, nil)
	if err != nil {
		log.Printf("k3s converge: skip k8s node cleanup for %v: %v", departed, err)
		return
	}
	state, err := p.loadState(name)
	if err != nil || state == nil {
		log.Printf("k3s converge: skip k8s node cleanup, no state for %q", name)
		return
	}
	cpIP := readAgentEndpoints(state)[cpName]
	if cpIP == "" {
		log.Printf("k3s converge: skip k8s node cleanup, no IP for surviving CP %s", cpName)
		return
	}

	client, err := ssh.WaitForSSH(cpIP, 22, spec.SSH.User, spec.SSH.PrivateKeyPath, time.Minute)
	if err != nil {
		log.Printf("k3s converge: k8s node cleanup: ssh to CP %s (%s): %v", cpName, cpIP, err)
		return
	}
	defer func() { _ = client.Close() }()

	for _, node := range departed {
		if out, err := client.RunSudo(k8sDeleteNodeCommand(node)); err != nil {
			log.Printf("k3s converge: delete k8s node %s: %v (%s)", node, err, strings.TrimSpace(out))
		}
	}
}
