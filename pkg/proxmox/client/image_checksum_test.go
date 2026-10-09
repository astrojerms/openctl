package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseImageChecksum(t *testing.T) {
	for algorithm, size := range map[string]int{"md5": 32, "sha1": 40, "sha224": 56, "sha256": 64, "sha384": 96, "sha512": 128} {
		t.Run(algorithm, func(t *testing.T) {
			checksum, err := ParseImageChecksum(algorithm + ":" + strings.Repeat("Ab", size/2))
			if err != nil || checksum.algorithm != algorithm || checksum.digest != strings.Repeat("ab", size/2) {
				t.Fatalf("valid digest was not normalized: checksum=%+v err=%v", checksum, err)
			}
		})
	}
	for _, value := range []string{"sha256", "sha256:", "sha256:abc123", "sha256:" + strings.Repeat("g", 64), "sha256:" + strings.Repeat("0", 65), "sha256:" + strings.Repeat("0", 63), "blake3:" + strings.Repeat("0", 64), "sha256:" + strings.Repeat("0", 64) + ":extra", " sha256:" + strings.Repeat("0", 64)} {
		if _, err := ParseImageChecksum(value); err == nil {
			t.Errorf("invalid checksum accepted: %q", value)
		}
	}
	checksum, err := ParseImageChecksum("")
	if err != nil || checksum != (ImageChecksum{}) || checksum.TemplateTag() != "" {
		t.Fatalf("omitted checksum must disable verification: %+v, %v", checksum, err)
	}
}

func TestWaitForTaskRequiresSuccessfulExit(t *testing.T) {
	for _, exitStatus := range []string{"OK", "checksum mismatch", ""} {
		t.Run("exit="+exitStatus, func(t *testing.T) {
			server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"status": "stopped", "exitstatus": exitStatus}})
			})
			defer server.Close()
			c := New(server.URL, "test", "test")
			c.httpClient = server.Client()
			err := c.WaitForTask(context.Background(), "pve1", "UPID:download", time.Second)
			if (err == nil) != (exitStatus == "OK") {
				t.Fatalf("only a successful task may proceed: exitstatus=%q err=%v", exitStatus, err)
			}
		})
	}
}

func TestDownloadRequiresTrackableTask(t *testing.T) {
	for _, response := range []string{`{"data":""}`, `{"data":null}`, `not json`} {
		t.Run(response, func(t *testing.T) {
			server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(response))
			})
			defer server.Close()
			c := New(server.URL, "test", "test")
			c.httpClient = server.Client()
			if _, err := c.DownloadToStorage(context.Background(), "pve1", "local", "https://example.invalid/image.img", "image.qcow2", "import", ImageChecksum{}); err == nil {
				t.Fatal("untrackable download would bypass completion and checksum verification")
			}
		})
	}
}
