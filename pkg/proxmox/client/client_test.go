package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockServer creates a test server that returns predefined responses
func mockServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(handler)
}

func TestNew(t *testing.T) {
	c := New("https://pve.example.com:8006", "root@pam!token", "secret")

	if c.endpoint != "https://pve.example.com:8006" {
		t.Errorf("expected endpoint without trailing slash, got %s", c.endpoint)
	}
	if c.tokenID != "root@pam!token" {
		t.Errorf("expected tokenID=root@pam!token, got %s", c.tokenID)
	}
	if c.tokenSecret != "secret" {
		t.Errorf("expected tokenSecret=secret, got %s", c.tokenSecret)
	}
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	c := New("https://pve.example.com:8006/", "token", "secret")

	if c.endpoint != "https://pve.example.com:8006" {
		t.Errorf("expected trailing slash to be trimmed, got %s", c.endpoint)
	}
}

func TestListVMs(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/nodes":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{
					{"node": "pve1"},
					{"node": "pve2"},
				},
			})
		case "/api2/json/nodes/pve1/qemu":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"vmid": 100, "name": "vm1", "status": "running", "template": 0},
					{"vmid": 9000, "name": "template1", "status": "stopped", "template": 1},
				},
			})
		case "/api2/json/nodes/pve2/qemu":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"vmid": 101, "name": "vm2", "status": "stopped", "template": 0},
				},
			})
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			http.Error(w, "not found", 404)
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	vms, err := c.ListVMs(context.Background())
	if err != nil {
		t.Fatalf("ListVMs failed: %v", err)
	}

	// Should return 3 VMs total (including template)
	if len(vms) != 3 {
		t.Errorf("expected 3 VMs, got %d", len(vms))
	}

	// Check that node is populated
	foundVM1 := false
	for _, vm := range vms {
		if vm.Name == "vm1" {
			foundVM1 = true
			if vm.Node != "pve1" {
				t.Errorf("expected vm1 node=pve1, got %s", vm.Node)
			}
			if vm.VMID != 100 {
				t.Errorf("expected vm1 vmid=100, got %d", vm.VMID)
			}
		}
	}
	if !foundVM1 {
		t.Error("vm1 not found in results")
	}
}

func TestGetVM(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/nodes":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"node": "pve1"}},
			})
		case "/api2/json/nodes/pve1/qemu":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"vmid": 100, "name": "test-vm", "status": "running"},
				},
			})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	vm, err := c.GetVM(context.Background(), "test-vm")
	if err != nil {
		t.Fatalf("GetVM failed: %v", err)
	}

	if vm.Name != "test-vm" {
		t.Errorf("expected name=test-vm, got %s", vm.Name)
	}
	if vm.VMID != 100 {
		t.Errorf("expected vmid=100, got %d", vm.VMID)
	}
}

func TestGetVM_NotFound(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/nodes":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"node": "pve1"}},
			})
		case "/api2/json/nodes/pve1/qemu":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{},
			})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	_, err := c.GetVM(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent VM")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

// TestGetVM_NotFoundSentinel verifies that a genuine miss (Proxmox reachable,
// no VM by that name) wraps ErrNotFound so callers can branch on it.
func TestGetVM_NotFoundSentinel(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/nodes":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"node": "pve1"}},
			})
		case "/api2/json/nodes/pve1/qemu":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	_, err := c.GetVM(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent VM")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected err to wrap ErrNotFound, got: %v", err)
	}
}

// TestGetVM_TransientErrorNotSentinel verifies that a transient failure (the
// Proxmox API erroring) is NOT reported as ErrNotFound — otherwise a reconcile
// would conclude the VM is gone and recreate it, and Apply would clone a
// duplicate.
func TestGetVM_TransientErrorNotSentinel(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Node listing itself fails (e.g. Proxmox 5xx / auth blip).
		http.Error(w, "internal error", http.StatusInternalServerError)
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	_, err := c.GetVM(context.Background(), "test-vm")
	if err == nil {
		t.Fatal("expected error when the API is failing")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("transient API error must not be classified as ErrNotFound, got: %v", err)
	}
}

