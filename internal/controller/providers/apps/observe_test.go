package apps

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const privateReadinessEvidence = "private-runner-credential"

func assertReadinessChecks(t *testing.T, status map[string]any, expected map[string]string) {
	t.Helper()
	checks, ok := status["checks"].(map[string]any)
	if !ok || len(checks) != len(expected) {
		t.Fatalf("unexpected readiness checks: %#v", status["checks"])
	}
	for name, state := range expected {
		check, ok := checks[name].(map[string]any)
		if !ok || check["state"] != state {
			t.Fatalf("check %s: want state %s, got %#v", name, state, checks[name])
		}
		if _, obsolete := check["ready"]; obsolete {
			t.Fatalf("check %s retains obsolete readiness boolean", name)
		}
		message, ok := check["message"].(string)
		if !ok || message == "" || len(check) != 2 {
			t.Fatalf("check %s violates the state/message contract: %#v", name, check)
		}
		if strings.Contains(message, privateReadinessEvidence) {
			t.Fatalf("check %s leaked private runner evidence", name)
		}
	}
	if message, ok := status["message"].(string); ok && strings.Contains(message, privateReadinessEvidence) {
		t.Fatal("aggregate readiness message leaked private runner evidence")
	}
}

func TestUnavailableGuestSkipsDependentProbesWithUnknownEvidence(t *testing.T) {
	for _, kind := range []string{"ComposeApplication", "Jellyfin"} {
		t.Run(kind, func(t *testing.T) {
			p, _ := testProvider()
			r := composeResource()
			if kind == "Jellyfin" {
				r = jellyfinResource()
			}
			_, l, err := parse(r, true)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
				calls++
				if command != "true" {
					t.Fatalf("dependent probe ran without verified SSH: %s", command)
				}
				return privateReadinessEvidence + " output", errors.New(privateReadinessEvidence + " error")
			})
			observed := p.observe(context.Background(), r.Kind, r.Metadata.Name, l)
			expected := map[string]string{
				"vm": "not_ready", "application": "unknown", "http": "unknown", "publicEndpoint": "not_configured",
			}
			if kind == "Jellyfin" {
				expected["media"], expected["gpu"], expected["onboarding"] = "unknown", "unknown", "unknown"
			}
			assertReadinessChecks(t, observed.Status, expected)
			if calls != 1 || observed.Status["ready"] != false || observed.Status["phase"] != "degraded" || observed.Status["state"] != "degraded" || observed.Spec["managedRunning"] != false {
				t.Fatalf("unobserved application reported healthy or ran extra probes: calls=%d, resource=%#v", calls, observed)
			}
		})
	}
}

func TestAttemptedJellyfinDependencyFailuresRemainNotReady(t *testing.T) {
	for _, tc := range []struct {
		check, command string
	}{
		{"application", "ps --all"},
		{"http", "curl "},
		{"media", "findmnt "},
		{"gpu", "nvidia-smi"},
		{"onboarding", "test -f /opt/jellyfin/.openctl-onboarded"},
	} {
		t.Run(tc.check, func(t *testing.T) {
			p, _ := testProvider()
			r := jellyfinResource()
			_, l, err := parse(r, true)
			if err != nil {
				t.Fatal(err)
			}
			attempted := false
			p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
				if strings.Contains(command, tc.command) {
					attempted = true
					return privateReadinessEvidence + " output", errors.New(privateReadinessEvidence + " error")
				}
				if command == "test -e /var/run/reboot-required" {
					return "", errors.New("no reboot required")
				}
				if strings.Contains(command, "ps --all") {
					return `[{"Service":"jellyfin","State":"running","Health":"healthy","ExitCode":0}]`, nil
				}
				return "", nil
			})
			observed := p.observe(context.Background(), r.Kind, r.Metadata.Name, l)
			expected := map[string]string{
				"vm": "ready", "application": "ready", "http": "ready", "media": "ready", "gpu": "ready", "onboarding": "ready", "publicEndpoint": "not_configured",
			}
			expected[tc.check] = "not_ready"
			assertReadinessChecks(t, observed.Status, expected)
			if !attempted || observed.Status["ready"] != false || observed.Status["phase"] != "degraded" {
				t.Fatalf("failed dependency did not gate readiness: attempted=%v, status=%#v", attempted, observed.Status)
			}
			if observed.Spec["managedRunning"] != (tc.check != "application") {
				t.Fatalf("dependency failure changed container running evidence: %#v", observed.Spec)
			}
		})
	}
}

