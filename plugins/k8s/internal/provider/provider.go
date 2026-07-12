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
	kindManifest    = "Manifest"
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
		// State carries the kubeconfig + release/object coordinates so Get/Delete
		// can reconnect to the cluster (Get/Delete params don't include the spec).
		// Plan lets the UI render the Platform composite's fan-out.
		Capabilities: []string{pluginproto.CapabilitySchema, pluginproto.CapabilityState, pluginproto.CapabilityPlan},
		Kinds: []pluginproto.KindInfo{
			{Kind: kindHelmRelease, Schema: helmReleaseSchema},
			{Kind: kindManifest, Schema: manifestSchema},
			{Kind: kindPlatform, Schema: platformSchema},
		},
	}, nil
}

// Plan expands a Platform composite into its component HelmRelease children for
// the UI graph. Only Platform composes; other kinds are applied directly.
func (p *provider) Plan(_ context.Context, m *protocol.Resource) (*pluginproto.PlanResult, error) {
	if m == nil || m.Kind != kindPlatform {
		return nil, pluginproto.Unsupported("only Platform composes")
	}
	return p.planPlatform(m), nil
}

// kubeconfigState records how to reach the cluster for Get/Delete (which carry
// no spec), shared by both kinds' state blobs. Fields are inlined into the
// enclosing JSON (anonymous embed):
//   - KubeconfigPath (preferred): a path on the controller host — typically a
//     k3s Cluster's status.outputs.kubeconfigPath resolved via a $ref. Only the
//     path is stored; Get/Delete re-read the file. No credential bytes persist.
//   - Kubeconfig: inline content, for an explicit external kubeconfig with no
//     stable path. Stored in openctl's local provider_state (SQLite, not git).
type kubeconfigState struct {
	KubeconfigPath string `json:"kubeconfigPath,omitempty"`
	Kubeconfig     string `json:"kubeconfig,omitempty"`
}

// loadKubeconfig re-reads from the path when path-based, else the inline content.
func (k kubeconfigState) loadKubeconfig() ([]byte, error) {
	if k.KubeconfigPath != "" {
		b, err := os.ReadFile(k.KubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("read kubeconfig %q: %w", k.KubeconfigPath, err)
		}
		return b, nil
	}
	if k.Kubeconfig != "" {
		return []byte(k.Kubeconfig), nil
	}
	return nil, fmt.Errorf("no kubeconfig in state")
}

func (k kubeconfigState) present() bool { return k.KubeconfigPath != "" || k.Kubeconfig != "" }

// kubeconfigStateOf records the path when path-based (no credential bytes), else
// the inline content.
func kubeconfigStateOf(content []byte, path string) kubeconfigState {
	if path != "" {
		return kubeconfigState{KubeconfigPath: path}
	}
	return kubeconfigState{Kubeconfig: string(content)}
}

type releaseState struct {
	kubeconfigState
	Namespace   string `json:"namespace"`
	ReleaseName string `json:"releaseName"`
}

func (st releaseState) hasCluster() bool { return st.ReleaseName != "" && st.present() }

// manifestState persists the objects a Manifest applied (so openctl can prune
// removed objects and delete on teardown) plus how to reach the cluster.
type manifestState struct {
	kubeconfigState
	Objects []objectRef `json:"objects,omitempty"`
}

func (p *provider) Apply(ctx context.Context, req pluginproto.ApplyParams) (*pluginproto.ApplyResult, error) {
	m := req.Manifest
	if m == nil {
		return nil, pluginproto.Unsupported("apply: nil manifest")
	}
	switch m.Kind {
	case kindHelmRelease:
		return p.applyHelmRelease(m)
	case kindManifest:
		return p.applyManifest(ctx, m, req.State)
	case kindPlatform:
		return p.applyPlatform(ctx, m, req.State)
	default:
		return nil, pluginproto.Unsupported("k8s provider handles HelmRelease, Manifest and Platform, not " + m.Kind)
	}
}

func (p *provider) Get(ctx context.Context, req pluginproto.GetParams) (*pluginproto.GetResult, error) {
	switch req.Kind {
	case kindHelmRelease:
		return p.getHelmRelease(req)
	case kindManifest:
		return p.getManifest(ctx, req)
	case kindPlatform:
		return p.getPlatform(ctx, req)
	default:
		return nil, pluginproto.Unsupported("k8s provider handles HelmRelease, Manifest and Platform, not " + req.Kind)
	}
}

func (p *provider) Delete(ctx context.Context, req pluginproto.DeleteParams) error {
	switch req.Kind {
	case kindHelmRelease:
		return p.deleteHelmRelease(req)
	case kindManifest:
		return p.deleteManifest(ctx, req)
	case kindPlatform:
		return p.deletePlatform(ctx, req)
	default:
		return nil
	}
}

// List has no cluster context (ListParams carries only the kind), and these
// kinds are inherently per-cluster, so this returns nothing. Per-cluster
// enumeration is a later phase (see docs/deployment-model.md).
func (p *provider) List(context.Context, string) ([]*protocol.Resource, error) {
	return nil, nil
}

// --- HelmRelease ---

func (p *provider) applyHelmRelease(m *protocol.Resource) (*pluginproto.ApplyResult, error) {
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

	rs := releaseState{
		kubeconfigState: kubeconfigStateOf(spec.kubeconfig, spec.kubeconfigPath),
		Namespace:       spec.namespace,
		ReleaseName:     spec.opts.releaseName,
	}
	st, _ := json.Marshal(rs)
	return &pluginproto.ApplyResult{Resource: observed(m, rel), State: st}, nil
}

func (p *provider) getHelmRelease(req pluginproto.GetParams) (*pluginproto.GetResult, error) {
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
	return &pluginproto.GetResult{Resource: observedFromRelease(req.Name, rel), State: req.State}, nil
}

