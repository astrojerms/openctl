package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// UpgradeRequest is the JSON body of POST /v1/upgrade/k3s.
type UpgradeRequest struct {
	// Version is a k3s release tag, e.g. "v1.30.5+k3s1".
	Version string `json:"version"`
}

// k3sReleaseBase is the GitHub releases download root for k3s binaries.
const k3sReleaseBase = "https://github.com/k3s-io/k3s/releases/download"

// versionRe constrains the target version so it is safe to interpolate into a
// download URL: a semver tag with a +k3sN build suffix. This is the injection
// guard — nothing else is ever placed in the URL path.
var versionRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+\+k3s[0-9]+$`)

// upgradeFunc performs a binary-swap upgrade to version. Injected so tests can
// exercise the HTTP plumbing without downloading or touching the real binary.
type upgradeFunc func(ctx context.Context, version string) error

// makeUpgradeHandler returns an HTTP handler that runs upgrade(version) on
// POST. The download + service restart can take minutes, so it extends this
// request's write deadline past the server's default WriteTimeout.
func makeUpgradeHandler(upgrade upgradeFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req UpgradeRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !versionRe.MatchString(req.Version) {
			http.Error(w, fmt.Sprintf("invalid version %q: want a tag like v1.30.5+k3s1", req.Version), http.StatusBadRequest)
			return
		}

		// A k3s binary is tens of MB; the whole request may outlast the
		// server's 30s WriteTimeout. Best-effort extend for this request only.
		if rc := http.NewResponseController(w); rc != nil {
			_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Minute))
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		if err := upgrade(ctx, req.Version); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// upgradeDeps are the seams runUpgrade needs, so the orchestration is testable
// without real network or root filesystem access.
type upgradeDeps struct {
	arch     string
	destPath string // installed k3s binary, e.g. /usr/local/bin/k3s
	baseURL  string // release download root (overridable in tests)
	fetch    func(ctx context.Context, url string) ([]byte, error)
	restart  func() error
}

// runUpgrade downloads the target k3s binary for the node's arch, verifies its
// published sha256, atomically swaps it into place, and restarts k3s.
func runUpgrade(ctx context.Context, version string, d upgradeDeps) error {
	if !versionRe.MatchString(version) {
		return fmt.Errorf("refusing to upgrade to unvalidated version %q", version)
	}
	asset, err := k3sAssetName(d.arch)
	if err != nil {
		return err
	}
	base := strings.TrimRight(d.baseURL, "/") + "/" + version + "/"

	sums, err := d.fetch(ctx, base+checksumAssetName(d.arch))
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}
	expected, err := parseSHA256(string(sums), asset)
	if err != nil {
		return err
	}

	bin, err := d.fetch(ctx, base+asset)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", asset, err)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(bin))
	if got != expected {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", asset, got, expected)
	}

	if err := installBinary(bin, d.destPath); err != nil {
		return err
	}
	if err := d.restart(); err != nil {
		return fmt.Errorf("restart k3s after upgrade: %w", err)
	}
	return nil
}

// k3sAssetName maps a GOARCH to the k3s release binary asset name.
func k3sAssetName(arch string) (string, error) {
	switch arch {
	case "amd64":
		return "k3s", nil
	case "arm64":
		return "k3s-arm64", nil
	case "arm":
		return "k3s-armhf", nil
	default:
		return "", fmt.Errorf("no k3s binary asset for arch %q", arch)
	}
}

// checksumAssetName maps a GOARCH to the k3s release checksum file name.
func checksumAssetName(arch string) string {
	switch arch {
	case "arm64":
		return "sha256sum-arm64.txt"
	case "arm":
		return "sha256sum-arm.txt"
	default:
		return "sha256sum-amd64.txt"
	}
}

// parseSHA256 finds the checksum for asset in a `sha256sum`-style file. Each
// line is "<hex>  <path>"; the path may be bare or "./"-prefixed. It matches
// the exact asset basename so "k3s" does not match "k3s-arm64".
func parseSHA256(sumsFile, asset string) (string, error) {
	for line := range strings.SplitSeq(sumsFile, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "./")
		if filepath.Base(name) == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum for %q in checksum file", asset)
}

// installBinary writes data to a temp file in destPath's directory, makes it
// executable, then atomically renames it over destPath. Same-dir temp keeps
// the rename atomic (no cross-device copy).
func installBinary(data []byte, destPath string) error {
	dir := filepath.Dir(destPath)
	tmp, err := os.CreateTemp(dir, ".k3s-upgrade-*")
	if err != nil {
		return fmt.Errorf("create temp binary: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp binary: %w", err)
	}
	// #nosec G302,G703 -- an executable must be 0755; tmpName is OS-generated by
	// os.CreateTemp and destPath is package-controlled (k3sBinaryPath: LookPath
	// or a fixed constant), never user input, so gosec's taint flag is a false
	// positive.
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("chmod temp binary: %w", err)
	}
	// #nosec G703 -- tmpName/destPath are not user-controlled (see above).
	if err := os.Rename(tmpName, destPath); err != nil {
		return fmt.Errorf("swap binary into %s: %w", destPath, err)
	}
	return nil
}

// httpFetch downloads a URL and returns its body. Used by the production
// upgrade path; tests inject their own fetcher.
func httpFetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G107 -- url is built from k3sReleaseBase + a regex-validated version
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20)) // 512 MiB cap
}

// k3sBinaryPath resolves the installed k3s binary, falling back to the
// well-known install location.
func k3sBinaryPath() string {
	if p, err := exec.LookPath("k3s"); err == nil && p != "" {
		return p
	}
	return "/usr/local/bin/k3s"
}

// upgradeK3s is the production upgradeFunc wired into the server.
func upgradeK3s(ctx context.Context, version string) error {
	return runUpgrade(ctx, version, upgradeDeps{
		arch:     runtime.GOARCH,
		destPath: k3sBinaryPath(),
		baseURL:  k3sReleaseBase,
		fetch:    httpFetch,
		restart:  func() error { return controlK3s(ServiceRestart) },
	})
}
