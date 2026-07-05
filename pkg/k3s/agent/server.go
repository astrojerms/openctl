package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"
)

type Options struct {
	Listen   string
	CertFile string
	KeyFile  string
	CAFile   string
}

type Server struct {
	opts Options
	srv  *http.Server
}

func New(opts Options) (*Server, error) {
	caData, err := os.ReadFile(opts.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		return nil, fmt.Errorf("parse CA: no PEM blocks found in %s", opts.CAFile)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/info", handleInfo)
	mux.HandleFunc("/v1/logs/k3s", makeLogsHandler(fetchK3sLogs))
	mux.HandleFunc("/v1/upgrade/k3s", makeUpgradeHandler(upgradeK3s))
	for _, action := range serviceActions {
		mux.HandleFunc("/v1/service/k3s/"+string(action), makeServiceHandler(action, controlK3s))
	}

	return &Server{
		opts: opts,
		srv: &http.Server{
			Addr:              opts.Listen,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			TLSConfig: &tls.Config{
				ClientCAs:  pool,
				ClientAuth: tls.RequireAndVerifyClientCert,
				MinVersion: tls.VersionTLS12,
			},
		},
	}, nil
}

func (s *Server) ListenAndServe() error {
	return s.srv.ListenAndServeTLS(s.opts.CertFile, s.opts.KeyFile)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(gatherInfo()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// makeServiceHandler returns an HTTP handler that runs `control(action)` on
// POST. Factored so tests inject a stub controller without touching systemd.
func makeServiceHandler(action ServiceAction, control func(ServiceAction) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := control(action); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// makeLogsHandler returns an HTTP handler that serves k3s logs from the given
// fetcher. Factored so tests can inject a stub fetcher and exercise the
// HTTP plumbing without shelling out to journalctl.
func makeLogsHandler(fetch func(int) (string, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		lines := 0 // 0 means "let fetcher pick its default"
		if l := r.URL.Query().Get("lines"); l != "" {
			parsed, err := strconv.Atoi(l)
			if err != nil || parsed < 0 {
				http.Error(w, "lines must be a non-negative integer", http.StatusBadRequest)
				return
			}
			lines = parsed
		}
		out, err := fetch(lines)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(out)) // #nosec G705 -- text/plain content type set; not interpreted as HTML
	}
}
