package apps

import (
	"context"
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func jellyfinResource() *protocol.Resource {
	r := composeResource()
	r.Kind = "Jellyfin"
	r.Spec = map[string]any{
		"target": r.Spec["target"], "image": "jellyfin/jellyfin@sha256:" + strings.Repeat("a", 64),
		"nas":    map[string]any{"server": "nas", "share": "media", "credentials": "username=media\npassword=secret\n"},
		"admin":  map[string]any{"username": "admin", "password": "admin-password"},
		"viewer": map[string]any{"username": "viewer", "password": "viewer-password"},
	}
	return r
}

func TestMediaSubdirectoryDefaultsToShareRootAndPreservesCleanPaths(t *testing.T) {
	for _, value := range []string{"", "video", "films/TV shows", "Kid's Movies", "日本語/映画"} {
		t.Run(value, func(t *testing.T) {
			r := jellyfinResource()
			r.Spec["nas"].(map[string]any)["mediaPath"] = value
			_, l, err := parse(r, true)
			if err != nil {
				t.Fatal(err)
			}
			expected := "/mnt/synology"
			if value != "" {
				expected += "/" + value
			}
			if l.NASMediaPath != value || mediaDirectory(l) != expected {
				t.Fatalf("wrong media placement: %+v", l)
			}
			_, preview, err := parse(r, false)
			if err != nil {
				t.Fatal(err)
			}
			if preview.NASMediaPath != l.NASMediaPath {
				t.Fatal("preview lost immutable media subdirectory")
			}
		})
	}
}

func TestMediaSubdirectoryRejectsTraversalAndAmbiguousPaths(t *testing.T) {
	for _, value := range []string{"/video", "../video", "films/../../etc", "films/../video", ".", "..", "films/.", "films//video", "films/", "films\\video", "films\nvideo", "films\tvideo", "films\x00video"} {
		t.Run(value, func(t *testing.T) {
			r := jellyfinResource()
			r.Spec["nas"].(map[string]any)["mediaPath"] = value
			if _, _, err := parse(r, true); err == nil {
				t.Fatalf("unsafe mediaPath accepted: %q", value)
			}
			if _, _, err := parse(r, false); err == nil {
				t.Fatalf("unsafe mediaPath accepted by preview: %q", value)
			}
		})
	}
}

func TestMediaSubdirectoryCannotChangeExistingApplicationPlacement(t *testing.T) {
	p, _ := testProvider()
	r := jellyfinResource()
	_, old, err := parse(r, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.save(context.Background(), r.Kind, r.Metadata.Name, old); err != nil {
		t.Fatal(err)
	}
	calls := 0
	p.runner = fakeRunner(func(context.Context, Target, string, []byte) (string, error) { calls++; return "", nil })
	r.Spec["nas"].(map[string]any)["mediaPath"] = "video"
	if _, err := p.DryRun(context.Background(), r); err == nil {
		t.Fatal("preview allowed switching media directories")
	}
	if _, err := p.Apply(context.Background(), r); err == nil {
		t.Fatal("apply allowed switching media directories")
	}
	if calls != 0 {
		t.Fatal("unsafe media placement change reached guest")
	}
}
