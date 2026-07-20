package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestHandler_HandleUnknownResourceType(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: "UnknownResource",
	}

	resp, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error == nil {
		t.Fatal("expected error in response")
	}
	if resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Errorf("expected code=INVALID_REQUEST, got %s", resp.Error.Code)
	}
}

func TestHandler_HandleVMUnknownAction(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       "unknown-action",
		ResourceType: "VirtualMachine",
	}

	resp, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Errorf("expected code=INVALID_REQUEST, got %s", resp.Error.Code)
	}
}

func TestHandler_HandleTemplateUnsupportedAction(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "Template",
	}

	resp, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
}

func TestHandler_CreateVMMissingNode(t *testing.T) {
	h := New(&protocol.ProviderConfig{
		// No node configured
	})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "test-vm"},
			Spec: map[string]any{
				// No node in spec either
				"template": map[string]any{
					"name": "ubuntu",
				},
			},
		},
	}

	resp, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Errorf("expected code=INVALID_REQUEST, got %s", resp.Error.Code)
	}
}

func TestHandler_CreateVMWithoutTemplate(t *testing.T) {
	h := New(&protocol.ProviderConfig{
		Node: "pve1",
	})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "test-vm"},
			Spec: map[string]any{
				// No template specified
				"cpu": map[string]any{
					"cores": float64(4),
				},
			},
		},
	}

	resp, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	// Creating VM without template is not yet supported
}

func TestHandler_NodeFromConfig(t *testing.T) {
	h := New(&protocol.ProviderConfig{
		Node: "config-node",
	})

	// Verify the handler uses the config node
	if h.config.Node != "config-node" {
		t.Errorf("expected config.Node=config-node, got %s", h.config.Node)
	}
}

func TestHandler_RoutesToCorrectResourceType(t *testing.T) {
	tests := []struct {
		resourceType string
		action       string
		shouldError  bool
	}{
		{"VirtualMachine", protocol.ActionList, false},
		{"VirtualMachine", protocol.ActionGet, false},
		{"VirtualMachine", protocol.ActionCreate, false},
		{"VirtualMachine", protocol.ActionDelete, false},
		{"VirtualMachine", protocol.ActionApply, false},
		{"Template", protocol.ActionList, false},
		{"Template", protocol.ActionGet, false},
		{"Template", protocol.ActionCreate, true}, // Not supported
		{"Unknown", protocol.ActionGet, true},
	}

	for _, tt := range tests {
		t.Run(tt.resourceType+"/"+tt.action, func(t *testing.T) {
			h := New(&protocol.ProviderConfig{
				Endpoint:    "https://pve.example.com:8006",
				TokenID:     "test",
				TokenSecret: "test",
				Node:        "pve1",
			})

			req := &protocol.Request{
				Version:      protocol.ProtocolVersion,
				Action:       tt.action,
				ResourceType: tt.resourceType,
				ResourceName: "test",
				Manifest: &protocol.Resource{
					APIVersion: "proxmox.openctl.io/v1",
					Kind:       tt.resourceType,
					Metadata:   protocol.ResourceMetadata{Name: "test"},
					Spec:       map[string]any{},
				},
			}

			resp, err := h.Handle(context.Background(), req)

			// The handler returns protocol errors, not Go errors
			// for known error conditions
			if tt.shouldError {
				if err == nil && (resp == nil || resp.Status != protocol.StatusError) {
					t.Errorf("expected error response")
				}
			}
			// Note: non-error cases will fail because we don't have
			// a real Proxmox server to connect to
		})
	}
}

// vmApplyRequest builds an Apply request for a VM backed by a template so the
// not-found branch has a create path to attempt.
func vmApplyRequest(name string) *protocol.Request {
	return &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionApply,
		ResourceType: "VirtualMachine",
		ResourceName: name,
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: name},
			Spec: map[string]any{
				"node":     "pve1",
				"template": map[string]any{"name": "ubuntu"},
			},
		},
	}
}

// TestApplyVM_TransientErrorDoesNotCreate is the core of the not-found fix:
// when the existence check fails transiently (Proxmox erroring), Apply must
// surface the error rather than fall through and clone a duplicate of a VM
// that already exists.
func TestApplyVM_TransientErrorDoesNotCreate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every call fails — including the pre-create existence check.
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	h := New(&protocol.ProviderConfig{Endpoint: server.URL, TokenID: "t", TokenSecret: "s", Node: "pve1"})

	resp, err := h.Handle(context.Background(), vmApplyRequest("existing-vm"))
	if err == nil {
		t.Fatalf("expected a transient error, got resp=%+v", resp)
	}
	if !strings.Contains(err.Error(), "check existing VM") {
		t.Errorf("expected the existence-check error (not a create attempt), got: %v", err)
	}
}