func (p *provider) deleteHelmRelease(req pluginproto.DeleteParams) error {
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

// --- Manifest ---

func (p *provider) applyManifest(ctx context.Context, m *protocol.Resource, prior json.RawMessage) (*pluginproto.ApplyResult, error) {
	content, path, err := kubeconfigFromSpec(m.Spec, m.Metadata.Name)
	if err != nil {
		return nil, err
	}
	manifestYAML := specString(m.Spec, "manifest")
	if manifestYAML == "" {
		return nil, fmt.Errorf("spec.manifest is required for Manifest %q", m.Metadata.Name)
	}
	objs, err := parseObjects(manifestYAML)
	if err != nil {
		return nil, fmt.Errorf("parse Manifest %q: %w", m.Metadata.Name, err)
	}
	kc, err := newKubeClient(content)
	if err != nil {
		return nil, err
	}

	applied := make([]objectRef, 0, len(objs))
	for _, obj := range objs {
		ref, err := kc.apply(ctx, obj)
		if err != nil {
			return nil, err
		}
		applied = append(applied, ref)
	}

	// Prune objects that were in prior state but left the manifest.
	var priorState manifestState
	if len(prior) > 0 {
		_ = json.Unmarshal(prior, &priorState)
	}
	for _, ref := range prunedRefs(priorState.Objects, applied) {
		if err := kc.delete(ctx, ref); err != nil {
			return nil, fmt.Errorf("prune %s: %w", ref, err)
		}
	}

	st, _ := json.Marshal(manifestState{kubeconfigState: kubeconfigStateOf(content, path), Objects: applied})
	return &pluginproto.ApplyResult{Resource: manifestObserved(m, applied), State: st}, nil
}

func (p *provider) getManifest(ctx context.Context, req pluginproto.GetParams) (*pluginproto.GetResult, error) {
	var st manifestState
	if len(req.State) > 0 {
		_ = json.Unmarshal(req.State, &st)
	}
	if len(st.Objects) == 0 || !st.present() {
		return nil, pluginproto.NotFound(fmt.Sprintf("Manifest %q has no prior state", req.Name))
	}
	kc, err := st.loadKubeconfig()
	if err != nil {
		return nil, err
	}
	client, err := newKubeClient(kc)
	if err != nil {
		return nil, err
	}

	present := 0
	names := make([]string, 0, len(st.Objects))
	for _, ref := range st.Objects {
		if _, err := client.get(ctx, ref); err == nil {
			present++
			names = append(names, ref.String())
		}
	}
	if present == 0 {
		return nil, pluginproto.NotFound(fmt.Sprintf("Manifest %q objects not found in cluster", req.Name))
	}
	status := map[string]any{
		"objects": names,
		"applied": present,
		"phase":   "Ready",
	}
	if present < len(st.Objects) {
		status["phase"] = "Degraded"
	}
	r := &protocol.Resource{APIVersion: apiVersion, Kind: kindManifest, Status: status}
	r.Metadata.Name = req.Name
	return &pluginproto.GetResult{Resource: r, State: req.State}, nil
}

func (p *provider) deleteManifest(ctx context.Context, req pluginproto.DeleteParams) error {
	var st manifestState
	if len(req.State) > 0 {
		_ = json.Unmarshal(req.State, &st)
	}
	if len(st.Objects) == 0 || !st.present() {
		return nil
	}
	kc, err := st.loadKubeconfig()
	if err != nil {
		return err
	}
	client, err := newKubeClient(kc)
	if err != nil {
		return err
	}
	for _, ref := range st.Objects {
		if err := client.delete(ctx, ref); err != nil {
			return fmt.Errorf("delete %s: %w", ref, err)
		}
	}
	return nil
}

// --- spec parsing ---

type helmSpec struct {
	kubeconfig     []byte // resolved content
	kubeconfigPath string // set when sourced from a path (stored in state for re-read)
	namespace      string
	chart          chartSpec
	opts           releaseOpts
}

// kubeconfigFromSpec resolves the kubeconfig bytes + the source path (empty when
// inline) from a spec's kubeconfigPath ($ref-resolved) or kubeconfig (inline)
// field. Exactly one must be present. Shared by both kinds.
func kubeconfigFromSpec(s map[string]any, name string) (content []byte, path string, err error) {
	if p := specString(s, "kubeconfigPath"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, "", fmt.Errorf("%q: read kubeconfig %q: %w", name, p, err)
		}
		return b, p, nil
	}
	if inline := specString(s, "kubeconfig"); inline != "" {
		return []byte(inline), "", nil
	}
	return nil, "", fmt.Errorf("%q: spec.kubeconfig or spec.kubeconfigPath is required", name)
}

func parseHelmSpec(m *protocol.Resource) (helmSpec, error) {
	s := m.Spec
	var hs helmSpec

	content, path, err := kubeconfigFromSpec(s, m.Metadata.Name)
	if err != nil {
		return hs, err
	}
	hs.kubeconfig = content
	hs.kubeconfigPath = path

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

// manifestObserved builds the applied resource for a Manifest: spec echoes the
// desired manifest (minus credentials), status lists the applied objects.
func manifestObserved(m *protocol.Resource, refs []objectRef) *protocol.Resource {
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.String())
	}
	spec := map[string]any{}
	for k, v := range m.Spec {
		if k == "kubeconfig" || k == "kubeconfigPath" {
			continue
		}
		spec[k] = v
	}
	r := &protocol.Resource{
		APIVersion: apiVersion,
		Kind:       kindManifest,
		Spec:       spec,
		Status:     map[string]any{"objects": names, "applied": len(refs), "phase": "Ready"},
	}
	r.Metadata.Name = m.Metadata.Name
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
