package apps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

type memoryState struct{ values map[string][]byte }

func (s *memoryState) LoadState(_ context.Context, a, k, n string) ([]byte, []byte, int, error) {
	b := s.values[a+k+n]
	if b == nil {
		return nil, nil, 0, nil
	}
	return b, nil, 1, nil
}
func (s *memoryState) SaveState(_ context.Context, a, k, n string, b, private []byte, v int) error {
	if len(private) != 0 {
		panic("private state must be empty")
	}
	s.values[a+k+n] = append([]byte(nil), b...)
	return nil
}
func (s *memoryState) DeleteState(_ context.Context, a, k, n string) error {
	delete(s.values, a+k+n)
	return nil
}

type fakeRunner func(context.Context, Target, string, []byte) (string, error)

func (f fakeRunner) Run(c context.Context, t Target, command string, b []byte) (string, error) {
	return f(c, t, command, b)
}
func testProvider() (*Provider, *memoryState) {
	s := &memoryState{values: map[string][]byte{}}
	return New(s, nil, "test-controller"), s
}
func composeResource() *protocol.Resource {
	return &protocol.Resource{APIVersion: APIVersion, Kind: "ComposeApplication", Metadata: protocol.ResourceMetadata{Name: "media"}, Spec: map[string]any{
		"target":  map[string]any{"host": "192.0.2.1", "user": "ubuntu", "privateKeyFile": "/keys/app", "sudo": true},
		"compose": "services:\n  web:\n    image: example.invalid/web@sha256:abc\n", "probe": map[string]any{"url": "http://127.0.0.1:8080/health"},
	}}
}

func TestPlacementChangesRejectedBeforeRemoteMutation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*protocol.Resource)
	}{
		{"host", func(r *protocol.Resource) { r.Spec["target"].(map[string]any)["host"] = "192.0.2.2" }},
		{"port", func(r *protocol.Resource) { r.Spec["target"].(map[string]any)["port"] = 2222 }},
		{"project", func(r *protocol.Resource) { r.Spec["project"] = "other" }},
		{"directory", func(r *protocol.Resource) { r.Spec["directory"] = "/opt/other" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := testProvider()
			r := composeResource()
			_, old, err := parse(r, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.save(context.Background(), r.Kind, r.Metadata.Name, old); err != nil {
				t.Fatal(err)
			}
			calls := 0
			p.runner = fakeRunner(func(context.Context, Target, string, []byte) (string, error) { calls++; return "", nil })
			tc.change(r)
			if _, err := p.DryRun(context.Background(), r); err == nil || !strings.Contains(err.Error(), "orphan") {
				t.Fatalf("preview must reject unsafe placement: %v", err)
			}
			if _, err := p.Apply(context.Background(), r); err == nil || !strings.Contains(err.Error(), "orphan") {
				t.Fatalf("apply must reject unsafe placement: %v", err)
			}
			if calls != 0 {
				t.Fatal("unsafe update reached SSH")
			}
		})
	}
}

func TestResolvedSecretsNeverPersistOrEnterCommandsOrErrors(t *testing.T) {
	p, state := testProvider()
	r := composeResource()
	secret := "test-super-sensitive-password"
	r.Spec["environment"] = "ADMIN_PASSWORD=" + secret + "\n"
	r.Spec["compose"] = "services:\n  web:\n    image: test\n    environment:\n      PASSWORD: " + secret + "\n"
	sawInput := false
	p.runner = fakeRunner(func(_ context.Context, _ Target, command string, input []byte) (string, error) {
		if len(state.values) == 0 {
			t.Fatal("remote mutation before locator persisted")
		}
		if strings.Contains(command, secret) {
			t.Fatal("secret in remote command arguments")
		}
		if strings.Contains(string(input), secret) {
			sawInput = true
		}
		if strings.Contains(command, " config --quiet") {
			return "", errors.New(secret)
		}
		return "", nil
	})
	_, err := p.Apply(context.Background(), r)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe operation diagnostic: %v", err)
	}
	if !sawInput {
		t.Fatal("secret never reached SSH stdin")
	}
	for _, b := range state.values {
		if strings.Contains(string(b), secret) || strings.Contains(string(b), "compose") || strings.Contains(string(b), "environment") {
			t.Fatalf("resolved content persisted: %s", b)
		}
	}
	l, err := p.load(context.Background(), r.Kind, r.Metadata.Name)
	if err != nil || l == nil {
		t.Fatal("failed apply lost resumable locator")
	}
}