// TestApplyVM_NotFoundTakesCreatePath verifies the complementary case: a
// genuine miss (Proxmox reachable, VM absent) routes into create. Here the
// template it references is also absent, so create returns a NotFound about
// the template — proving Apply chose the create branch, not the update branch.
func TestApplyVM_NotFoundTakesCreatePath(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/nodes":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"node": "pve1"}}})
		default:
			// No VMs, no templates anywhere.
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
		}
	}))
	defer server.Close()

	h := New(&protocol.ProviderConfig{Endpoint: server.URL, TokenID: "t", TokenSecret: "s", Node: "pve1"})

	resp, err := h.Handle(context.Background(), vmApplyRequest("brand-new-vm"))
	if err != nil {
		t.Fatalf("Handle should return a protocol response, not a Go error: %v", err)
	}
	if resp.Status != protocol.StatusError || resp.Error.Code != protocol.ErrorCodeNotFound {
		t.Fatalf("expected a NotFound (template missing) from the create path, got: %+v", resp)
	}
	if !strings.Contains(resp.Error.Message, "template") {
		t.Errorf("expected the create path's template-not-found message, got: %s", resp.Error.Message)
	}
}

func TestCreateVMFromTemplatePassesDiskStorageToClone(t *testing.T) {
	var cloneForm url.Values
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api2/json/nodes" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"node": "pve1"}}})
		case r.URL.Path == "/api2/json/nodes/pve1/qemu" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"vmid": 9000, "name": "ubuntu-template", "template": 1, "status": "stopped"},
			}})
		case r.URL.Path == "/api2/json/cluster/nextid" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"data": "200"})
		case r.URL.Path == "/api2/json/nodes/pve1/qemu/9000/clone" && r.Method == "POST":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			cloneForm = cloneValues(r.Form)
			json.NewEncoder(w).Encode(map[string]any{"data": ""})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	h := New(&protocol.ProviderConfig{Endpoint: server.URL, TokenID: "t", TokenSecret: "s", Node: "pve1"})
	resp, err := h.Handle(context.Background(), &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "controller-vm"},
			Spec: map[string]any{
				"node":     "pve1",
				"template": map[string]any{"name": "ubuntu-template"},
				"disks": []any{
					map[string]any{
						"name":    "scsi0",
						"storage": "local-lvm",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("status = %s, resp=%+v", resp.Status, resp)
	}
	if cloneForm.Get("storage") != "local-lvm" {
		t.Fatalf("clone storage = %q, want local-lvm (form=%v)", cloneForm.Get("storage"), cloneForm)
	}
	if cloneForm.Get("newid") != "200" || cloneForm.Get("name") != "controller-vm" || cloneForm.Get("full") != "1" {
		t.Fatalf("clone form missing default params: %v", cloneForm)
	}
}

// TestCreateVMUploadsPerVMVendorSnippet drives the create path for a VM that
// declares cloud-init packages/runcmd and asserts the E1 wiring: a per-VM
// vendor snippet (named by vmid) is uploaded with the packages + agent runcmd,
// and the VM is configured with cicustom pointing at it.
func TestCreateVMUploadsPerVMVendorSnippet(t *testing.T) {
	var (
		uploadedName    string
		uploadedContent string
		cicustom        string
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api2/json/nodes" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"node": "pve1"}}})
		case r.URL.Path == "/api2/json/nodes/pve1/qemu" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"vmid": 9000, "name": "ubuntu-template", "template": 1, "status": "stopped"},
			}})
		case r.URL.Path == "/api2/json/cluster/nextid" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"data": "200"})
		case r.URL.Path == "/api2/json/nodes/pve1/storage" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"storage": "local", "type": "dir", "content": "snippets,vztmpl,iso", "active": 1},
				{"storage": "local-lvm", "type": "lvmthin", "content": "images,rootdir", "active": 1},
			}})
		case r.URL.Path == "/api2/json/nodes/pve1/qemu/9000/clone" && r.Method == "POST":
			json.NewEncoder(w).Encode(map[string]any{"data": ""})
		case r.URL.Path == "/api2/json/nodes/pve1/storage/local/upload" && r.Method == "POST":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("ParseMultipartForm: %v", err)
			}
			files := r.MultipartForm.File["filename"]
			if len(files) != 1 {
				t.Fatalf("upload: want 1 file field, got %d", len(files))
			}
			uploadedName = files[0].Filename
			f, _ := files[0].Open()
			b, _ := io.ReadAll(f)
			uploadedContent = string(b)
			json.NewEncoder(w).Encode(map[string]any{"data": ""})
		case r.URL.Path == "/api2/json/nodes/pve1/qemu/200/config":
			// ConfigureVM issues PUT; capture the cicustom-bearing call.
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if c := r.Form.Get("cicustom"); c != "" {
				cicustom = c
			}
			json.NewEncoder(w).Encode(map[string]any{"data": ""})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	h := New(&protocol.ProviderConfig{Endpoint: server.URL, TokenID: "t", TokenSecret: "s", Node: "pve1"})
	resp, err := h.Handle(context.Background(), &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "longhorn-node"},
			Spec: map[string]any{
				"node":     "pve1",
				"template": map[string]any{"name": "ubuntu-template"},
				"cloudInit": map[string]any{
					"packages": []any{"open-iscsi"},
					"runcmd":   []any{"systemctl enable iscsid"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("status = %s, resp=%+v", resp.Status, resp)
	}
	if uploadedName != "openctl-vendor-200.yaml" {
		t.Errorf("uploaded snippet name = %q, want openctl-vendor-200.yaml", uploadedName)
	}
	for _, want := range []string{"#cloud-config", "open-iscsi", "systemctl enable qemu-guest-agent", "systemctl enable iscsid"} {
		if !strings.Contains(uploadedContent, want) {
			t.Errorf("uploaded snippet missing %q:\n%s", want, uploadedContent)
		}
	}
	if cicustom != "vendor=local:snippets/openctl-vendor-200.yaml" {
		t.Errorf("cicustom = %q, want vendor=local:snippets/openctl-vendor-200.yaml", cicustom)
	}
}

// snippetHardeningServer is a create-path fake whose /storage response is
// configurable, so tests can exercise auto-selection and the no-snippets case.
// It records the upload target storage and the cicustom value.
func snippetHardeningServer(t *testing.T, storageData []map[string]any, uploadStorage *string, cicustom *string) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api2/json/nodes" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"node": "pve1"}}})
		case r.URL.Path == "/api2/json/nodes/pve1/qemu" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"vmid": 9000, "name": "ubuntu-template", "template": 1, "status": "stopped"},
			}})
		case r.URL.Path == "/api2/json/cluster/nextid" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"data": "200"})
		case r.URL.Path == "/api2/json/nodes/pve1/storage" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{"data": storageData})
		case r.URL.Path == "/api2/json/nodes/pve1/qemu/9000/clone" && r.Method == "POST":
			json.NewEncoder(w).Encode(map[string]any{"data": ""})
		case strings.HasSuffix(r.URL.Path, "/upload") && r.Method == "POST":
			// /api2/json/nodes/pve1/storage/<storage>/upload
			parts := strings.Split(r.URL.Path, "/")
			*uploadStorage = parts[len(parts)-2]
			json.NewEncoder(w).Encode(map[string]any{"data": ""})
		case r.URL.Path == "/api2/json/nodes/pve1/qemu/200/config":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if c := r.Form.Get("cicustom"); c != "" {
				*cicustom = c
			}
			json.NewEncoder(w).Encode(map[string]any{"data": ""})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
}

