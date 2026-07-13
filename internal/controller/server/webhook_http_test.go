package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func webhookReq(method, sig string, body []byte) *http.Request {
	r := httptest.NewRequest(method, defaultWebhookPath, bytes.NewReader(body))
	if sig != "" {
		r.Header.Set("X-Hub-Signature-256", sig)
	}
	return r
}

// serveAndWait runs the handler and reports whether the async trigger fired
// within a short window (it runs in a goroutine so a fast ack can return first).
func serveAndWait(t *testing.T, h *GitOpsWebhook, req *http.Request) (code int, triggered bool) {
	t.Helper()
	done := make(chan struct{}, 1)
	h.trigger = func(context.Context) error { done <- struct{}{}; return nil }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	select {
	case <-done:
		return w.Code, true
	case <-time.After(300 * time.Millisecond):
		return w.Code, false
	}
}

func TestGitOpsWebhookSignatureVerification(t *testing.T) {
	const secret = "s3cr3t"
	body := []byte(`{"ref":"refs/heads/main"}`)

	t.Run("valid signature triggers reconcile", func(t *testing.T) {
		h := NewGitOpsWebhook("", secret, nil)
		code, fired := serveAndWait(t, h, webhookReq(http.MethodPost, signBody(secret, body), body))
		if code != http.StatusAccepted {
			t.Errorf("code = %d, want 202", code)
		}
		if !fired {
			t.Error("valid signature should trigger reconcile")
		}
	})

	t.Run("bad signature rejected, no trigger", func(t *testing.T) {
		h := NewGitOpsWebhook("", secret, nil)
		code, fired := serveAndWait(t, h, webhookReq(http.MethodPost, signBody("wrong-secret", body), body))
		if code != http.StatusUnauthorized {
			t.Errorf("code = %d, want 401", code)
		}
		if fired {
			t.Error("bad signature must not trigger reconcile")
		}
	})

	t.Run("missing signature rejected when secret set", func(t *testing.T) {
		h := NewGitOpsWebhook("", secret, nil)
		code, fired := serveAndWait(t, h, webhookReq(http.MethodPost, "", body))
		if code != http.StatusUnauthorized || fired {
			t.Errorf("missing signature: code=%d fired=%v, want 401/false", code, fired)
		}
	})

	t.Run("non-POST rejected", func(t *testing.T) {
		h := NewGitOpsWebhook("", secret, nil)
		code, fired := serveAndWait(t, h, webhookReq(http.MethodGet, "", nil))
		if code != http.StatusMethodNotAllowed || fired {
			t.Errorf("GET: code=%d fired=%v, want 405/false", code, fired)
		}
	})

	t.Run("no secret accepts any POST", func(t *testing.T) {
		h := NewGitOpsWebhook("", "", nil)
		code, fired := serveAndWait(t, h, webhookReq(http.MethodPost, "", body))
		if code != http.StatusAccepted || !fired {
			t.Errorf("unsigned mode: code=%d fired=%v, want 202/true", code, fired)
		}
	})
}

func TestGitOpsWebhookRegisterMountsPath(t *testing.T) {
	mux := http.NewServeMux()
	NewGitOpsWebhook("/custom/hook", "", func(context.Context) error { return nil }).register(mux)

	// A nil handler and a trigger-less handler must not panic / mount.
	(*GitOpsWebhook)(nil).register(mux)
	(&GitOpsWebhook{path: "/x"}).register(mux)

	_, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, "/custom/hook", nil))
	if pattern == "" {
		t.Error("custom path should be mounted")
	}
	if _, p := mux.Handler(httptest.NewRequest(http.MethodPost, "/x", nil)); p != "" {
		t.Error("trigger-less handler should not have mounted /x")
	}
}
