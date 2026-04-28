package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestGatherInfoPopulatesBasicFields(t *testing.T) {
	info := gatherInfo()

	if info.OS != runtime.GOOS {
		t.Errorf("OS = %q, want %q", info.OS, runtime.GOOS)
	}
	if info.Arch != runtime.GOARCH {
		t.Errorf("Arch = %q, want %q", info.Arch, runtime.GOARCH)
	}
	if info.AgentVersion != Version {
		t.Errorf("AgentVersion = %q, want %q", info.AgentVersion, Version)
	}
	if info.Hostname == "" {
		t.Error("Hostname is empty")
	}
	if info.Capabilities == nil {
		t.Error("Capabilities is nil")
	}
}

func TestDetectInitReturnsKnownValue(t *testing.T) {
	got := detectInit()
	switch got {
	case "systemd", "openrc", "unknown":
	default:
		t.Errorf("detectInit() = %q, want one of systemd/openrc/unknown", got)
	}
}

func TestCapabilitiesMatchInit(t *testing.T) {
	cases := map[string]struct{ logs, service string }{
		"systemd": {"journald", "systemd"},
		"openrc":  {"file", "openrc"},
		"unknown": {"none", "none"},
	}
	for init, want := range cases {
		got := capabilities(init)
		if got["logs"] != want.logs {
			t.Errorf("capabilities(%q)[logs] = %q, want %q", init, got["logs"], want.logs)
		}
		if got["service"] != want.service {
			t.Errorf("capabilities(%q)[service] = %q, want %q", init, got["service"], want.service)
		}
	}
}

func TestHandleInfoGETReturnsJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
	w := httptest.NewRecorder()
	handleInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var info Info
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if info.AgentVersion != Version {
		t.Errorf("response AgentVersion = %q, want %q", info.AgentVersion, Version)
	}
}

func TestHandleInfoRejectsNonGET(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/v1/info", nil)
		w := httptest.NewRecorder()
		handleInfo(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, w.Code)
		}
	}
}