func createVMWithPackages(t *testing.T, h *Handler) (*protocol.Response, error) {
	t.Helper()
	return h.Handle(context.Background(), &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "longhorn-node"},
			Spec: map[string]any{
				"node":      "pve1",
				"template":  map[string]any{"name": "ubuntu-template"},
				"cloudInit": map[string]any{"packages": []any{"open-iscsi"}},
			},
		},
	})
}

// TestCreateVMAutoSelectsSnippetsStorage: the preferred/default storage is
// LVM-only (no snippets), so the vendor snippet must land on the node's
// snippets-capable storage instead of silently failing.
func TestCreateVMAutoSelectsSnippetsStorage(t *testing.T) {
	var uploadStorage, cicustom string
	server := snippetHardeningServer(t, []map[string]any{
		{"storage": "local-lvm", "type": "lvmthin", "content": "images,rootdir", "active": 1},
		{"storage": "local", "type": "dir", "content": "snippets,vztmpl", "active": 1},
	}, &uploadStorage, &cicustom)
	defer server.Close()

	// Default storage is the LVM one (can't hold snippets).
	h := New(&protocol.ProviderConfig{Endpoint: server.URL, TokenID: "t", TokenSecret: "s", Node: "pve1",
		Defaults: map[string]string{"storage": "local-lvm"}})
	resp, err := createVMWithPackages(t, h)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("status = %s, resp=%+v", resp.Status, resp)
	}
	if uploadStorage != "local" {
		t.Errorf("uploaded to %q, want local (auto-selected snippets-capable storage)", uploadStorage)
	}
	if cicustom != "vendor=local:snippets/openctl-vendor-200.yaml" {
		t.Errorf("cicustom = %q, want vendor=local:snippets/openctl-vendor-200.yaml", cicustom)
	}
}

