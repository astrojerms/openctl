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
// plane to evict a node's Kubernetes Node object. k3s bundles kubectl and
// reads the admin kubeconfig at /etc/rancher/k3s/k3s.yaml (root), so this
// runs via RunSudo. --ignore-not-found makes it a no-op if the node is
// already gone from the API.
func k8sDeleteNodeCommand(node string) string {
	return fmt.Sprintf("k3s kubectl delete node %s --ignore-not-found=true", node)
}

// k8sDeleteNodePasswordSecretCommand removes the k3s per-node password secret
// (<node>.node-password.k3s in kube-system). k3s rejects a re-registering node
// whose hostname already has a node-passwd entry with a different password
// ("Node password rejected, duplicate hostname"). A respec recreates the
// same-hostname node with a freshly-generated password, so the stale secret
// must be cleared or the new agent can't join.
func k8sDeleteNodePasswordSecretCommand(node string) string {
	return fmt.Sprintf("k3s kubectl delete secret %s.node-password.k3s -n kube-system --ignore-not-found=true", node)
}

// survivingCPEndpoint resolves the IP of a surviving control plane (one that
// exists in state, isn't being removed, and isn't in exclude) from the
// cluster's saved agent endpoints — the CP on which to run cluster-eviction
// commands. exclude lets a respec skip the node currently being recreated.
func (p *Provider) survivingCPEndpoint(name string, current []childRef, removed, exclude map[string]bool) (string, error) {
	cpName, err := survivingControlPlane(name, current, removed, exclude)
	if err != nil {
		return "", err
	}
	state, err := p.loadState(name)
	if err != nil || state == nil {
		return "", fmt.Errorf("no state for cluster %q", name)
	}
	ip := readAgentEndpoints(state)[cpName]
	if ip == "" {
		return "", fmt.Errorf("no IP for surviving CP %s", cpName)
	}
	return ip, nil
}

// evictK8sNode removes a node's Kubernetes Node object and its node-password
// secret via the control plane at cpIP, so a same-hostname node can
// re-register cleanly (respec) and no NotReady object lingers (scale-down).
//
// Best-effort: the VM/state are already gone by the time this runs, so an
// unreachable CP or transient API error is logged, not fatal — a lingering
// object is recoverable by hand, whereas failing the converge after the
// destroys would leave a messier partial state.
func (p *Provider) evictK8sNode(cpIP string, spec *k3sresources.ClusterSpec, node string) {
	client, err := ssh.WaitForSSH(cpIP, 22, spec.SSH.User, spec.SSH.PrivateKeyPath, time.Minute)
	if err != nil {
		log.Printf("k3s converge: evict %s: ssh to CP %s: %v", node, cpIP, err)
		return
	}
	defer func() { _ = client.Close() }()

	for _, cmd := range []string{
		k8sDeleteNodeCommand(node),
		k8sDeleteNodePasswordSecretCommand(node),
	} {
		if out, err := client.RunSudo(cmd); err != nil {
			log.Printf("k3s converge: evict %s (%s): %v (%s)", node, cmd, err, strings.TrimSpace(out))
		}
	}
}

// deleteDepartedK8sNodes evicts the departed nodes' cluster state (Node object
// + node-password secret) after a scale-down, via a surviving CP, so the
// cluster doesn't retain them as NotReady. Best-effort — see evictK8sNode.
func (p *Provider) deleteDepartedK8sNodes(name string, spec *k3sresources.ClusterSpec, departed []string, current []childRef, removed map[string]bool) {
	if len(departed) == 0 {
		return
	}
	cpIP, err := p.survivingCPEndpoint(name, current, removed, nil)
	if err != nil {
		log.Printf("k3s converge: skip k8s node cleanup for %v: %v", departed, err)
		return
	}
	for _, node := range departed {
		p.evictK8sNode(cpIP, spec, node)
	}
}
