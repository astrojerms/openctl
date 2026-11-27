package apps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type commandRunner interface {
	Run(context.Context, Target, string, []byte) (string, error)
}

type sshRunner struct{}

// preCommandError is evidence that this invocation never submitted an SSH exec
// request. Errors from Run/Start are deliberately outside this boundary: even a
// lost response can mean the command executed remotely.
type preCommandError struct{ err error }

func (e *preCommandError) Error() string { return e.err.Error() }
func (e *preCommandError) Unwrap() error { return e.err }

func expandHome(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// Run follows the existing x/crypto SSH machinery, but never trusts unknown keys
// and closes the transport on cancellation (including during the handshake).
func (sshRunner) Run(ctx context.Context, t Target, command string, input []byte) (out string, err error) {
	commandRequested := false
	defer func() {
		if err != nil && !commandRequested {
			err = &preCommandError{err: err}
		}
	}()
	keyPath, err := expandHome(t.PrivateKeyFile)
	if err != nil {
		return "", fmt.Errorf("apps: cannot resolve SSH key path")
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("apps: cannot read target privateKeyFile")
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("apps: cannot parse target private key (unencrypted controller-side key required)")
	}
	hostsPath, err := expandHome(t.KnownHostsFile)
	if err != nil {
		return "", fmt.Errorf("apps: cannot resolve known_hosts path")
	}
	verify, err := knownhosts.New(hostsPath)
	if err != nil {
		return "", fmt.Errorf("apps: cannot load knownHostsFile; install a verified SSH host key before applying")
	}
	address := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	conn, err := (&net.Dialer{Timeout: 20 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return "", fmt.Errorf("apps: SSH target connection failed: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	cc, chans, reqs, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: t.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: verify})
	if err != nil {
		return "", fmt.Errorf("apps: SSH handshake failed; verify host key, user and key authorization: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("apps: cannot open SSH command session")
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(input)
	var output boundedOutput
	session.Stdout = &output
	// Remote stderr may contain Compose interpolation values, URLs or passwords.
	session.Stderr = io.Discard
	command = "set -euo pipefail; " + command
	if t.Sudo {
		command = "sudo -n -- bash -c " + quote(command)
	} else {
		command = "bash -c " + quote(command)
	}
	commandRequested = true
	err = session.Run(command)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		if exit, ok := err.(*ssh.ExitError); ok {
			return "", fmt.Errorf("apps: remote command exited with status %d; check guest prerequisites for this lifecycle stage (raw output withheld to protect credentials)", exit.ExitStatus())
		}
		return "", fmt.Errorf("apps: remote command failed; check guest prerequisites for this lifecycle stage")
	}
	if output.truncated {
		return "", fmt.Errorf("apps: remote output exceeded 1 MiB")
	}
	return output.String(), nil
}

type boundedOutput struct {
	bytes.Buffer
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 1024*1024 - b.Len()
	if len(p) > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func (p *Provider) run(ctx context.Context, l locator, stage, command string, input []byte) (string, error) {
	out, err := p.runner.Run(ctx, l.Target, command, input)
	if err != nil {
		var before *preCommandError
		noCommand := errors.As(err, &before)
		// Never interpolate remote output or arbitrary runner errors: neither is
		// a trusted, secret-free diagnostic channel. Preserve typed boundary
		// evidence even when cancellation also explains the transport failure.
		var diagnostic error
		if _, ok := p.runner.(sshRunner); ok {
			diagnostic = fmt.Errorf("apps: %s: %w", stage, err)
		} else {
			diagnostic = fmt.Errorf("apps: %s failed", stage)
		}
		if ctx.Err() != nil {
			diagnostic = fmt.Errorf("apps: %s: %w", stage, ctx.Err())
		}
		if noCommand {
			return "", &preCommandError{err: diagnostic}
		}
		return "", diagnostic
	}
	return out, nil
}

func (p *Provider) upload(ctx context.Context, l locator, file string, data []byte) error {
	// stdin carries file contents, so neither passwords nor Compose env values
	// become SSH command arguments. Atomic rename leaves the previous file intact
	// if upload is interrupted. Refuse symlinks to avoid escaping the directory.
	_, err := p.run(ctx, l, "upload application file", "set -eu; umask 077; test ! -L "+quote(file)+"; tmp=$(mktemp "+quote(file+".openctl.XXXXXX")+"); trap 'rm -f -- \"$tmp\"' EXIT; cat > \"$tmp\"; chmod 0600 \"$tmp\"; mv -f -- \"$tmp\" "+quote(file), data)
	return err
}
