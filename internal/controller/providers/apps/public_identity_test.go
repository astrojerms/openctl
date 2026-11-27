package apps

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestObservedJellyfinPublicIdentityGatesReadinessWithoutPersistence(t *testing.T) {
	const selected = `{"Id":"selected-jellyfin-instance","StartupWizardCompleted":true}`
	for _, tc := range []struct {
		name, guestInfo, publicInfo, contentType, publicState, httpState string
		guestReachable, ready                                            bool
	}{
		{"same instance", selected, selected, "application/json", "ready", "ready", true, true},
		{"wrong tunnel origin", selected, `{"Id":"other-jellyfin","StartupWizardCompleted":true}`, "application/json", "not_ready", "ready", true, false},
		{"HTTP 200 login portal", selected, "<html>PRIVATE-PUBLIC-BODY login</html>", "text/html", "not_ready", "ready", true, false},
		{"guest unavailable", "", selected, "application/json", "unknown", "unknown", false, false},
		{"local identity unavailable", `{"StartupWizardCompleted":true}`, selected, "application/json", "unknown", "not_ready", true, false},
		{"invalid public body with guest unavailable", "", "PRIVATE-PUBLIC-BODY", "application/json", "not_ready", "unknown", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write([]byte(tc.publicInfo))
			}))
			defer upstream.Close()
			p, state := testProvider()
			r := jellyfinResource()
			r.Spec["publicEndpoint"] = upstream.URL + "/System/Info/Public"
			_, l, err := parse(r, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.save(context.Background(), r.Kind, r.Metadata.Name, l); err != nil {
				t.Fatal(err)
			}
			key := APIVersion + r.Kind + r.Metadata.Name
			before := bytes.Clone(state.values[key])
			p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
				if !tc.guestReachable {
					if command != "true" {
						t.Fatal("dependent guest command ran without SSH")
					}
					return "", errors.New("PRIVATE-GUEST-ERROR")
				}
				if strings.Contains(command, "ps --all") {
					return `[{"Service":"jellyfin","State":"running","Health":"healthy"}]`, nil
				}
				if strings.Contains(command, "curl --fail") {
					return tc.guestInfo, nil
				}
				if command == "test -e /var/run/reboot-required" {
					return "", errors.New("absent")
				}
				return "", nil
			})
			observed, err := p.Get(context.Background(), r.Kind, r.Metadata.Name)
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 1 {
				t.Fatal("configured public endpoint was not independently probed once")
			}
			checks := observed.Status["checks"].(map[string]any)
			if checks["publicEndpoint"].(map[string]any)["state"] != tc.publicState || checks["http"].(map[string]any)["state"] != tc.httpState || observed.Status["ready"] != tc.ready {
				t.Fatalf("incorrect identity/readiness evidence: %#v", observed.Status)
			}
			if !bytes.Equal(before, state.values[key]) {
				t.Fatal("observation persisted ephemeral server identity or changed placement")
			}
			for _, check := range checks {
				message := check.(map[string]any)["message"].(string)
				if strings.Contains(message, "PRIVATE-") || strings.Contains(message, "selected-jellyfin-instance") || strings.Contains(message, "other-jellyfin") {
					t.Fatal("readiness diagnostics exposed private probe evidence")
				}
			}
		})
	}
}
