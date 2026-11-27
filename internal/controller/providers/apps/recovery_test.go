package apps

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func targetSpec(target Target) map[string]any {
	return map[string]any{
		"host": target.Host, "port": target.Port, "user": target.User,
		"privateKeyFile": target.PrivateKeyFile, "knownHostsFile": target.KnownHostsFile,
	}
}

func TestFreshLocatorRemovedOnlyBeforeFirstCommand(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failAt      int
		failure     error
		preexisting bool
		wantRetain  bool
	}{
		{"typed first claim", 1, &preCommandError{err: errors.New("no command")}, false, false},
		{"wrapped typed first claim", 1, errors.Join(errors.New("transport"), &preCommandError{err: errors.New("no command")}), false, false},
		{"untyped first claim", 1, errors.New("connection failed"), false, true},
		{"remote first claim", 1, errors.New("remote command exited with status 1"), false, true},
		{"typed project claim after directory executed", 2, &preCommandError{err: errors.New("no command")}, false, true},
		{"typed existing binding", 1, &preCommandError{err: errors.New("no command")}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, state := testProvider()
			r := composeResource()
			if tc.preexisting {
				_, l, err := parse(r, true)
				if err != nil {
					t.Fatal(err)
				}
				if err := p.save(context.Background(), r.Kind, r.Metadata.Name, l); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			p.runner = fakeRunner(func(context.Context, Target, string, []byte) (string, error) {
				calls++
				if len(state.values) == 0 {
					t.Fatal("first remote claim ran before locator persistence")
				}
				if calls == tc.failAt {
					return "", tc.failure
				}
				return "", nil
			})
			_, err := p.Apply(context.Background(), r)
			if err == nil || calls != tc.failAt {
				t.Fatalf("unexpected apply result: calls=%d err=%v", calls, err)
			}
			if (len(state.values) != 0) != tc.wantRetain {
				t.Fatalf("locator retention=%v, want %v", len(state.values) != 0, tc.wantRetain)
			}
			if tc.wantRetain && !strings.Contains(err.Error(), "locator retained") {
				t.Fatalf("retained locator needs actionable recovery diagnostic: %v", err)
			}
			if !tc.wantRetain {
				r.Spec["target"].(map[string]any)["host"] = "corrected.example.test"
				p.runner = fakeRunner(func(context.Context, Target, string, []byte) (string, error) {
					return "5.8.1", nil
				})
				if _, err := p.DryRun(context.Background(), r); err != nil {
					t.Fatalf("safe failed first connection pinned typo placement: %v", err)
				}
			}
		})
	}
}

func TestRealSSHLocalFailuresReleaseFreshLocator(t *testing.T) {
	for _, name := range []string{"missing key", "invalid key", "missing known hosts", "cancelled dial"} {
		t.Run(name, func(t *testing.T) {
			p, state := testProvider()
			target, _ := sshTestIdentity(t)
			ctx := context.Background()
			switch name {
			case "missing key":
				target.PrivateKeyFile += ".missing"
			case "invalid key":
				if err := os.WriteFile(target.PrivateKeyFile, []byte("not a private key"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cancelled dial":
				if err := os.WriteFile(target.KnownHostsFile, nil, 0600); err != nil {
					t.Fatal(err)
				}
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			r := composeResource()
			r.Spec["target"] = targetSpec(target)
			_, err := p.Apply(ctx, r)
			var before *preCommandError
			if !errors.As(err, &before) || !strings.Contains(err.Error(), "fresh locator removed") {
				t.Fatalf("real runner lost no-command evidence: %v", err)
			}
			if len(state.values) != 0 {
				t.Fatal("local SSH setup failure left permanent placement binding")
			}
		})
	}
}

func TestRealSSHHandshakeFailureReleasesFreshLocator(t *testing.T) {
	target, _ := sshTestIdentity(t)
	if err := os.WriteFile(target.KnownHostsFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	target.Port = listener.Addr().(*net.TCPAddr).Port
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close() // Disconnect during handshake, before any exec.
		}
	}()
	p, state := testProvider()
	r := composeResource()
	r.Spec["target"] = targetSpec(target)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = p.Apply(ctx, r)
	var before *preCommandError
	if !errors.As(err, &before) || len(state.values) != 0 || !strings.Contains(err.Error(), "fresh locator removed") {
		t.Fatalf("handshake-only failure pinned initial placement: %v", err)
	}
	<-done
}

type failedCleanupState struct{ *memoryState }

func (s failedCleanupState) DeleteState(context.Context, string, string, string) error {
	return errors.New("state store unavailable")
}

func TestFreshLocatorCleanupFailureRemainsBoundAndActionable(t *testing.T) {
	p, state := testProvider()
	p.store = failedCleanupState{state}
	p.runner = fakeRunner(func(context.Context, Target, string, []byte) (string, error) {
		return "", &preCommandError{err: errors.New("no command")}
	})
	_, err := p.Apply(context.Background(), composeResource())
	if err == nil || !strings.Contains(err.Error(), "removing the fresh locator failed") || len(state.values) != 1 {
		t.Fatalf("cleanup failure must retain binding and explain recovery: %v", err)
	}
}
