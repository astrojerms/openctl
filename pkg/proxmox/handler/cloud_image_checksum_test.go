package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestCloudImageChecksumMismatchBlocksTemplateCreation(t *testing.T) {
	image := []byte("downloaded cloud image bytes")
	actual := sha256.Sum256(image)
	mutations := 0
	exitStatus := "OK"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api2/json/nodes":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"node": "pve1"}}})
		case r.URL.Path == "/api2/json/nodes/pve1/qemu" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		case strings.HasSuffix(r.URL.Path, "/download-url"):
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			if r.Form.Get("checksum-algorithm") == "sha256" && r.Form.Get("checksum") != hex.EncodeToString(actual[:]) {
				exitStatus = "checksum mismatch"
			}
			json.NewEncoder(w).Encode(map[string]any{"data": "UPID:pve1:download"})
		case strings.HasPrefix(r.URL.Path, "/api2/json/nodes/pve1/tasks/"):
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"status": "stopped", "exitstatus": exitStatus}})
		default:
			if r.Method != http.MethodGet {
				mutations++
			}
			http.Error(w, "template mutation must not follow a failed checksum", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	h := New(&protocol.ProviderConfig{Endpoint: server.URL, Node: "pve1"})
	_, err := h.Handle(context.Background(), checksumCreateRequest("sha256:"+strings.Repeat("0", 64)))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") || mutations != 0 {
		t.Fatalf("mismatched image must fail before template creation: err=%v mutations=%d", err, mutations)
	}
}

func checksumCreateRequest(checksum string) *protocol.Request {
	return &protocol.Request{
		Action: protocol.ActionCreate, ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine",
			Metadata: protocol.ResourceMetadata{Name: "checksum-vm"},
			Spec: map[string]any{
				"node":          "pve1",
				"cloudImage":    map[string]any{"url": "https://example.invalid/noble.img", "storage": "local", "templateName": "checksum-template", "checksum": checksum},
				"startOnCreate": false,
			},
		},
	}
}

func TestCloudImageChecksumCacheRequiresVerifiedDigest(t *testing.T) {
	requested := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, tags string
		allowClone bool
	}{
		{"unverified", "", false},
		{"wrong digest", "openctl-image-sha256-" + strings.Repeat("b", 64), false},
		{"matching digest", "site-tag;openctl-image-sha256-" + strings.Repeat("a", 64) + ";other", true},
		{"partial tag", "openctl-image-sha256-" + strings.Repeat("a", 64) + "-suffix", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clones, downloads := 0, 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api2/json/nodes":
					json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"node": "pve1"}}})
				case r.URL.Path == "/api2/json/nodes/pve1/qemu":
					json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"vmid": 9000, "name": "checksum-template", "template": 1}}})
				case r.URL.Path == "/api2/json/nodes/pve1/qemu/9000/config":
					json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"tags": tc.tags}})
				case r.URL.Path == "/api2/json/cluster/nextid":
					json.NewEncoder(w).Encode(map[string]any{"data": "200"})
				case r.URL.Path == "/api2/json/nodes/pve1/qemu/9000/clone":
					clones++
					json.NewEncoder(w).Encode(map[string]any{"data": ""})
				case r.URL.Path == "/api2/json/nodes/pve1/qemu/200/config":
					json.NewEncoder(w).Encode(map[string]any{"data": ""})
				case r.URL.Path == "/api2/json/nodes/pve1/storage":
					json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
				case strings.HasSuffix(r.URL.Path, "/download-url"):
					downloads++
					http.Error(w, "cache must not redownload", http.StatusBadRequest)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
				}
			}))
			defer server.Close()
			h := New(&protocol.ProviderConfig{Endpoint: server.URL, Node: "pve1"})
			response, err := h.Handle(context.Background(), checksumCreateRequest(requested))
			if tc.allowClone {
				if err != nil || response == nil || response.Status != protocol.StatusSuccess || clones != 1 || downloads != 0 {
					t.Fatalf("verified cache must clone without downloading: response=%+v err=%v clones=%d downloads=%d", response, err, clones, downloads)
				}
			} else if err == nil || clones != 0 || downloads != 0 {
				t.Fatalf("unverified cache must be rejected before mutation: err=%v clones=%d downloads=%d", err, clones, downloads)
			}
		})
	}
}

func TestCloudImageInvalidChecksumRejectedBeforeCacheLookup(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "invalid checksum must not access the cache", http.StatusBadRequest)
	}))
	defer server.Close()
	h := New(&protocol.ProviderConfig{Endpoint: server.URL, Node: "pve1"})
	response, err := h.Handle(context.Background(), checksumCreateRequest("sha256:abc123"))
	if err != nil || response == nil || response.Error == nil || response.Error.Code != protocol.ErrorCodeInvalidRequest || requests != 0 {
		t.Fatalf("malformed checksum must be an invalid request before API access: response=%+v err=%v requests=%d", response, err, requests)
	}
}