// TestContextCancellation verifies the client honors a canceled context so a
// canceled Watch/reconcile aborts the in-flight request instead of waiting out
// the client timeout.
func TestContextCancellation(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{}})
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before the call

	_, err := c.ListVMs(ctx)
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestGetVMConfig(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/nodes/pve1/qemu/100/config" {
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"name":    "test-vm",
					"cores":   4,
					"sockets": 2,
					"memory":  8192,
				},
			})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	config, err := c.GetVMConfig(context.Background(), "pve1", 100)
	if err != nil {
		t.Fatalf("GetVMConfig failed: %v", err)
	}

	if config.Cores != 4 {
		t.Errorf("expected cores=4, got %d", config.Cores)
	}
	if config.Sockets != 2 {
		t.Errorf("expected sockets=2, got %d", config.Sockets)
	}
	if config.Memory != 8192 {
		t.Errorf("expected memory=8192, got %d", config.Memory)
	}
}

func TestListTemplates(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/nodes":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"node": "pve1"}},
			})
		case "/api2/json/nodes/pve1/qemu":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"vmid": 100, "name": "vm1", "status": "running", "template": 0},
					{"vmid": 9000, "name": "ubuntu-22.04", "status": "stopped", "template": 1},
					{"vmid": 9001, "name": "debian-12", "status": "stopped", "template": 1},
				},
			})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	templates, err := c.ListTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListTemplates failed: %v", err)
	}

	if len(templates) != 2 {
		t.Errorf("expected 2 templates, got %d", len(templates))
	}

	// Check template properties
	for _, tmpl := range templates {
		if tmpl.VMID != 9000 && tmpl.VMID != 9001 {
			t.Errorf("unexpected template VMID: %d", tmpl.VMID)
		}
	}
}

func TestGetTemplate(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/nodes":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"node": "pve1"}},
			})
		case "/api2/json/nodes/pve1/qemu":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"vmid": 9000, "name": "ubuntu-22.04", "status": "stopped", "template": 1},
				},
			})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	tmpl, err := c.GetTemplate(context.Background(), "ubuntu-22.04")
	if err != nil {
		t.Fatalf("GetTemplate failed: %v", err)
	}

	if tmpl.Name != "ubuntu-22.04" {
		t.Errorf("expected name=ubuntu-22.04, got %s", tmpl.Name)
	}
	if tmpl.VMID != 9000 {
		t.Errorf("expected vmid=9000, got %d", tmpl.VMID)
	}
}

func TestGetTemplate_NotFound(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/nodes":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"node": "pve1"}},
			})
		case "/api2/json/nodes/pve1/qemu":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{},
			})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	_, err := c.GetTemplate(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent template")
	}
}