func TestContainerObservationDistinguishesUnknownFromNoContainers(t *testing.T) {
	for _, tc := range []struct {
		name, output, state string
	}{
		{"empty output", "", "unknown"},
		{"malformed JSON", privateReadinessEvidence + " not JSON", "unknown"},
		{"wrong JSON type", `"private-runner-credential"`, "unknown"},
		{"null", `null`, "unknown"},
		{"empty container object", `{}`, "unknown"},
		{"missing container state", `[{"Service":"web"}]`, "unknown"},
		{"empty container state", `[{"Service":"web","State":""}]`, "unknown"},
		{"missing container service", `[{"State":"running"}]`, "unknown"},
		{"empty container service", `[{"Service":"","State":"running"}]`, "unknown"},
		{"null container", `[null]`, "unknown"},
		{"wrong container shape", `[[]]`, "unknown"},
		{"invalid state type", `[{"Service":"web","State":true}]`, "unknown"},
		{"partial JSON stream", "{\"Service\":\"web\",\"State\":\"running\"}\n" + privateReadinessEvidence, "unknown"},
		{"no containers", `[]`, "not_ready"},
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
					return tc.output, nil
				}
				return "", nil
			})
			observed := p.observe(context.Background(), r.Kind, r.Metadata.Name, l)
			assertReadinessChecks(t, observed.Status, map[string]string{
				"vm": "ready", "application": tc.state, "http": "ready", "publicEndpoint": "not_configured",
			})
			if observed.Status["ready"] != false || observed.Spec["managedRunning"] != false {
				t.Fatalf("absent or invalid containers reported ready: %#v", observed)
			}
			containers, ready := parseContainers(tc.output)
			if ready || (containers == nil) != (tc.state == "unknown") {
				t.Fatalf("parser lost invalid versus empty distinction: containers=%#v ready=%v", containers, ready)
			}
		})
	}
}

func TestConfiguredPublicEndpointIsIndependentAndRequired(t *testing.T) {
	for _, reachable := range []bool{false, true} {
		for _, code := range []int{http.StatusOK, http.StatusServiceUnavailable} {
			name := "guest-unavailable"
			if reachable {
				name = "guest-reachable"
			}
			t.Run(name+"/"+http.StatusText(code), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					w.WriteHeader(code)
				}))
				defer server.Close()
				p, _ := testProvider()
				r := composeResource()
				r.Spec["publicEndpoint"] = server.URL
				_, l, err := parse(r, true)
				if err != nil {
					t.Fatal(err)
				}
				p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
					if !reachable {
						if command != "true" {
							t.Fatalf("guest probe ran without verified SSH: %s", command)
						}
						return privateReadinessEvidence + " output", errors.New(privateReadinessEvidence + " error")
					}
					if strings.Contains(command, "ps --all") {
						return `[{"Service":"web","State":"running"}]`, nil
					}
					return "", nil
				})
				observed := p.observe(context.Background(), r.Kind, r.Metadata.Name, l)
				expected := map[string]string{
					"vm": "not_ready", "application": "unknown", "http": "unknown", "publicEndpoint": "not_ready",
				}
				if reachable {
					expected["vm"], expected["application"], expected["http"] = "ready", "ready", "ready"
				}
				if code == http.StatusOK {
					expected["publicEndpoint"] = "ready"
				}
				assertReadinessChecks(t, observed.Status, expected)
				if requests.Load() != 1 || observed.Status["ready"] != (reachable && code == http.StatusOK) {
					t.Fatalf("public endpoint did not independently gate readiness: requests=%d status=%#v", requests.Load(), observed.Status)
				}
			})
		}
	}
}

func TestHealthyJellyfinReadinessStillRequiresClearedReboot(t *testing.T) {
	for _, rebootRequired := range []bool{false, true} {
		name := "no-reboot-required"
		if rebootRequired {
			name = "reboot-required"
		}
		t.Run(name, func(t *testing.T) {
			p, _ := testProvider()
			r := jellyfinResource()
			_, l, err := parse(r, true)
			if err != nil {
				t.Fatal(err)
			}
			p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
				if command == "test -e /var/run/reboot-required" && !rebootRequired {
					return "", errors.New("no reboot required")
				}
				if strings.Contains(command, "ps --all") {
					return `[{"Service":"jellyfin","State":"running","Health":"healthy"}]`, nil
				}
				return "", nil
			})
			observed := p.observe(context.Background(), r.Kind, r.Metadata.Name, l)
			assertReadinessChecks(t, observed.Status, map[string]string{
				"vm": "ready", "application": "ready", "http": "ready", "media": "ready", "gpu": "ready", "onboarding": "ready", "publicEndpoint": "not_configured",
			})
			phase := "ready"
			if rebootRequired {
				phase = "reboot-required"
			}
			if observed.Status["ready"] != !rebootRequired || observed.Status["phase"] != phase || observed.Status["state"] != phase || observed.Spec["managedRunning"] != true {
				t.Fatalf("healthy checks lost reboot or private readiness semantics: %#v", observed)
			}
		})
	}
}