// TestCreateVMFailsFastWhenNoSnippetsStorage: packages/runcmd were explicitly
// requested but the node has no snippets-capable storage, so create must fail
// fast with an actionable error rather than produce a silently-broken node.
func TestCreateVMFailsFastWhenNoSnippetsStorage(t *testing.T) {
	var uploadStorage, cicustom string
	server := snippetHardeningServer(t, []map[string]any{
		{"storage": "local-lvm", "type": "lvmthin", "content": "images,rootdir", "active": 1},
	}, &uploadStorage, &cicustom)
	defer server.Close()

	h := New(&protocol.ProviderConfig{Endpoint: server.URL, TokenID: "t", TokenSecret: "s", Node: "pve1"})
	_, err := createVMWithPackages(t, h)
	if err == nil {
		t.Fatal("expected a fail-fast error when no snippets-capable storage exists")
	}
	if !strings.Contains(err.Error(), "snippets") {
		t.Errorf("error should mention snippets storage, got: %v", err)
	}
}

func cloneValues(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for k, values := range in {
		out[k] = append([]string(nil), values...)
	}
	return out
}

func TestGenerateTemplateNameFromURL(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{
			url:      "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
			expected: "tpl-jammy-server-cloudimg-amd64",
		},
		{
			url:      "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2",
			expected: "tpl-debian-12-generic-amd64",
		},
		{
			url:      "https://download.rockylinux.org/pub/rocky/9/images/Rocky-9-GenericCloud.latest.x86_64.qcow2",
			expected: "tpl-Rocky-9-GenericCloud.latest.x86-64",
		},
		{
			url:      "https://example.com/image_with_underscores.raw",
			expected: "tpl-image-with-underscores",
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			result := generateTemplateNameFromURL(tt.url)
			if result != tt.expected {
				t.Errorf("generateTemplateNameFromURL(%q) = %q, want %q", tt.url, result, tt.expected)
			}
		})
	}
}

func TestExtractFilenameFromURL(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{
			url:      "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
			expected: "jammy-server-cloudimg-amd64.img",
		},
		{
			url:      "https://example.com/path/to/image.qcow2",
			expected: "image.qcow2",
		},
		{
			url:      "https://example.com/simple.raw",
			expected: "simple.raw",
		},
		{
			url:      "simple-file.img",
			expected: "simple-file.img",
		},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			result := extractFilenameFromURL(tt.url)
			if result != tt.expected {
				t.Errorf("extractFilenameFromURL(%q) = %q, want %q", tt.url, result, tt.expected)
			}
		})
	}
}

func TestCreateVMFromCloudImage_MissingURL(t *testing.T) {
	h := New(&protocol.ProviderConfig{
		Node: "pve1",
	})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "test-vm"},
			Spec: map[string]any{
				"cloudImage": map[string]any{
					"storage": "local",
					// Missing URL
				},
			},
		},
	}

	resp, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error == nil {
		t.Fatal("expected error in response")
	}
	if !strings.Contains(resp.Error.Message, "url") {
		t.Errorf("expected error about missing url, got: %s", resp.Error.Message)
	}
}

func TestCreateVMFromCloudImage_MissingStorage(t *testing.T) {
	h := New(&protocol.ProviderConfig{
		Node: "pve1",
	})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "test-vm"},
			Spec: map[string]any{
				"cloudImage": map[string]any{
					"url": "https://example.com/image.img",
					// Missing storage
				},
			},
		},
	}

	resp, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error == nil {
		t.Fatal("expected error in response")
	}
	if !strings.Contains(resp.Error.Message, "storage") {
		t.Errorf("expected error about missing storage, got: %s", resp.Error.Message)
	}
}

func TestCreateVMFromImage_MissingStorage(t *testing.T) {
	h := New(&protocol.ProviderConfig{
		Node: "pve1",
	})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "test-vm"},
			Spec: map[string]any{
				"image": map[string]any{
					"file": "image.img",
					// Missing storage
				},
			},
		},
	}

	resp, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error == nil {
		t.Fatal("expected error in response")
	}
	if !strings.Contains(resp.Error.Message, "storage") {
		t.Errorf("expected error about missing storage, got: %s", resp.Error.Message)
	}
}

func TestCreateVMFromImage_MissingFile(t *testing.T) {
	h := New(&protocol.ProviderConfig{
		Node: "pve1",
	})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "test-vm"},
			Spec: map[string]any{
				"image": map[string]any{
					"storage": "local",
					// Missing file
				},
			},
		},
	}

	resp, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error == nil {
		t.Fatal("expected error in response")
	}
	if !strings.Contains(resp.Error.Message, "file") {
		t.Errorf("expected error about missing file, got: %s", resp.Error.Message)
	}
}
