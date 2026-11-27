package apps

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestRuntimeDriftDetectsStoppedContainersWithoutComparingSecrets(t *testing.T) {
	p, _ := testProvider()
	r := composeResource()
	r.Spec["environment"] = "PASSWORD=secret"
	_, l, err := parse(r, true)
	if err != nil {
		t.Fatal(err)
	}
	p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
		if strings.Contains(command, "ps --all") {
			return `[{"Service":"web","State":"exited","Health":"","ExitCode":0}]`, nil
		}
		return "", nil
	})
	observed := p.observe(context.Background(), r.Kind, r.Metadata.Name, l)
	desired, live := p.DriftSpecs(r.Kind, r.Spec, observed.Spec)
	if desired["managedRunning"] != true || live["managedRunning"] != false {
		t.Fatalf("stopped containers invisible to drift: desired=%v live=%v", desired, live)
	}
	if _, ok := desired["environment"]; ok {
		t.Fatal("write-only secret included in drift")
	}
	delete(desired, "managedRunning")
	delete(live, "managedRunning")
	if !reflect.DeepEqual(desired, live) {
		t.Fatalf("normalized defaults cause synthetic drift: desired=%v live=%v", desired, live)
	}
}

func TestImageObservationUsesActualContainerNotSavedPin(t *testing.T) {
	p, _ := testProvider()
	r := composeResource()
	_, l, err := parse(r, true)
	if err != nil {
		t.Fatal(err)
	}
	saved := "jellyfin/jellyfin@sha256:" + strings.Repeat("a", 64)
	actual := "jellyfin/jellyfin@sha256:" + strings.Repeat("b", 64)
	l.Image = saved
	p.runner = fakeRunner(func(_ context.Context, _ Target, command string, _ []byte) (string, error) {
		if strings.Contains(command, "ps --all") {
			return `[{"Service":"jellyfin","State":"running","Health":"healthy"}]`, nil
		}
		if strings.Contains(command, "{{.Config.Image}}") {
			return actual + "\n", nil
		}
		return "", nil
	})
	observed := p.observe(context.Background(), "Jellyfin", "media", l)
	if observed.Spec["image"] != actual {
		t.Fatalf("saved desired pin substituted for live image: %v", observed.Spec["image"])
	}
	d, o := p.DriftSpecs("Jellyfin", map[string]any{"image": saved}, observed.Spec)
	if reflect.DeepEqual(d, o) {
		t.Fatal("out-of-band image change was not detected")
	}
}