func TestUnhealthyContainerAndFailedHTTPDoNotReportReady(t *testing.T) {
	for _, tc := range []struct {
		name, containers string
		httpFails        bool
	}{
		{"unhealthy", `[{"Service":"web","State":"running","Health":"unhealthy","ExitCode":0}]`, false},
		{"HTTP failed", `[{"Service":"web","State":"running","Health":"healthy","ExitCode":0}]`, true},
		{"missing", `[]`, false},
		{"exited", `{"Service":"web","State":"exited","Health":"","ExitCode":1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := testProvider()
			r := composeResource()
			_, l, err := parse(r, true)
			if err != nil {
				t.Fatal(err)
			}
			p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
				if strings.Contains(command, "ps --all") {
					return tc.containers, nil
				}
				if tc.httpFails && strings.Contains(command, "curl ") {
					return "", errors.New("unreachable")
				}
				return "", nil
			})
			observed := p.observe(context.Background(), r.Kind, r.Metadata.Name, l)
			if observed.Status["ready"] != false || observed.Status["phase"] != "degraded" {
				t.Fatalf("unhealthy app is green: %#v", observed.Status)
			}
		})
	}
}

func TestHealthyHTTPAndContainersDoNotRequirePublicExposure(t *testing.T) {
	p, _ := testProvider()
	r := composeResource()
	_, l, err := parse(r, true)
	if err != nil {
		t.Fatal(err)
	}
	p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
		if strings.Contains(command, "ps --all") {
			return `{"Service":"web","State":"running","Health":"healthy","ExitCode":0}`, nil
		}
		return "", nil
	})
	observed := p.observe(context.Background(), r.Kind, r.Metadata.Name, l)
	if observed.Status["ready"] != true {
		t.Fatalf("healthy private app not ready: %#v", observed.Status)
	}
	checks := observed.Status["checks"].(map[string]any)
	if checks["publicEndpoint"].(map[string]any)["ready"] != false {
		t.Fatal("provider fabricated public endpoint")
	}
}

func TestJellyfinBootstrapRebootIsExplicitAndResumable(t *testing.T) {
	p, state := testProvider()
	r := &protocol.Resource{APIVersion: APIVersion, Kind: "Jellyfin", Metadata: protocol.ResourceMetadata{Name: "jellyfin"}, Spec: map[string]any{
		"target": map[string]any{"host": "192.0.2.1", "user": "root", "privateKeyFile": "/keys/app"},
		"image":  "jellyfin/jellyfin@sha256:" + strings.Repeat("a", 64), "bootstrap": true,
		"nas":   map[string]any{"server": "nas", "share": "media", "credentials": "username=media\npassword=nas-password\n"},
		"admin": map[string]any{"username": "admin", "password": "admin-password"}, "viewer": map[string]any{"username": "viewer", "password": "viewer-password"},
	}}
	p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
		if strings.Contains(command, "then printf required") {
			return "required", nil
		}
		if strings.Contains(command, " up -d") {
			t.Fatal("application started before required reboot")
		}
		if strings.Contains(command, "reboot ") || strings.Contains(command, " reboot") {
			t.Fatal("provider rebooted guest")
		}
		return "", nil
	})
	_, err := p.Apply(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "explicitly reboot") {
		t.Fatalf("expected actionable reboot error: %v", err)
	}
	if len(state.values) != 1 {
		t.Fatal("reboot failure lost locator")
	}
	for _, b := range state.values {
		if strings.Contains(string(b), "password") {
			t.Fatal("Jellyfin credentials persisted")
		}
	}
}

func TestDeleteRemovesContainersButNeverVolumesOrData(t *testing.T) {
	p, _ := testProvider()
	r := composeResource()
	_, l, err := parse(r, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.save(context.Background(), r.Kind, r.Metadata.Name, l); err != nil {
		t.Fatal(err)
	}
	var commands []string
	p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
		commands = append(commands, command)
		return "", nil
	})
	if err := p.Delete(context.Background(), r.Kind, r.Metadata.Name); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 {
		t.Fatalf("expected deletion command: %v", commands)
	}
	for _, c := range commands {
		if strings.Contains(c, "--volumes") || strings.Contains(c, "rm -rf") || strings.Contains(c, "volume rm") {
			t.Fatalf("data-destructive delete: %s", c)
		}
	}
	missing, err := p.load(context.Background(), r.Kind, r.Metadata.Name)
	if err != nil || missing != nil {
		t.Fatal("delete retained locator")
	}
}

func TestPreviewAcceptsSecretMarkersWithoutResolvingThem(t *testing.T) {
	p, _ := testProvider()
	r := composeResource()
	r.Spec["compose"] = map[string]any{"$secret": map[string]any{"file": "compose"}}
	r.Spec["environment"] = map[string]any{"$secret": map[string]any{"file": "env"}}
	if _, err := p.DryRun(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestContainerFormatsAndHealth(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		ready bool
	}{
		{`[{"State":"running","Health":"healthy"}]`, true},
		{"{\"State\":\"running\"}\n{\"State\":\"running\",\"Health\":\"starting\"}", false},
		{`[]`, false}, {`not-json`, false}, {`{"State":"running","ExitCode":1}`, false},
	} {
		_, ready := parseContainers(tc.raw)
		if ready != tc.ready {
			t.Fatalf("readiness of %q = %v", tc.raw, ready)
		}
	}
}

func TestLocatorJSONExcludesWriteOnlyFields(t *testing.T) {
	r := composeResource()
	_, l, err := parse(r, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"compose", "environment", "admin", "viewer", "credentials"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("write-only field in locator: %s", key)
		}
	}
}
