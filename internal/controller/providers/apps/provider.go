package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
)

// Provider persists only application locators. Desired secrets remain in the
// controller resolver and travel to the guest through SSH stdin, never state.
type Provider struct {
	store        StateStore
	names        NameLister
	controllerID string
	runner       commandRunner
	locks        sync.Map
}

func New(store StateStore, names NameLister, controllerID string) *Provider {
	return &Provider{store: store, names: names, controllerID: controllerID, runner: sshRunner{}}
}
func (*Provider) Name() string    { return "apps" }
func (*Provider) Kinds() []string { return []string{"ComposeApplication", "Jellyfin"} }

// Get observes placement and readiness, not the private Compose/env contents.
// Explicit reconciliation must execute even when that observable subset matches.
func (*Provider) CanCacheApply() bool { return false }

var _ providers.Provider = (*Provider)(nil)
var _ providers.DryRunner = (*Provider)(nil)
var _ providers.Actioner = (*Provider)(nil)
var _ providers.ActionDescriber = (*Provider)(nil)
var _ providers.DriftProjector = (*Provider)(nil)
var _ providers.CreateRecoverer = (*Provider)(nil)
var _ providers.ApplyCachePolicy = (*Provider)(nil)

func (p *Provider) lock(kind, name string) func() {
	value, _ := p.locks.LoadOrStore(kind+"/"+name, &sync.Mutex{})
	m := value.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

func (p *Provider) load(ctx context.Context, kind, name string) (*locator, error) {
	if err := p.requireIdentity(); err != nil {
		return nil, err
	}
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	if p.store == nil {
		return nil, fmt.Errorf("apps: persistent provider-state store is required")
	}
	state, _, version, err := p.store.LoadState(ctx, APIVersion, kind, name)
	if err != nil {
		return nil, fmt.Errorf("apps: load locator: %w", err)
	}
	if len(state) == 0 {
		return nil, nil
	}
	if version != 1 {
		return nil, fmt.Errorf("apps: unsupported locator schema version %d", version)
	}
	var l locator
	if json.Unmarshal(state, &l) != nil {
		return nil, fmt.Errorf("apps: invalid saved locator")
	}
	return &l, nil
}

func (p *Provider) save(ctx context.Context, kind, name string, l locator) error {
	data, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("apps: encode locator")
	}
	if err := p.store.SaveState(ctx, APIVersion, kind, name, data, nil, 1); err != nil {
		return fmt.Errorf("apps: persist locator before remote mutation: %w", err)
	}
	return nil
}

// CanResumeCreate recognizes this controller's retained partial work. Apply
// independently rechecks placement, directory ownership and the project sentinel.
func (p *Provider) CanResumeCreate(ctx context.Context, kind, name string) (bool, error) {
	l, err := p.load(ctx, kind, name)
	return l != nil && err == nil, err
}

