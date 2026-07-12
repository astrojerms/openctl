package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"helm.sh/helm/v3/pkg/release"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

const (
	providerName    = "k8s"
	apiVersion      = "k8s.openctl.io/v1"
	kindHelmRelease = "HelmRelease"
)

// provider is the k8s pluginproto Handler. Phase 1 manages HelmRelease via the
// Helm Go SDK against an explicitly-supplied kubeconfig.
type provider struct {
	pluginproto.UnimplementedHandler
}

func New() *provider { return &provider{} }

func (p *provider) Handshake(context.Context) (*pluginproto.HandshakeResult, error) {
	return &pluginproto.HandshakeResult{
		ProviderName:    providerName,
		ProtocolVersion: pluginproto.ProtocolVersion,
		// State carries the kubeconfig + release coordinates so Get/Delete can
		// reconnect to the cluster (Get/Delete params don't include the spec).
		Capabilities: []string{pluginproto.CapabilitySchema, pluginproto.CapabilityState},
		Kinds: []pluginproto.KindInfo{
			{Kind: kindHelmRelease, Schema: helmReleaseSchema},
		},
	}, nil
}

// releaseState is the opaque blob persisted for a HelmRelease: enough to Get and
// Delete the release without the spec (Get/Delete params carry no spec). It
// records how to reach the cluster:
//   - KubeconfigPath (preferred): a path on the controller host — typically a
//     k3s Cluster's status.outputs.kubeconfigPath resolved via a $ref. Only the
//     path is stored; Get/Delete re-read the file. No credential bytes persist.
//   - Kubeconfig: inline content, for an explicit external kubeconfig with no
//     stable path. Stored in openctl's local provider_state (SQLite, not git).
type releaseState struct {
	KubeconfigPath string `json:"kubeconfigPath,omitempty"`
	Kubeconfig     string `json:"kubeconfig,omitempty"`
	Namespace      string `json:"namespace"`
	ReleaseName    string `json:"releaseName"`
}

// loadKubeconfig returns the kubeconfig bytes for a stored release: re-read from
// the path when path-based, else the inline content.
func (st releaseState) loadKubeconfig() ([]byte, error) {
	if st.KubeconfigPath != "" {
		b, err := os.ReadFile(st.KubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("read kubeconfig %q: %w", st.KubeconfigPath, err)
		}
		return b, nil
	}
	if st.Kubeconfig != "" {
		return []byte(st.Kubeconfig), nil
	}
	return nil, fmt.Errorf("no kubeconfig in state")
}

func (st releaseState) hasCluster() bool {
	return st.ReleaseName != "" && (st.KubeconfigPath != "" || st.Kubeconfig != "")
}

func (p *provider) Apply(ctx context.Context, req pluginproto.ApplyParams) (*pluginproto.ApplyResult, error) {
	m := req.Manifest
	if m == nil {
		return nil, pluginproto.Unsupported("apply: nil manifest")
	}
	if m.Kind != kindHelmRelease {
		return nil, pluginproto.Unsupported("k8s provider handles HelmRelease, not " + m.Kind)
	}

	spec, err := parseHelmSpec(m)
	if err != nil {
		return nil, err
	}
	cfg, settings, cleanup, err := newActionConfig(spec.kubeconfig, spec.namespace)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	ch, err := loadChart(cfg, settings, spec.chart)
	if err != nil {
		return nil, err
	}
	rel, err := installOrUpgrade(cfg, spec.opts, ch)
	if err != nil {
		return nil, err
	}

	rs := releaseState{Namespace: spec.namespace, ReleaseName: spec.opts.releaseName}
	if spec.kubeconfigPath != "" {
		rs.KubeconfigPath = spec.kubeconfigPath // path-based: store the path, not the bytes
	} else {
		rs.Kubeconfig = string(spec.kubeconfig)
	}
	st, _ := json.Marshal(rs)
	return &pluginproto.ApplyResult{Resource: observed(m, rel), State: st}, nil
}

func (p *provider) Get(_ context.Context, req pluginproto.GetParams) (*pluginproto.GetResult, error) {
	if req.Kind != kindHelmRelease {
		return nil, pluginproto.Unsupported("k8s provider handles HelmRelease, not " + req.Kind)
	}
	var st releaseState
	if len(req.State) > 0 {
		_ = json.Unmarshal(req.State, &st)
	}
	if !st.hasCluster() {
		return nil, pluginproto.NotFound(fmt.Sprintf("HelmRelease %q has no prior state", req.Name))
	}
	kc, err := st.loadKubeconfig()
	if err != nil {
		return nil, err
	}
	cfg, _, cleanup, err := newActionConfig(kc, st.Namespace)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	rel, err := getRelease(cfg, st.ReleaseName)
	if err != nil {
		return nil, pluginproto.NotFound(fmt.Sprintf("HelmRelease %q not found: %v", req.Name, err))
	}
	r := observedFromRelease(req.Name, rel)
	return &pluginproto.GetResult{Resource: r, State: req.State}, nil
}

// List has no cluster context (ListParams carries only the kind), and
// HelmReleases are inherently per-cluster, so Phase 1 returns nothing.
// Per-cluster enumeration is a later phase (see docs/deployment-model.md).
func (p *provider) List(context.Context, string) ([]*protocol.Resource, error) {
	return nil, nil
}

