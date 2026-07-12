package provider

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"

	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// chartSpec locates a chart in an HTTP repo or an OCI registry.
type chartSpec struct {
	Repo    string // "https://…" or "oci://…"
	Name    string
	Version string
}

func (c chartSpec) isOCI() bool { return strings.HasPrefix(c.Repo, "oci://") }

// ref is the chart reference passed to LocateChart. For OCI it's the full
// oci:// path (repo, optionally joined with name); for HTTP it's the chart name
// (the repo URL is set separately on ChartPathOptions.RepoURL).
func (c chartSpec) ref() string {
	if c.isOCI() {
		if c.Name == "" {
			return c.Repo
		}
		return strings.TrimRight(c.Repo, "/") + "/" + c.Name
	}
	return c.Name
}

func (c chartSpec) httpRepoURL() string {
	if c.isOCI() {
		return ""
	}
	return c.Repo
}

// releaseOpts is the desired state of a Helm release.
type releaseOpts struct {
	releaseName     string
	namespace       string
	createNamespace bool
	values          map[string]any
	wait            bool
	timeout         time.Duration
}

// newActionConfig builds a Helm action.Configuration from kubeconfig bytes for a
// namespace. Helm's RESTClientGetter reads a kubeconfig path, so the bytes are
// written to a 0600 temp file; the returned cleanup removes it. An OCI registry
// client is wired so oci:// charts resolve.
func newActionConfig(kubeconfig []byte, namespace string) (cfg *action.Configuration, settings *cli.EnvSettings, cleanup func(), err error) {
	f, err := os.CreateTemp("", "openctl-k8s-kubeconfig-*")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("temp kubeconfig: %w", err)
	}
	path := f.Name()
	cleanup = func() { _ = os.Remove(path) }
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		cleanup()
		return nil, nil, nil, err
	}
	if _, err := f.Write(kubeconfig); err != nil {
		f.Close()
		cleanup()
		return nil, nil, nil, fmt.Errorf("write kubeconfig: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return nil, nil, nil, err
	}

	settings = cli.New()
	flags := genericclioptions.NewConfigFlags(false)
	flags.KubeConfig = &path
	flags.Namespace = &namespace

	cfg = new(action.Configuration)
	if err := cfg.Init(flags, namespace, os.Getenv("HELM_DRIVER"), func(string, ...any) {}); err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("init helm: %w", err)
	}
	rc, err := registry.NewClient()
	if err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("registry client: %w", err)
	}
	cfg.RegistryClient = rc
	return cfg, settings, cleanup, nil
}

// loadChart resolves and loads a chart from an HTTP repo or OCI registry. It
// goes through an Install action's ChartPathOptions so the OCI registry client
// wired on cfg is used.
func loadChart(cfg *action.Configuration, settings *cli.EnvSettings, cs chartSpec) (*chart.Chart, error) {
	inst := action.NewInstall(cfg) // wires ChartPathOptions.registryClient from cfg.RegistryClient
	inst.RepoURL = cs.httpRepoURL()
	inst.Version = cs.Version
	cp, err := inst.LocateChart(cs.ref(), settings)
	if err != nil {
		return nil, fmt.Errorf("locate chart %q: %w", cs.ref(), err)
	}
	ch, err := loader.Load(cp)
	if err != nil {
		return nil, fmt.Errorf("load chart: %w", err)
	}
	return ch, nil
}

// installOrUpgrade applies a chart as a release: install when it doesn't exist,
// upgrade when it does (the `helm upgrade --install` semantics). Chart loading
// is separate (loadChart) so this is unit-testable with a fake kube client.
func installOrUpgrade(cfg *action.Configuration, o releaseOpts, ch *chart.Chart) (*release.Release, error) {
	hist := action.NewHistory(cfg)
	hist.Max = 1
	_, err := hist.Run(o.releaseName)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return runInstall(cfg, o, ch)
	}
	if err != nil {
		return nil, fmt.Errorf("release history: %w", err)
	}
	return runUpgrade(cfg, o, ch)
}

func runInstall(cfg *action.Configuration, o releaseOpts, ch *chart.Chart) (*release.Release, error) {
	inst := action.NewInstall(cfg)
	inst.ReleaseName = o.releaseName
	inst.Namespace = o.namespace
	inst.CreateNamespace = o.createNamespace
	inst.Wait = o.wait
	inst.Timeout = o.timeout
	rel, err := inst.Run(ch, o.values)
	if err != nil {
		return nil, fmt.Errorf("install: %w", err)
	}
	return rel, nil
}

func runUpgrade(cfg *action.Configuration, o releaseOpts, ch *chart.Chart) (*release.Release, error) {
	upg := action.NewUpgrade(cfg)
	upg.Namespace = o.namespace
	upg.Wait = o.wait
	upg.Timeout = o.timeout
	rel, err := upg.Run(o.releaseName, ch, o.values)
	if err != nil {
		return nil, fmt.Errorf("upgrade: %w", err)
	}
	return rel, nil
}

func getRelease(cfg *action.Configuration, name string) (*release.Release, error) {
	return action.NewGet(cfg).Run(name)
}

func listReleases(cfg *action.Configuration) ([]*release.Release, error) {
	l := action.NewList(cfg)
	l.All = true
	return l.Run()
}

// uninstall removes a release. Idempotent: an already-absent release is nil.
// Helm's uninstall wraps a missing release as a formatted (non-sentinel) error,
// so we pre-check with a Get (which does return driver.ErrReleaseNotFound).
func uninstall(cfg *action.Configuration, name string) error {
	if _, err := getRelease(cfg, name); errors.Is(err, driver.ErrReleaseNotFound) {
		return nil
	}
	if _, err := action.NewUninstall(cfg).Run(name); err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	return nil
}
