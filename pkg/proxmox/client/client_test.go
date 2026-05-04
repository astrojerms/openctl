package client

import (
	"encoding/json"
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

	vms, err := c.ListVMs()
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

	vm, err := c.GetVM("test-vm")
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

	_, err := c.GetVM("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent VM")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
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

	config, err := c.GetVMConfig("pve1", 100)
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

	templates, err := c.ListTemplates()
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

	tmpl, err := c.GetTemplate("ubuntu-22.04")
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

	_, err := c.GetTemplate("nonexistent")
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

	vmid, upid, err := c.CloneVM("pve1", 9000, "new-vm", nil)
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
	err := c.ConfigureVM("pve1", 100, params)
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

	upid, err := c.StartVM("pve1", 100)
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

	upid, err := c.StopVM("pve1", 100)
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

	err := c.DeleteVM("pve1", 100)
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

	err := c.ResizeVMDisk("pve1", 100, "scsi0", "50G")
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

	info, err := c.GetStorageInfo("local-lvm")
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

	_, err := c.ListVMs()
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

	c.ListVMs()

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

	err := c.ConvertToTemplate("pve1", 100)
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

	err := c.AddCloudInitDrive("pve1", 100, "local-lvm")
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

	upid, err := c.DownloadToStorage("pve1", "local", "https://example.com/image.img", "image.img", "iso")
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
