package apps

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGuestReadinessDoesNotImplyDockerOrHTTPReadiness(t *testing.T) {
	p, _ := testProvider()
	r := composeResource()
	_, l, err := parse(r, true)
	if err != nil {
		t.Fatal(err)
	}
	p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
		if command == "true" {
			return "", nil
		}
		return "", errors.New("Docker missing")
	})
	observed := p.observe(context.Background(), r.Kind, r.Metadata.Name, l)
	assertReadinessChecks(t, observed.Status, map[string]string{
		"vm": "ready", "application": "not_ready", "http": "not_ready", "publicEndpoint": "not_configured",
	})
	if observed.Status["ready"] != false {
		t.Fatal("VM running incorrectly reported an application ready")
	}
}

func TestJellyfinReportsRebootRequirementIndependentlyOfApplication(t *testing.T) {
	p, _ := testProvider()
	r := composeResource()
	_, l, err := parse(r, true)
	if err != nil {
		t.Fatal(err)
	}
	p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
		if command == "true" || command == "test -e /var/run/reboot-required" {
			return "", nil
		}
		if strings.Contains(command, "ps --all") {
			return "[]", nil
		}
		return "", errors.New("driver not loaded")
	})
	observed := p.observe(context.Background(), "Jellyfin", "media", l)
	assertReadinessChecks(t, observed.Status, map[string]string{
		"vm": "ready", "application": "not_ready", "http": "not_ready", "media": "not_ready", "gpu": "not_ready", "onboarding": "not_ready", "publicEndpoint": "not_configured",
	})
	if observed.Status["phase"] != "reboot-required" || observed.Status["ready"] != false {
		t.Fatalf("reboot requirement hidden: %v", observed.Status)
	}
}
