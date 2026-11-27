package apps

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxJellyfinPublicInfoBytes = 64 * 1024

// jellyfinServerID accepts only completed onboarding with a bounded, nonempty
// identity. Public metadata is untrusted and never included in diagnostics.
func jellyfinServerID(body []byte) (string, bool) {
	var info struct {
		ID        string `json:"Id"`
		Completed bool   `json:"StartupWizardCompleted"`
	}
	if json.Unmarshal(body, &info) != nil || !info.Completed || info.ID == "" || len(info.ID) > 128 || strings.TrimSpace(info.ID) != info.ID {
		return "", false
	}
	return info.ID, true
}

// probePublic sends no credentials and never follows redirects. Jellyfin must
// identify the same onboarded instance reached through verified guest SSH;
// generic Compose applications retain their status-only HTTP probe contract.
func probePublic(ctx context.Context, address, kind, expectedID string) (state, message string) {
	const failed = "Public HTTP probe failed. Check the configured endpoint, TLS trust, tunnel origin and access policy; redirects are not accepted as readiness."
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "not_ready", failed
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return "not_ready", failed
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "not_ready", failed
	}
	if kind != "Jellyfin" {
		return "ready", "Configured public HTTP endpoint responded successfully, independently of guest/application readiness."
	}
	const invalid = "Public endpoint did not return valid, onboarded Jellyfin system information. Check the System/Info/Public path, tunnel origin and access policy; an HTTP 200 login page is not application readiness."
	mediaType, _, _ := strings.Cut(response.Header.Get("Content-Type"), ";")
	if !strings.EqualFold(strings.TrimSpace(mediaType), "application/json") {
		return "not_ready", invalid
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxJellyfinPublicInfoBytes+1))
	if err != nil || len(body) > maxJellyfinPublicInfoBytes {
		return "not_ready", invalid
	}
	publicID, valid := jellyfinServerID(body)
	if !valid {
		return "not_ready", invalid
	}
	if expectedID == "" {
		return "unknown", "Public Jellyfin responded, but its instance identity cannot be verified. Restore verified guest SSH and local Jellyfin HTTP/onboarding access, then refresh health."
	}
	if publicID != expectedID {
		return "not_ready", "Public endpoint identifies a different Jellyfin instance. Check DNS and the tunnel origin against the selected guest before proceeding."
	}
	return "ready", "Public endpoint identifies the same onboarded Jellyfin instance observed through verified guest SSH. This is not a playback or access-policy test."
}
