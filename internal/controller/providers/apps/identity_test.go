package apps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingControllerIdentityRejectsMutationBeforeStateOrSSH(t *testing.T) {
	for _, id := range []string{"", " \t"} {
		t.Run(id, func(t *testing.T) {
			p, state := testProvider()
			p.controllerID = id
			calls := 0
			p.runner = fakeRunner(func(context.Context, Target, string, []byte) (string, error) { calls++; return "", nil })
			r := composeResource()
			if _, err := p.Apply(context.Background(), r); err == nil || !strings.Contains(err.Error(), "controller identity") {
				t.Fatalf("missing identity accepted: %v", err)
			}
			if _, err := p.DryRun(context.Background(), r); err == nil {
				t.Fatal("preview accepted missing controller identity")
			}
			if err := p.claimProject(context.Background(), locator{}, "", true); err == nil {
				t.Fatal("project claim accepted missing controller identity")
			}
			if err := p.claim(context.Background(), r.Kind, r.Metadata.Name, locator{}); err == nil {
				t.Fatal("directory claim accepted missing controller identity")
			}
			if calls != 0 || len(state.values) != 0 {
				t.Fatal("missing identity mutated guest or persisted state")
			}
		})
	}
}

func TestDirectoryOwnerSeparatesControllersWithIdenticalResourceNames(t *testing.T) {
	p, dir, l := ownershipFixture(t)
	if err := os.MkdirAll(l.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	identity := owner("test-controller", "ComposeApplication", "media")
	marker := filepath.Join(l.Directory, ".openctl-owner")
	if err := os.WriteFile(marker, []byte(identity), 0600); err != nil {
		t.Fatal(err)
	}
	p.controllerID = "other-controller"
	if err := p.claim(context.Background(), "ComposeApplication", "media", l); err == nil {
		t.Fatal("another controller claimed the same resource name and guest directory")
	}
	current, err := os.ReadFile(marker)
	if err != nil || string(current) != identity {
		t.Fatal("foreign controller modified directory ownership")
	}
	if _, err := os.Stat(filepath.Join(dir, "trace")); !os.IsNotExist(err) {
		t.Fatal("foreign directory claim reached Docker mutation")
	}
}
