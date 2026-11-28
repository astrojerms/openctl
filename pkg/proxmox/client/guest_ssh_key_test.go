package client

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

func guestHostPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	key, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func guestHostStatus(t *testing.T, output string) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"data": map[string]any{"exited": 1, "exitcode": 0, "out-truncated": 0, "out-data": output}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestGetVMSSHHostKeyReturnsActualKey(t *testing.T) {
	key := guestHostPublicKey(t)
	var submissions, polls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=fixture@pam!read=secret" {
			t.Error("missing API authorization")
		}
		switch r.URL.Path {
		case "/api2/json/nodes/pve1/qemu/101/agent/exec":
			submissions.Add(1)
			if r.Method != http.MethodPost || r.ParseForm() != nil {
				t.Error("expected exec POST form")
			}
			command := r.PostForm["command"]
			if len(r.PostForm) != 1 || len(command) != 2 || command[0] != "/bin/cat" || command[1] != "/etc/ssh/ssh_host_ed25519_key.pub" {
				t.Errorf("unexpected fixed read request: %v", r.PostForm)
			}
			w.Write([]byte(`{"data":{"pid":42}}`))
		case "/api2/json/nodes/pve1/qemu/101/agent/exec-status":
			if r.Method != http.MethodGet || r.URL.RawQuery != "pid=42" {
				t.Error("unexpected status request")
			}
			if polls.Add(1) == 1 {
				w.Write([]byte(`{"data":{"exited":0}}`))
				return
			}
			w.Write([]byte(guestHostStatus(t, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))+" root@guest\n")))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := trustedTestClient(t, server, "fixture@pam!read", "secret")
	actual, err := client.GetVMSSHHostKey(context.Background(), "pve1", 101)
	if err != nil || actual == nil || string(actual.Marshal()) != string(key.Marshal()) {
		t.Fatalf("actual public key = %v, err = %v", actual, err)
	}
	if submissions.Load() != 1 || polls.Load() != 2 {
		t.Fatalf("exec=%d polls=%d", submissions.Load(), polls.Load())
	}
}

func TestGetVMSSHHostKeyAcceptsOmittedFalseTruncationFlags(t *testing.T) {
	key := guestHostPublicKey(t)
	status, err := json.Marshal(map[string]any{"data": map[string]any{
		"exited": 1, "exitcode": 0, "out-data": string(ssh.MarshalAuthorizedKey(key)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Write([]byte(`{"data":{"pid":42}}`))
			return
		}
		w.Write(status)
	}))
	defer server.Close()
	client := trustedTestClient(t, server, "", "")
	actual, err := client.GetVMSSHHostKey(context.Background(), "pve1", 101)
	if err != nil || actual == nil || string(actual.Marshal()) != string(key.Marshal()) {
		t.Fatalf("valid QEMU output with omitted optional flags was rejected: %v", err)
	}
}