func (p *provider) Delete(_ context.Context, req pluginproto.DeleteParams) error {
	if req.Kind != kindHelmRelease {
		return nil
	}
	var st releaseState
	if len(req.State) > 0 {
		_ = json.Unmarshal(req.State, &st)
	}
	if !st.hasCluster() {
		return nil // never applied / no state
	}
	kc, err := st.loadKubeconfig()
	if err != nil {
		return err
	}
	cfg, _, cleanup, err := newActionConfig(kc, st.Namespace)
	if err != nil {
		return err
	}
	defer cleanup()
	return uninstall(cfg, st.ReleaseName)
}

// --- spec parsing ---

type helmSpec struct {
	kubeconfig     []byte // resolved content
	kubeconfigPath string // set when sourced from a path (stored in state for re-read)
	namespace      string
	chart          chartSpec
	opts           releaseOpts
}

func parseHelmSpec(m *protocol.Resource) (helmSpec, error) {
	s := m.Spec
	var hs helmSpec

	// Kubeconfig comes from either a path (typically a Cluster's
	// status.outputs.kubeconfigPath resolved via $ref — read the file) or inline
	// content (an explicit external kubeconfig, usually a $secret).
	if path := specString(s, "kubeconfigPath"); path != "" {
		content, err := os.ReadFile(path)
		if err != nil {
			return hs, fmt.Errorf("HelmRelease %q: read kubeconfig %q: %w", m.Metadata.Name, path, err)
		}
		hs.kubeconfig = content
		hs.kubeconfigPath = path
	} else if inline := specString(s, "kubeconfig"); inline != "" {
		hs.kubeconfig = []byte(inline)
	} else {
		return hs, fmt.Errorf("HelmRelease %q: spec.kubeconfig or spec.kubeconfigPath is required", m.Metadata.Name)
	}

	hs.namespace = specString(s, "namespace")
	if hs.namespace == "" {
		hs.namespace = "default"
	}

	chartRaw, _ := s["chart"].(map[string]any)
	if chartRaw == nil {
		return hs, fmt.Errorf("HelmRelease %q: spec.chart is required", m.Metadata.Name)
	}
	hs.chart = chartSpec{
		Repo:    specString(chartRaw, "repo"),
		Name:    specString(chartRaw, "name"),
		Version: specString(chartRaw, "version"),
	}
	if hs.chart.Repo == "" {
		return hs, fmt.Errorf("HelmRelease %q: spec.chart.repo is required", m.Metadata.Name)
	}

	releaseName := specString(s, "releaseName")
	if releaseName == "" {
		releaseName = m.Metadata.Name
	}
	hs.opts = releaseOpts{
		releaseName:     releaseName,
		namespace:       hs.namespace,
		createNamespace: specBool(s, "createNamespace"),
		wait:            specBool(s, "wait"),
		timeout:         5 * time.Minute,
	}
	if v, ok := s["values"].(map[string]any); ok {
		hs.opts.values = v
	}
	if t := specString(s, "timeout"); t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			hs.opts.timeout = d
		}
	}
	return hs, nil
}

// --- observed resource ---

// observed builds the applied resource: spec echoes the desired manifest (minus
// the kubeconfig secret), status carries the live release info.
func observed(m *protocol.Resource, rel *release.Release) *protocol.Resource {
	spec := map[string]any{}
	for k, v := range m.Spec {
		if k == "kubeconfig" || k == "kubeconfigPath" {
			continue // never surface the credential / resolved path
		}
		spec[k] = v
	}
	r := &protocol.Resource{APIVersion: apiVersion, Kind: kindHelmRelease, Spec: spec, Status: releaseStatus(rel)}
	r.Metadata.Name = m.Metadata.Name
	return r
}

// observedFromRelease builds a resource from just the live release (Get path,
// where the desired spec isn't available).
func observedFromRelease(name string, rel *release.Release) *protocol.Resource {
	r := &protocol.Resource{APIVersion: apiVersion, Kind: kindHelmRelease, Status: releaseStatus(rel)}
	r.Metadata.Name = name
	return r
}

func releaseStatus(rel *release.Release) map[string]any {
	status := map[string]any{
		"releaseName": rel.Name,
		"namespace":   rel.Namespace,
		"revision":    rel.Version,
		"status":      rel.Info.Status.String(),
		"phase":       phaseFor(rel),
	}
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		status["chart"] = rel.Chart.Metadata.Name + "-" + rel.Chart.Metadata.Version
		status["appVersion"] = rel.Chart.Metadata.AppVersion
	}
	return status
}

// phaseFor maps a Helm release status onto openctl's coarse phase vocabulary.
func phaseFor(rel *release.Release) string {
	switch rel.Info.Status {
	case release.StatusDeployed:
		return "Ready"
	case release.StatusFailed, release.StatusSuperseded:
		return "Failed"
	case release.StatusUninstalled, release.StatusUninstalling:
		return "Deleting"
	default:
		return "Pending"
	}
}

func specString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func specBool(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}

var _ pluginproto.Handler = (*provider)(nil)
