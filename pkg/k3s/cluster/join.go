package cluster

import (
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/openctl/openctl/pkg/k3s/agent/bootstrap"
	"github.com/openctl/openctl/pkg/k3s/agent/certs"
	"github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/k3s/ssh"
	"github.com/openctl/openctl/pkg/protocol"
)

// Joiner adds nodes to an already-running cluster. It mirrors Creator but
// reuses an existing CA bundle and reads the join token from a live first
// control-plane node instead of bootstrapping a fresh cluster.
//
// The caller is responsible for having already created the VMs for newNodes
// (via the in-process VM provider) before calling JoinNodes — Joiner only
// handles the post-VM k3s install + openctl-k3s-agent install on each new
// node.
type Joiner struct {
	name            string
	spec            *resources.ClusterSpec
	config          *protocol.ProviderConfig
	bundle          *certs.Bundle
	bundleDir       string
	existingNodeIPs map[string]string // already-known node name -> IP
	firstCPName     string
	firstCPIP       string
	joinToken       string
	allNewNodeIPs   map[string]string // new node name -> IP
	newCPNodes      []string
	newWorkerNodes  []string
}

// NewJoiner constructs a Joiner. firstCPName/firstCPIP identify the
// existing CP to SSH into for the join token. bundle is the live cluster's
// CA bundle (must already include CA cert+key); the caller is responsible
// for having minted server certs for each new node into it before calling
// JoinNodes.
func NewJoiner(
	name string,
	spec *resources.ClusterSpec,
	config *protocol.ProviderConfig,
	bundle *certs.Bundle,
	bundleDir string,
	existingNodeIPs map[string]string,
	firstCPName, firstCPIP string,
	newCPNodes, newWorkerNodes []string,
	newNodeIPs map[string]string,
) *Joiner {
	return &Joiner{
		name:            name,
		spec:            spec,
		config:          config,
		bundle:          bundle,
		bundleDir:       bundleDir,
		existingNodeIPs: existingNodeIPs,
		firstCPName:     firstCPName,
		firstCPIP:       firstCPIP,
		newCPNodes:      newCPNodes,
		newWorkerNodes:  newWorkerNodes,
		allNewNodeIPs:   newNodeIPs,
	}
}

// FetchJoinToken SSHes into the first existing CP and reads the cluster's
// join token. Called before JoinNodes; cached on the Joiner so the value
// can be reused across multiple node-install loops without re-SSHing.
func (j *Joiner) FetchJoinToken() error {
	if j.joinToken != "" {
		return nil
	}
	if j.firstCPIP == "" {
		return fmt.Errorf("first CP IP not set (cluster state may be missing agent.endpoints)")
	}
	client, err := ssh.WaitForSSH(j.firstCPIP, 22, j.spec.SSH.User, j.spec.SSH.PrivateKeyPath, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("ssh to existing CP %s (%s): %w", j.firstCPName, j.firstCPIP, err)
	}
	defer client.Close()
	out, err := client.RunSudo("cat /var/lib/rancher/k3s/server/node-token")
	if err != nil {
		return fmt.Errorf("read join token from %s: %w", j.firstCPName, err)
	}
	j.joinToken = strings.TrimSpace(out)
	if j.joinToken == "" {
		return fmt.Errorf("empty join token from %s", j.firstCPName)
	}
	return nil
}

// JoinNodes installs k3s on each new node (server-join for new CPs,
// agent install for new workers) and lays down the openctl-k3s-agent
// using the existing bundle's CA + the pre-minted server cert for each
// node. Caller must have already minted certs into j.bundle for every
// node in newCPNodes/newWorkerNodes before calling.
//
// On success, the bundle on disk is updated (re-WriteTo'd) so subsequent
// applies see the extended trust chain.
func (j *Joiner) JoinNodes() error {
	if err := j.FetchJoinToken(); err != nil {
		return err
	}
	// Use a Creator-shaped commander to reuse the install command builders
	// without duplicating their logic. The creator's other fields aren't
	// touched for the count-up path.
	cmder := &Creator{name: j.name, spec: j.spec, config: j.config}
	installer := &bootstrap.Installer{}

	for _, cp := range j.newCPNodes {
		ip := j.allNewNodeIPs[cp]
		if ip == "" {
			return fmt.Errorf("missing IP for new CP %s", cp)
		}
		fmt.Fprintf(os.Stderr, "Joining new K3s server on %s (%s)...\n", cp, ip)
		client, err := ssh.WaitForSSH(ip, 22, j.spec.SSH.User, j.spec.SSH.PrivateKeyPath, 5*time.Minute)
		if err != nil {
			return fmt.Errorf("ssh to %s: %w", cp, err)
		}
		joinCmd := cmder.buildServerJoinCommand(j.firstCPIP, j.joinToken)
		if _, err := client.RunSudo(joinCmd); err != nil {
			client.Close()
			return fmt.Errorf("install K3s server on %s: %w", cp, err)
		}
		if err := installAgentOn(client, installer, j.bundle, cp); err != nil {
			client.Close()
			return err
		}
		client.Close()
	}

	for _, w := range j.newWorkerNodes {
		ip := j.allNewNodeIPs[w]
		if ip == "" {
			return fmt.Errorf("missing IP for new worker %s", w)
		}
		fmt.Fprintf(os.Stderr, "Joining new K3s agent on %s (%s)...\n", w, ip)
		client, err := ssh.WaitForSSH(ip, 22, j.spec.SSH.User, j.spec.SSH.PrivateKeyPath, 5*time.Minute)
		if err != nil {
			return fmt.Errorf("ssh to %s: %w", w, err)
		}
		agentCmd := cmder.buildAgentInstallCommand(j.firstCPIP, j.joinToken)
		if _, err := client.RunSudo(agentCmd); err != nil {
			client.Close()
			return fmt.Errorf("install K3s agent on %s: %w", w, err)
		}
		if err := installAgentOn(client, installer, j.bundle, w); err != nil {
			client.Close()
			return err
		}
		client.Close()
	}

	// Persist the extended bundle so subsequent count-ups (and any
	// out-of-band tooling that reads the bundle directly) see the new
	// server certs.
	if err := j.bundle.WriteTo(j.bundleDir); err != nil {
		return fmt.Errorf("persist extended bundle: %w", err)
	}

	// Verify only the new nodes' agents — existing nodes are presumed up.
	if err := verifyAgentsReachable(j.bundle, j.allNewNodeIPs, bootstrap.Port); err != nil {
		return fmt.Errorf("new-node agent reachability: %w", err)
	}
	return nil
}

// JoinResult mirrors a slice of InstallResult — only the fields a Joiner
// can produce. KubeconfigPath/ServerIP come from the original Creator and
// are preserved in the cluster's state file; the Joiner just confirms.
type JoinResult struct {
	ServerIP       string
	AgentBundleDir string
	AgentEndpoints map[string]string
	AgentPort      int
}

// Result returns a JoinResult summarizing the join. Endpoints includes the
// union of existing + newly joined nodes so the caller can rewrite the
// cluster state's agent.endpoints field in one shot.
func (j *Joiner) Result() *JoinResult {
	all := copyMap(j.existingNodeIPs)
	maps.Copy(all, j.allNewNodeIPs)
	return &JoinResult{
		ServerIP:       j.firstCPIP,
		AgentBundleDir: j.bundleDir,
		AgentEndpoints: all,
		AgentPort:      bootstrap.Port,
	}
}
