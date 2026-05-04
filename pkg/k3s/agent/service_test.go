package agent

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServiceHandlerSuccessReturns204(t *testing.T) {
	var got ServiceAction
	handler := makeServiceHandler(ServiceRestart, func(a ServiceAction) error {
		got = a
		return nil
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/service/k3s/restart", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if got != ServiceRestart {
		t.Errorf("controller got action %q, want %q", got, ServiceRestart)
	}
}

func TestServiceHandlerSurfacesControllerError(t *testing.T) {
	handler := makeServiceHandler(ServiceStart, func(ServiceAction) error {
		return errors.New("k3s.service: not loaded")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/service/k3s/start", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "k3s.service: not loaded") {
		t.Errorf("body missing controller error: %q", w.Body.String())
	}
}

func TestServiceHandlerRejectsNonPOST(t *testing.T) {
	handler := makeServiceHandler(ServiceStop, func(ServiceAction) error { return nil })
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, "/v1/service/k3s/stop", nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", m, w.Code)
		}
	}
}

func TestServiceActionsCoversAllVerbs(t *testing.T) {
	want := map[ServiceAction]bool{ServiceStart: false, ServiceStop: false, ServiceRestart: false}
	for _, a := range serviceActions {
		want[a] = true
	}
	for a, found := range want {
		if !found {
			t.Errorf("serviceActions missing %q", a)
		}
	}
}