func TestGetVMSSHHostKeyRejectsUncertainOrInvalidExecution(t *testing.T) {
	key := string(ssh.MarshalAuthorizedKey(guestHostPublicKey(t)))
	for _, tc := range []struct {
		name, submit, status string
		httpStatus           int
	}{
		{name: "missing pid", submit: `{"data":{}}`},
		{name: "unknown pid field", submit: `{"data":{"pid":42,"extra":true}}`},
		{name: "null pid", submit: `{"data":{"pid":null}}`},
		{name: "string pid", submit: `{"data":{"pid":"42"}}`},
		{name: "fractional pid", submit: `{"data":{"pid":42.5}}`},
		{name: "negative pid", submit: `{"data":{"pid":-42}}`},
		{name: "zero pid", submit: `{"data":{"pid":0}}`},
		{name: "duplicate pid", submit: `{"data":{"pid":42,"pid":43}}`},
		{name: "unknown envelope", submit: `{"data":{"pid":42},"errors":"POISON"}`},
		{name: "malformed submit", submit: `{"data":`},
		{name: "oversized submit", submit: strings.Repeat("x", guestExecResponseLimit+1)},
		{name: "submit denied", submit: "POISON", httpStatus: http.StatusForbidden},
		{name: "missing exited", status: `{"data":{"exitcode":0}}`},
		{name: "null exited", status: `{"data":{"exited":null}}`},
		{name: "invalid exited", status: `{"data":{"exited":2}}`},
		{name: "string exited", status: `{"data":{"exited":"1"}}`},
		{name: "unknown status", status: `{"data":{"exited":1,"unknown":"POISON"}}`},
		{name: "duplicate status", status: `{"data":{"exited":0,"exited":1}}`},
		{name: "running with output", status: `{"data":{"exited":0,"out-data":"POISON"}}`},
		{name: "missing exitcode", status: `{"data":{"exited":1,"out-truncated":0,"out-data":"POISON"}}`},
		{name: "nonzero exitcode", status: `{"data":{"exited":1,"exitcode":1,"out-truncated":0,"out-data":"POISON"}}`},
		{name: "invalid exitcode", status: `{"data":{"exited":1,"exitcode":"0","out-truncated":0}}`},
		{name: "signalled", status: `{"data":{"exited":1,"exitcode":0,"signal":9}}`},
		{name: "null truncation", status: `{"data":{"exited":1,"exitcode":0,"out-truncated":null}}`},
		{name: "truncated", status: `{"data":{"exited":1,"exitcode":0,"out-truncated":1}}`},
		{name: "missing stdout", status: `{"data":{"exited":1,"exitcode":0,"out-truncated":0}}`},
		{name: "invalid stdout", status: `{"data":{"exited":1,"exitcode":0,"out-truncated":0,"out-data":42}}`},
		{name: "stderr", status: `{"data":{"exited":1,"exitcode":0,"out-truncated":0,"err-data":"POISON"}}`},
		{name: "truncated stderr", status: `{"data":{"exited":1,"exitcode":0,"out-truncated":0,"err-truncated":true}}`},
		{name: "oversized output", status: guestHostStatus(t, strings.Repeat("x", guestSSHKeyLimit+1))},
		{name: "invalid key blob", status: guestHostStatus(t, "ssh-ed25519 aW52YWxpZA==\n")},
		{name: "two keys", status: guestHostStatus(t, key+key)},
		{name: "authorized key options", status: guestHostStatus(t, `command="POISON" `+key)},
		{name: "leading garbage", status: guestHostStatus(t, "POISON\n"+key)},
		{name: "certificate key", status: guestHostStatus(t, "ssh-ed25519-cert-v01@openssh.com aW52YWxpZA==\n")},
		{name: "malformed status", status: "POISON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var submissions, polls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					submissions.Add(1)
					if tc.httpStatus != 0 {
						w.WriteHeader(tc.httpStatus)
					}
					if tc.submit != "" {
						w.Write([]byte(tc.submit))
					} else {
						w.Write([]byte(`{"data":{"pid":42}}`))
					}
					return
				}
				polls.Add(1)
				w.Write([]byte(tc.status))
			}))
			defer server.Close()
			client := trustedTestClient(t, server, "", "")
			actual, err := client.GetVMSSHHostKey(context.Background(), "pve1", 101)
			if actual != nil || err == nil || strings.Contains(err.Error(), "POISON") || !strings.Contains(err.Error(), "guest agent") || !strings.Contains(err.Error(), "VM.Monitor") {
				t.Fatalf("unsafe or missing rejection: key=%v err=%v", actual, err)
			}
			if submissions.Load() != 1 || polls.Load() > 1 {
				t.Fatalf("uncertain operation retried: exec=%d polls=%d", submissions.Load(), polls.Load())
			}
		})
	}
}

func TestGetVMSSHHostKeyCancellationNeverResubmits(t *testing.T) {
	for _, cancelAt := range []string{"before submission", "submission", "status", "poll wait"} {
		t.Run(cancelAt, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var submissions, polls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					submissions.Add(1)
					if cancelAt == "submission" {
						cancel()
					}
					w.Write([]byte(`{"data":{"pid":42}}`))
					return
				}
				polls.Add(1)
				if cancelAt == "status" {
					cancel()
				}
				w.Write([]byte(`{"data":{"exited":0}}`))
				if cancelAt == "poll wait" {
					w.(http.Flusher).Flush()
					cancel()
				}
			}))
			defer server.Close()
			client := trustedTestClient(t, server, "", "")
			if cancelAt == "before submission" {
				cancel()
			}
			key, err := client.GetVMSSHHostKey(ctx, "pve1", 101)
			if key != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation emitted key or lost cancellation: %v %v", key, err)
			}
			if submissions.Load() > 1 || polls.Load() > 1 || (cancelAt == "before submission" && submissions.Load() != 0) {
				t.Fatalf("cancelled operation resubmitted: exec=%d polls=%d", submissions.Load(), polls.Load())
			}
		})
	}
}

func TestGetVMSSHHostKeyRequiresVerifiedTLS(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	client, err := New(server.URL, "", "", TLSOptions{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	key, err := client.GetVMSSHHostKey(context.Background(), "pve1", 101)
	if key != nil || err == nil || requests.Load() != 0 {
		t.Fatalf("unverified TLS allowed: key=%v err=%v requests=%d", key, err, requests.Load())
	}
}