func (p *Provider) DryRun(ctx context.Context, r *protocol.Resource) (*providers.DryRunResult, error) {
	_, l, err := parse(r, false)
	if err != nil {
		return nil, err
	}
	old, err := p.load(ctx, r.Kind, r.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if old != nil {
		if err := placementChange(*old, l); err != nil {
			return nil, err
		}
	}
	if r.Kind == "ComposeApplication" {
		if err := p.checkComposePrerequisites(ctx, l); err != nil {
			return nil, err
		}
	}
	return &providers.DryRunResult{Summary: "Roll out Docker Compose in place; preserve all volumes, configuration and media. Jellyfin bootstrap changes only an explicitly dedicated guest and may require an operator reboot. HTTP, media, GPU and public readiness are checked independently."}, nil
}

func (p *Provider) Apply(ctx context.Context, r *protocol.Resource) (*protocol.Resource, error) {
	s, l, err := parse(r, true)
	if err != nil {
		return nil, err
	}
	unlock := p.lock(r.Kind, r.Metadata.Name)
	defer unlock()
	old, err := p.load(ctx, r.Kind, r.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if old != nil {
		if err := placementChange(*old, l); err != nil {
			return nil, err
		}
	}
	// Persist before touching the guest. Only a fresh locator whose very first
	// claim provably submitted no command can be released after a failure.
	if err := p.save(ctx, r.Kind, r.Metadata.Name, l); err != nil {
		return nil, err
	}
	if err := p.claimDirectory(ctx, r.Kind, r.Metadata.Name, l); err != nil {
		var before *preCommandError
		if old == nil && errors.As(err, &before) {
			if cleanupErr := p.store.DeleteState(context.WithoutCancel(ctx), APIVersion, r.Kind, r.Metadata.Name); cleanupErr != nil {
				return nil, fmt.Errorf("%w; no SSH command was submitted, but removing the fresh locator failed; original placement remains bound: %w", err, cleanupErr)
			}
			return nil, fmt.Errorf("%w; no SSH command was submitted; fresh locator removed, correct target/SSH settings and retry", err)
		}
		return nil, fmt.Errorf("%w; locator retained: an existing binding or uncertain remote execution requires inspecting the original guest before retrying", err)
	}
	if err := p.claimProject(ctx, l, owner(p.controllerID, r.Kind, r.Metadata.Name), true); err != nil {
		return nil, fmt.Errorf("%w; application directory claim already executed; locator retained, inspect the original guest and retry with the same placement", err)
	}
	if r.Kind == "Jellyfin" {
		if err := p.applyJellyfin(ctx, l, s, owner(p.controllerID, r.Kind, r.Metadata.Name)); err != nil {
			return nil, err
		}
	} else {
		if err := p.applyCompose(ctx, l, s); err != nil {
			return nil, err
		}
	}
	return p.observe(ctx, r.Kind, r.Metadata.Name, l), nil
}

func owner(controllerID, kind, name string) string {
	return controllerID + "/" + APIVersion + "/" + kind + "/" + name
}
func (p *Provider) requireIdentity() error {
	if strings.TrimSpace(p.controllerID) == "" {
		return fmt.Errorf("apps: stable persistent controller identity is required")
	}
	return nil
}
func (p *Provider) ownership(kind, name string, l locator) string {
	return "(test ! -L " + quote(l.Directory) + " && test ! -L " + quote(l.Directory+"/.openctl-owner") + " && test \"$(cat " + quote(l.Directory+"/.openctl-owner") + ")\" = " + quote(owner(p.controllerID, kind, name)) + ")"
}
func (p *Provider) claim(ctx context.Context, kind, name string, l locator) error {
	if err := p.claimDirectory(ctx, kind, name, l); err != nil {
		return err
	}
	return p.claimProject(ctx, l, owner(p.controllerID, kind, name), true)
}

func (p *Provider) claimDirectory(ctx context.Context, kind, name string, l locator) error {
	if err := p.requireIdentity(); err != nil {
		return err
	}
	command := "set -eu; umask 077; test ! -L " + quote(l.Directory) + "; mkdir -p -- " + quote(l.Directory) + "; test \"$(realpath -- " + quote(l.Directory) + ")\" = " + quote(l.Directory) + "; " +
		"if test -e " + quote(l.Directory+"/.openctl-owner") + "; then " + p.ownership(kind, name, l) + "; else test ! -e " + quote(l.Directory+"/compose.yaml") + "; (set -C; printf '%s' " + quote(owner(p.controllerID, kind, name)) + " > " + quote(l.Directory+"/.openctl-owner") + ") || " + p.ownership(kind, name, l) + "; fi"
	_, err := p.run(ctx, l, "claim application directory (existing unmanaged Compose files or another resource owner are not adopted implicitly)", command, nil)
	return err
}

func compose(l locator) string {
	return composeBase(l) + " --env-file " + quote(l.Directory+"/.env") + " -f " + quote(l.Directory+"/compose.yaml")
}

func (p *Provider) Get(ctx context.Context, kind, name string) (*protocol.Resource, error) {
	l, err := p.load(ctx, kind, name)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, providers.NotFound(kind, name)
	}
	return p.observe(ctx, kind, name, *l), nil
}

func (p *Provider) List(ctx context.Context, kind string) ([]*protocol.Resource, error) {
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	if p.names == nil {
		return nil, fmt.Errorf("apps: manifest name lister is required")
	}
	names, err := p.names.ListNames(ctx, APIVersion, kind)
	if err != nil {
		return nil, err
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	resources := make([]*protocol.Resource, 0, len(sorted))
	for _, name := range sorted {
		l, err := p.load(ctx, kind, name)
		if err != nil {
			return nil, err
		}
		if l != nil {
			resources = append(resources, p.observe(ctx, kind, name, *l))
		}
	}
	return resources, nil
}

func (p *Provider) Delete(ctx context.Context, kind, name string) error {
	unlock := p.lock(kind, name)
	defer unlock()
	l, err := p.load(ctx, kind, name)
	if err != nil || l == nil {
		return err
	}
	command := "set -eu; " + p.ownership(kind, name, *l) + "; if command -v docker >/dev/null; then " + projectGuard(*l, owner(p.controllerID, kind, name)) + "; fi; "
	if kind == "Jellyfin" {
		command += "if test -f /etc/systemd/system/jellyfin.service; then systemctl disable --now jellyfin.service; fi; "
	}
	// Use Docker's project label instead of parsing a possibly invalid Compose
	// document left by a failed update. No volume or filesystem deletion occurs.
	command += "if command -v docker >/dev/null; then docker ps -aq --filter " + quote("label=com.docker.compose.project="+l.Project) + " | xargs -r docker rm --force --; fi"
	if _, err := p.run(ctx, *l, "remove application containers without deleting any volumes/configuration/media", command, nil); err != nil {
		return err
	}
	return p.store.DeleteState(ctx, APIVersion, kind, name)
}

func (*Provider) Actions(kind string) []string {
	if checkKind(kind) != nil {
		return nil
	}
	return []string{"logs", "start", "stop", "restart"}
}

// ActionSpecs describes the runtime commands without changing their stable IDs.
func (*Provider) ActionSpecs(kind string) []providers.ActionSpec {
	if checkKind(kind) != nil {
		return nil
	}
	start := "Start the Compose application with existing local images after ownership and project checks. Does not reboot, bootstrap the host, or pull a new image; preserves volumes, configuration, and media."
	stop := "Stop the Compose containers, interrupting application availability. Preserves containers, volumes, configuration, and media; does not change boot startup policy."
	restart := "Restart the Compose containers, interrupting application availability. Preserves volumes, configuration, and media; does not pull a new image."
	if kind == "Jellyfin" {
		start = "Check onboarding, reboot-required status, NVIDIA, and read-only media prerequisites, then enable and start the Jellyfin service, including boot startup. Does not reboot, bootstrap the host, or pull a new image; preserves configuration, volumes, and media."
		stop = "Disable and stop the Jellyfin service, interrupting playback and disabling boot startup. Removes application containers through Compose down, but preserves volumes, configuration, and media."
		restart = "Check onboarding, reboot-required status, NVIDIA, and read-only media prerequisites, then restart the Jellyfin service, interrupting playback. Recreates application containers without pulling a new image; preserves boot startup policy, volumes, configuration, and media."
	}
	return []providers.ActionSpec{
		{
			Name:        "logs",
			Label:       "Download application logs",
			Description: "Download redacted application logs without changing runtime state. Logs may still contain sensitive application data.",
		},
		{
			Name:        "start",
			Label:       "Start application",
			Description: start,
		},
		{
			Name:        "stop",
			Label:       "Stop application",
			Description: stop,
		},
		{
			Name:        "restart",
			Label:       "Restart application",
			Description: restart,
		},
	}
}

func (p *Provider) DoAction(ctx context.Context, kind, name, action string) (*providers.ActionResult, error) {
	if action != "logs" && action != "start" && action != "stop" && action != "restart" {
		return nil, fmt.Errorf("apps: unsupported action %q", action)
	}
	unlock := p.lock(kind, name)
	defer unlock()
	l, err := p.load(ctx, kind, name)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, providers.NotFound(kind, name)
	}
	prefix := "set -eu; " + p.ownership(kind, name, *l) + "; " + projectGuard(*l, owner(p.controllerID, kind, name)) + "; "
	if kind == "Jellyfin" && action != "logs" && action != "stop" {
		prefix += "test -f /opt/jellyfin/.openctl-onboarded; test ! -e /var/run/reboot-required; nvidia-smi >/dev/null; (" + mediaCheck(*l) + "); "
	}
	command := compose(*l) + " " + action
	if action == "start" {
		command = compose(*l) + " up -d --pull never"
	}
	if kind == "Jellyfin" && action != "logs" {
		verb := action
		if action == "start" {
			verb = "enable --now"
		}
		if action == "stop" {
			verb = "disable --now"
		}
		command = "systemctl " + verb + " jellyfin.service"
	}
	if action == "logs" {
		command = redactedLogs(*l)
	}
	out, err := p.run(ctx, *l, "application "+action, prefix+command, nil)
	if err != nil {
		return nil, err
	}
	if action == "logs" {
		return &providers.ActionResult{DownloadContent: out, DownloadFilename: name + ".log"}, nil
	}
	return &providers.ActionResult{Message: "Application " + action + " completed; refresh resource for independent readiness checks."}, nil
}

// Drift comparison excludes write-only content rather than echoing saved desired
// secrets as observation. Readiness always comes from live containers and HTTP.
func (*Provider) DriftSpecs(kind string, desired, observed map[string]any) (map[string]any, map[string]any) {
	d, o := make(map[string]any), make(map[string]any)
	// Compare only explicitly declared locator fields; normalized defaults in
	// observation must not create perpetual synthetic drift.
	for _, key := range []string{"project", "directory", "image", "publicEndpoint"} {
		if value, ok := desired[key]; ok {
			d[key] = value
			if live, exists := observed[key]; exists {
				o[key] = live
			}
		}
	}
	if target, ok := desired["target"].(map[string]any); ok {
		dt, ot := make(map[string]any, len(target)), make(map[string]any, len(target))
		live, _ := observed["target"].(map[string]any)
		for key, value := range target {
			dt[key] = value
			if v, exists := live[key]; exists {
				ot[key] = v
			}
		}
		d["target"], o["target"] = dt, ot
	}
	// This is a runtime intent, not a saved desired observation: stopped or
	// unhealthy containers are repairable by explicitly opted-in reconciliation.
	d["managedRunning"] = true
	if value, ok := observed["managedRunning"]; ok {
		o["managedRunning"] = value
	}
	return d, o
}
