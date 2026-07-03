package k3s

import (
	"path/filepath"

	"github.com/openctl/openctl/pkg/protocol"
)

// readAgentEndpoints pulls the node→IP map out of a Cluster's saved
// status.outputs.agent.endpoints. Returns an empty map if any layer is
// missing or the wrong type so callers can range without nil-checks.
func readAgentEndpoints(r *protocol.Resource) map[string]string {
	out := map[string]string{}
	if r == nil || r.Status == nil {
		return out
	}
	outputs, ok := r.Status["outputs"].(map[string]any)
	if !ok {
		return out
	}
	agent, ok := outputs["agent"].(map[string]any)
	if !ok {
		return out
	}
	endpoints, ok := agent["endpoints"].(map[string]any)
	if !ok {
		return out
	}
	for name, v := range endpoints {
		if ip, ok := v.(string); ok && ip != "" {
			out[name] = ip
		}
	}
	return out
}

// clusterBundleDir resolves the per-cluster CA bundle dir
// (~/.openctl/state/k3s/<cluster>/) so the converge paths can load the
// existing bundle and persist an extended one back.
func clusterBundleDir(name string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}
