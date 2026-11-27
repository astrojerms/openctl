package apps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicReadinessRequiresSuccessfulHTTPNotLoginRedirect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		ready  bool
	}{
		{"healthy", http.StatusOK, true},
		{"redirect to login", http.StatusFound, false},
		{"application failure", http.StatusServiceUnavailable, false},
		{"access denied", http.StatusForbidden, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/login")
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			if ready := probePublic(context.Background(), server.URL); ready != tc.ready {
				t.Fatalf("HTTP %d reported ready=%v", tc.status, ready)
			}
		})
	}
}

func TestPublicReadinessHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if probePublic(ctx, "http://192.0.2.1:8096") {
		t.Fatal("cancelled probe reported ready")
	}
}
