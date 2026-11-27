package apps

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPublicJellyfinRequiresOnboardedMatchingIdentity(t *testing.T) {
	const identity = "selected-jellyfin-instance"
	const good = `{"Id":"selected-jellyfin-instance","StartupWizardCompleted":true}`
	for _, tc := range []struct {
		name, contentType, body, expectedID, state string
		code                                       int
	}{
		{"matching", "application/json", good, identity, "ready", 200},
		{"JSON charset", "Application/JSON; charset=utf-8", good, identity, "ready", 200},
		{"login page", "text/html", "<html>PRIVATE-PUBLIC-BODY login</html>", identity, "not_ready", 200},
		{"wrong server", "application/json", `{"Id":"PRIVATE-WRONG-SERVER","StartupWizardCompleted":true}`, identity, "not_ready", 200},
		{"unfinished onboarding", "application/json", `{"Id":"selected-jellyfin-instance","StartupWizardCompleted":false}`, identity, "not_ready", 200},
		{"missing identity", "application/json", `{"StartupWizardCompleted":true}`, identity, "not_ready", 200},
		{"missing completion", "application/json", `{"Id":"selected-jellyfin-instance"}`, identity, "not_ready", 200},
		{"wrong completion type", "application/json", `{"Id":"selected-jellyfin-instance","StartupWizardCompleted":"true"}`, identity, "not_ready", 200},
		{"invalid JSON", "application/json", "PRIVATE-PUBLIC-BODY", identity, "not_ready", 200},
		{"trailing response", "application/json", good + ` {"PRIVATE-PUBLIC-BODY":true}`, identity, "not_ready", 200},
		{"oversized response", "application/json", good + strings.Repeat(" ", maxJellyfinPublicInfoBytes), identity, "not_ready", 200},
		{"unbounded identity", "application/json", fmt.Sprintf(`{"Id":%q,"StartupWizardCompleted":true}`, strings.Repeat("x", 129)), identity, "not_ready", 200},
		{"unverified guest", "application/json", good, "", "unknown", 200},
		{"unverified guest and login page", "text/html", "PRIVATE-PUBLIC-BODY login", "", "not_ready", 200},
		{"provider failure", "application/json", good, identity, "not_ready", 503},
		{"login redirect", "application/json", good, identity, "not_ready", 302},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, redirects atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/System/Info/Public" {
					redirects.Add(1)
				}
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("public probe sent credentials or a non-read request")
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Location", "/login")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			state, message := probePublic(context.Background(), upstream.URL+"/System/Info/Public", "Jellyfin", tc.expectedID)
			if state != tc.state || requests.Load() != 1 || redirects.Load() != 0 {
				t.Fatalf("unexpected public evidence: state=%s requests=%d redirects=%d", state, requests.Load(), redirects.Load())
			}
			for _, private := range []string{"PRIVATE-PUBLIC-BODY", "PRIVATE-WRONG-SERVER", identity} {
				if strings.Contains(message, private) {
					t.Fatal("public diagnostic echoed untrusted response or instance identity")
				}
			}
		})
	}
}

func TestPublicJellyfinRejectsUntrustedTLSAndCancellation(t *testing.T) {
	var reads atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Id":"same","StartupWizardCompleted":true}`))
	}))
	defer upstream.Close()
	state, _ := probePublic(context.Background(), upstream.URL, "Jellyfin", "same")
	if state != "not_ready" || reads.Load() != 0 {
		t.Fatal("public probe bypassed certificate trust")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state, _ = probePublic(ctx, upstream.URL, "Jellyfin", "same")
	if state != "not_ready" || reads.Load() != 0 {
		t.Fatal("canceled public probe established readiness")
	}
}
