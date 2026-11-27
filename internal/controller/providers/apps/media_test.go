package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

const testMediaContainerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const healthyMediaMounts = `[{"Type":"bind","Source":"/mnt/synology","Destination":"/media","RW":false}]`
const healthyMediaInspection = `{"id":"` + testMediaContainerID + `","mounts":` + healthyMediaMounts + `}`

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

func TestObservedJellyfinMediaRejectsRuntimeMisbindings(t *testing.T) {
	projection := func(mounts string) string {
		return `{"id":"` + testMediaContainerID + `","mounts":` + mounts + `}`
	}
	for _, tc := range []struct {
		name, output, mediaPath, state, failure string
	}{
		{"read-only container bind", healthyMediaInspection, "", "ready", ""},
		{"quoted subdirectory", projection(`[{"Type":"bind","Source":"/mnt/synology/Kid's Movies","Destination":"/media","RW":false}]`), "Kid's Movies", "ready", ""},
		{"writable container bind", projection(`[{"Type":"bind","Source":"/mnt/synology","Destination":"/media","RW":true}]`), "", "not_ready", ""},
		{"wrong container source", projection(`[{"Type":"bind","Source":"/srv/PRIVATE-MEDIA","Destination":"/media","RW":false}]`), "", "not_ready", ""},
		{"share root instead of selected subdirectory", healthyMediaInspection, "films", "not_ready", ""},
		{"named volume", projection(`[{"Type":"volume","Source":"/mnt/synology","Destination":"/media","RW":false}]`), "", "not_ready", ""},
		{"missing media bind", projection(`[]`), "", "not_ready", ""},
		{"shadowed media subdirectory", projection(`[{"Type":"bind","Source":"/mnt/synology","Destination":"/media","RW":false},{"Type":"bind","Source":"/srv/PRIVATE-MEDIA","Destination":"/media/films","RW":true}]`), "", "not_ready", ""},
		{"unknown read-only flag", projection(`[{"Type":"bind","Source":"/mnt/synology","Destination":"/media"}]`), "", "unknown", ""},
		{"null read-only flag", projection(`[{"Type":"bind","Source":"/mnt/synology","Destination":"/media","RW":null}]`), "", "unknown", ""},
		{"invalid read-only flag", projection(`[{"Type":"bind","Source":"/mnt/synology","Destination":"/media","RW":"false"}]`), "", "unknown", ""},
		{"duplicate media bind", projection(`[{"Type":"bind","Source":"/mnt/synology","Destination":"/media","RW":false},{"Type":"bind","Source":"/mnt/synology","Destination":"/media","RW":false}]`), "", "unknown", ""},
		{"invalid inspection JSON", "PRIVATE-MEDIA-PROBE", "", "unknown", ""},
		{"empty inspection", "", "", "unknown", ""},
		{"null inspection", `null`, "", "unknown", ""},
		{"missing mounts", `{"id":"` + testMediaContainerID + `"}`, "", "unknown", ""},
		{"null mounts", projection(`null`), "", "unknown", ""},
		{"null mount row", projection(`[null]`), "", "unknown", ""},
		{"missing mount destination", projection(`[{}]`), "", "unknown", ""},
		{"missing media mount type", projection(`[{"Source":"/mnt/synology","Destination":"/media","RW":false}]`), "", "unknown", ""},
		{"missing media mount source", projection(`[{"Type":"bind","Destination":"/media","RW":false}]`), "", "unknown", ""},
		{"missing container identity", `{"mounts":` + healthyMediaMounts + `}`, "", "unknown", ""},
		{"unsafe container identity", `{"id":"PRIVATE-MEDIA; echo unsafe","mounts":` + healthyMediaMounts + `}`, "", "unknown", ""},
		{"host mount inaccessible", healthyMediaInspection, "", "not_ready", "host"},
		{"container observation unavailable", healthyMediaInspection, "", "unknown", "inspect"},
		{"container media inaccessible", healthyMediaInspection, "", "not_ready", "access"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, state := testProvider()
			r := jellyfinResource()
			r.Spec["nas"].(map[string]any)["mediaPath"] = tc.mediaPath
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
				if strings.Contains(command, "findmnt ") && tc.failure == "host" {
					return "PRIVATE-MEDIA-PROBE", errors.New("PRIVATE-MEDIA-ERROR")
				}
				if strings.Contains(command, ".Mounts") {
					if tc.failure == "inspect" {
						return "PRIVATE-MEDIA-PROBE", errors.New("PRIVATE-MEDIA-ERROR")
					}
					return tc.output, nil
				}
				if strings.Contains(command, "--user 1000:1000") {
					// Simulate service replacement: only the inspected full ID
					// remains a valid target for this observation's access probe.
					if !strings.Contains(command, quote(testMediaContainerID)) || strings.Contains(command, "ps -q") {
						return "", errors.New("container identity changed")
					}
					if tc.failure == "access" {
						return "PRIVATE-MEDIA-PROBE", errors.New("PRIVATE-MEDIA-ERROR")
					}
				}
				if strings.Contains(command, "ps --all") {
					return `[{"Service":"jellyfin","State":"running","Health":"healthy"}]`, nil
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
			check := observed.Status["checks"].(map[string]any)["media"].(map[string]any)
			if check["state"] != tc.state || observed.Status["ready"] != (tc.state == "ready") {
				t.Fatalf("runtime media evidence did not gate readiness: %#v", observed.Status)
			}
			if !bytes.Equal(before, state.values[key]) {
				t.Fatal("media inspection changed persisted application placement")
			}
			status, err := json.Marshal(observed.Status)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(status), "PRIVATE-MEDIA") || strings.Contains(string(status), testMediaContainerID) {
				t.Fatal("readiness status exposed raw runtime or private probe evidence")
			}
		})
	}
}
