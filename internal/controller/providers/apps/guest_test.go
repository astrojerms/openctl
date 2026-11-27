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
	checks := observed.Status["checks"].(map[string]any)
	if checks["vm"].(map[string]any)["ready"] != true {
		t.Fatal("healthy SSH guest was conflated with missing application runtime")
	}
	if checks["application"].(map[string]any)["ready"] != false || checks["http"].(map[string]any)["ready"] != false || observed.Status["ready"] != false {
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
	if observed.Status["phase"] != "reboot-required" || observed.Status["ready"] != false {
		t.Fatalf("reboot requirement hidden: %v", observed.Status)
	}
}
