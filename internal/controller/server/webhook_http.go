package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"strings"
)

// defaultWebhookPath is where the git-as-source push webhook mounts unless the
// operator overrides it in config.
const defaultWebhookPath = "/gitops/webhook"

// maxWebhookBody caps the payload we read for signature verification — GitHub
// push payloads are well under this; the limit just bounds memory.
const maxWebhookBody = 5 << 20 // 5 MiB

// GitOpsWebhook is the push-triggered reconcile endpoint. A GitHub (or
// compatible) push webhook POSTs here; after verifying the HMAC-SHA256
// signature it fires trigger, which does an immediate pull+reconcile so the
// controller converges without waiting for the poll interval.
//
// The response is returned before the reconcile finishes (202 Accepted) so the
// sender isn't held open for the git pull + apply — webhooks want a fast ack.
type GitOpsWebhook struct {
	path    string
	secret  []byte
	trigger func(context.Context) error
}

// NewGitOpsWebhook builds a webhook handler. An empty path defaults to
// /gitops/webhook. An empty secret disables signature verification (any POST is
// accepted — only safe on a trusted network); a non-empty secret requires a
// valid X-Hub-Signature-256 header. trigger is required.
func NewGitOpsWebhook(path, secret string, trigger func(context.Context) error) *GitOpsWebhook {
	if path == "" {
		path = defaultWebhookPath
	}
	return &GitOpsWebhook{path: path, secret: []byte(secret), trigger: trigger}
}

// register mounts the webhook on mux. No-op for a nil handler so callers can
// pass nil to disable, mirroring the OIDC handler.
func (h *GitOpsWebhook) register(mux *http.ServeMux) {
	if h == nil || h.trigger == nil {
		return
	}
	mux.Handle(h.path, h)
}

func (h *GitOpsWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if len(h.secret) > 0 && !validSignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Reconcile asynchronously so the sender gets a fast ack; the git pull +
	// apply can take seconds. Uses a background ctx so it isn't tied to the
	// request's lifecycle.
	go func() {
		if err := h.trigger(context.Background()); err != nil {
			log.Printf("gitops webhook: reconcile failed: %v", err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("reconcile triggered\n"))
}

// validSignature verifies GitHub's "sha256=<hex>" HMAC over the raw body.
// Constant-time compare avoids leaking the digest via timing.
func validSignature(secret, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}