func TestCloneVM(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api2/json/cluster/nextid":
			json.NewEncoder(w).Encode(map[string]any{
				"data": "200",
			})
		case r.URL.Path == "/api2/json/nodes/pve1/qemu/9000/clone" && r.Method == "POST":
			json.NewEncoder(w).Encode(map[string]any{
				"data": "UPID:pve1:00001234:12345678:clone:9000:root@pam:",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	vmid, upid, err := c.CloneVM(context.Background(), "pve1", 9000, "new-vm", nil)
	if err != nil {
		t.Fatalf("CloneVM failed: %v", err)
	}

	if vmid != 200 {
		t.Errorf("expected vmid=200, got %d", vmid)
	}
	if upid == "" {
		t.Error("expected non-empty UPID")
	}
}

func TestConfigureVM(t *testing.T) {
	var receivedParams map[string]string
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/nodes/pve1/qemu/100/config" && r.Method == "PUT" {
			r.ParseForm()
			receivedParams = make(map[string]string)
			for k, v := range r.Form {
				if len(v) > 0 {
					receivedParams[k] = v[0]
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": nil})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	params := map[string]any{
		"cores":  4,
		"memory": 8192,
	}
	err := c.ConfigureVM(context.Background(), "pve1", 100, params)
	if err != nil {
		t.Fatalf("ConfigureVM failed: %v", err)
	}

	if receivedParams["cores"] != "4" {
		t.Errorf("expected cores=4, got %s", receivedParams["cores"])
	}
	if receivedParams["memory"] != "8192" {
		t.Errorf("expected memory=8192, got %s", receivedParams["memory"])
	}
}

func TestStartVM(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/nodes/pve1/qemu/100/status/start" && r.Method == "POST" {
			json.NewEncoder(w).Encode(map[string]any{
				"data": "UPID:pve1:00001234:12345678:qmstart:100:root@pam:",
			})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	upid, err := c.StartVM(context.Background(), "pve1", 100)
	if err != nil {
		t.Fatalf("StartVM failed: %v", err)
	}

	if upid == "" {
		t.Error("expected non-empty UPID")
	}
}

func TestStopVM(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/nodes/pve1/qemu/100/status/stop" && r.Method == "POST" {
			json.NewEncoder(w).Encode(map[string]any{
				"data": "UPID:pve1:00001234:12345678:qmstop:100:root@pam:",
			})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	upid, err := c.StopVM(context.Background(), "pve1", 100)
	if err != nil {
		t.Fatalf("StopVM failed: %v", err)
	}

	if upid == "" {
		t.Error("expected non-empty UPID")
	}
}

func TestDeleteVM(t *testing.T) {
	deleted := false
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/nodes/pve1/qemu/100" && r.Method == "DELETE" {
			deleted = true
			json.NewEncoder(w).Encode(map[string]any{"data": nil})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	err := c.DeleteVM(context.Background(), "pve1", 100)
	if err != nil {
		t.Fatalf("DeleteVM failed: %v", err)
	}

	if !deleted {
		t.Error("expected DELETE request to be made")
	}
}

func TestResizeVMDisk(t *testing.T) {
	var receivedParams map[string]string
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/nodes/pve1/qemu/100/resize" && r.Method == "PUT" {
			r.ParseForm()
			receivedParams = make(map[string]string)
			for k, v := range r.Form {
				if len(v) > 0 {
					receivedParams[k] = v[0]
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": nil})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	err := c.ResizeVMDisk(context.Background(), "pve1", 100, "scsi0", "50G")
	if err != nil {
		t.Fatalf("ResizeVMDisk failed: %v", err)
	}

	if receivedParams["disk"] != "scsi0" {
		t.Errorf("expected disk=scsi0, got %s", receivedParams["disk"])
	}
	if receivedParams["size"] != "50G" {
		t.Errorf("expected size=50G, got %s", receivedParams["size"])
	}
}

func TestMergeDiskOptions(t *testing.T) {
	cases := []struct {
		name      string
		existing  string
		overrides map[string]string
		want      string
	}{
		{
			"adds flags to bare volref",
			"local-lvm:vm-100-disk-0",
			map[string]string{"ssd": "1", "discard": "on"},
			"local-lvm:vm-100-disk-0,discard=on,ssd=1",
		},
		{
			"preserves size and merges new flags",
			"local-lvm:vm-100-disk-0,size=32G",
			map[string]string{"iothread": "1"},
			"local-lvm:vm-100-disk-0,iothread=1,size=32G",
		},
		{
			"overrides existing flag value",
			"local-lvm:vm-100-disk-0,cache=none,size=32G",
			map[string]string{"cache": "writeback"},
			"local-lvm:vm-100-disk-0,cache=writeback,size=32G",
		},
		{
			"empty overrides leaves string structurally equal",
			"local-lvm:vm-100-disk-0,size=32G,ssd=1",
			map[string]string{},
			"local-lvm:vm-100-disk-0,size=32G,ssd=1",
		},
	}
	for _, tc := range cases {
		got := MergeDiskOptions(tc.existing, tc.overrides)
		if got != tc.want {
			t.Errorf("%s:\n  got  %q\n  want %q", tc.name, got, tc.want)
		}
	}
}

func TestSetDiskOptions(t *testing.T) {
	var configPUT map[string]string
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api2/json/nodes/pve1/qemu/100/config" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"scsi0": "local-lvm:vm-100-disk-0,size=32G",
				},
			})
		case r.URL.Path == "/api2/json/nodes/pve1/qemu/100/config" && r.Method == "PUT":
			_ = r.ParseForm()
			configPUT = map[string]string{}
			for k, v := range r.Form {
				if len(v) > 0 {
					configPUT[k] = v[0]
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": nil})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	err := c.SetDiskOptions(context.Background(), "pve1", 100, "scsi0", map[string]string{"ssd": "1", "discard": "on"})
	if err != nil {
		t.Fatalf("SetDiskOptions: %v", err)
	}
	want := "local-lvm:vm-100-disk-0,discard=on,size=32G,ssd=1"
	if configPUT["scsi0"] != want {
		t.Errorf("PUT scsi0 = %q, want %q", configPUT["scsi0"], want)
	}
}

func TestSetDiskOptions_NoOp(t *testing.T) {
	// Empty opts should not hit the API at all — important for the
	// hot path where most disks won't carry flags.
	called := false
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	defer server.Close()
	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()
	if err := c.SetDiskOptions(context.Background(), "pve1", 100, "scsi0", nil); err != nil {
		t.Fatalf("SetDiskOptions nil opts: %v", err)
	}
	if called {
		t.Error("SetDiskOptions with nil opts should not call the API")
	}
}

func TestGetStorageInfo(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/storage/local-lvm" {
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"storage": "local-lvm",
					"type":    "lvmthin",
					"content": "images,rootdir",
				},
			})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	info, err := c.GetStorageInfo(context.Background(), "local-lvm")
	if err != nil {
		t.Fatalf("GetStorageInfo failed: %v", err)
	}

	if info.Storage != "local-lvm" {
		t.Errorf("expected storage=local-lvm, got %s", info.Storage)
	}
	if info.Type != "lvmthin" {
		t.Errorf("expected type=lvmthin, got %s", info.Type)
	}
	if !strings.Contains(info.Content, "images") {
		t.Errorf("expected content to include 'images', got %s", info.Content)
	}
}

func TestAPIError(t *testing.T) {
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errors": {"username": "invalid credentials"}}`))
	})
	defer server.Close()

	c := New(server.URL, "bad-token", "bad-secret")
	c.httpClient = server.Client()

	_, err := c.ListVMs(context.Background())
	if err == nil {
		t.Fatal("expected error for unauthorized request")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 error, got: %v", err)
	}
}

