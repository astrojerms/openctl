package agent

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogsHandlerReturnsBody(t *testing.T) {
	called := 0
	wantLines := 250
	handler := makeLogsHandler(func(lines int) (string, error) {
		called++
		if lines != wantLines {
			t.Errorf("fetcher got lines=%d, want %d", lines, wantLines)
		}
		return "line A\nline B\nline C\n", nil
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/logs/k3s?lines=250", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), "line B") {
		t.Errorf("body missing expected line: %q", body)
	}
	if called != 1 {
		t.Errorf("fetcher called %d times, want 1", called)
	}
}

func TestLogsHandlerDefaultLinesWhenOmitted(t *testing.T) {
	var seen int
	handler := makeLogsHandler(func(lines int) (string, error) {
		seen = lines
		return "", nil
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/logs/k3s", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if seen != 0 {
		t.Errorf("with no lines query, fetcher should get 0 (its own default), got %d", seen)
	}
}

func TestLogsHandlerRejectsNonGET(t *testing.T) {
	handler := makeLogsHandler(func(int) (string, error) { return "", nil })
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, "/v1/logs/k3s", nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", m, w.Code)
		}
	}
}

func TestLogsHandlerRejectsBadLinesParam(t *testing.T) {
	handler := makeLogsHandler(func(int) (string, error) { return "", nil })
	cases := []string{"abc", "-5", "1.5"}
	for _, val := range cases {
		req := httptest.NewRequest(http.MethodGet, "/v1/logs/k3s?lines="+val, nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("lines=%q: status = %d, want 400", val, w.Code)
		}
	}
}

func TestLogsHandlerSurfacesFetcherError(t *testing.T) {
	handler := makeLogsHandler(func(int) (string, error) {
		return "", errors.New("k3s service not present")
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/logs/k3s", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "k3s service not present") {
		t.Errorf("body missing fetcher error: %q", w.Body.String())
	}
}