func TestAuthorizationHeader(t *testing.T) {
	var authHeader string
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{},
		})
	})
	defer server.Close()

	c := New(server.URL, "root@pam!mytoken", "supersecret")
	c.httpClient = server.Client()

	c.ListVMs(context.Background())

	expected := "PVEAPIToken=root@pam!mytoken=supersecret"
	if authHeader != expected {
		t.Errorf("expected Authorization=%s, got %s", expected, authHeader)
	}
}

func TestConvertToTemplate(t *testing.T) {
	converted := false
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/nodes/pve1/qemu/100/template" && r.Method == "POST" {
			converted = true
			json.NewEncoder(w).Encode(map[string]any{"data": nil})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	err := c.ConvertToTemplate(context.Background(), "pve1", 100)
	if err != nil {
		t.Fatalf("ConvertToTemplate failed: %v", err)
	}

	if !converted {
		t.Error("expected POST request to template endpoint")
	}
}

func TestAddCloudInitDrive(t *testing.T) {
	var receivedParams map[string]string
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/nodes/pve1/qemu/100/config" && r.Method == "PUT" {
			r.ParseForm()
			receivedParams = make(map[string]string)
			for k, v := range r.Form {
				if len(v) > 0 {
					receivedParams[k] = v[0]
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": nil})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	err := c.AddCloudInitDrive(context.Background(), "pve1", 100, "local-lvm")
	if err != nil {
		t.Fatalf("AddCloudInitDrive failed: %v", err)
	}

	if receivedParams["ide2"] != "local-lvm:cloudinit" {
		t.Errorf("expected ide2=local-lvm:cloudinit, got %s", receivedParams["ide2"])
	}
}

func TestDownloadToStorage(t *testing.T) {
	var receivedParams map[string]string
	server := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/nodes/pve1/storage/local/download-url" && r.Method == "POST" {
			r.ParseForm()
			receivedParams = make(map[string]string)
			for k, v := range r.Form {
				if len(v) > 0 {
					receivedParams[k] = v[0]
				}
			}
			json.NewEncoder(w).Encode(map[string]any{
				"data": "UPID:pve1:00001234:12345678:download:local:root@pam:",
			})
		}
	})
	defer server.Close()

	c := New(server.URL, "test", "test")
	c.httpClient = server.Client()

	upid, err := c.DownloadToStorage(context.Background(), "pve1", "local", "https://example.com/image.img", "image.img", "iso")
	if err != nil {
		t.Fatalf("DownloadToStorage failed: %v", err)
	}

	if upid == "" {
		t.Error("expected non-empty UPID")
	}
	if receivedParams["url"] != "https://example.com/image.img" {
		t.Errorf("expected url parameter, got %s", receivedParams["url"])
	}
	if receivedParams["filename"] != "image.img" {
		t.Errorf("expected filename parameter, got %s", receivedParams["filename"])
	}
	if receivedParams["content"] != "iso" {
		t.Errorf("expected content=iso, got %s", receivedParams["content"])
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is a ..."},
		{"", 10, ""},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}
