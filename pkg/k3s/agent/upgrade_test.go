package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256hex(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func TestK3sAssetName(t *testing.T) {
	cases := map[string]string{"amd64": "k3s", "arm64": "k3s-arm64", "arm": "k3s-armhf"}
	for arch, want := range cases {
		got, err := k3sAssetName(arch)
		if err != nil || got != want {
			t.Errorf("k3sAssetName(%q) = %q, %v; want %q", arch, got, err, want)
		}
	}
	if _, err := k3sAssetName("riscv64"); err == nil {
		t.Error("expected error for unsupported arch")
	}
}

func TestParseSHA256(t *testing.T) {
	// Real k3s checksum files list several assets; "k3s" must not match
	// "k3s-arm64" or "k3s-airgap-images".
	sums := "abc123  k3s-arm64\n" +
		"deadbeef  ./k3s\n" +
		"999  k3s-airgap-images-arm64.tar\n"
	got, err := parseSHA256(sums, "k3s")
	if err != nil {
		t.Fatalf("parseSHA256: %v", err)
	}
	if got != "deadbeef" {
		t.Errorf("got %q, want deadbeef (must not match k3s-arm64)", got)
	}

	got, err = parseSHA256(sums, "k3s-arm64")
	if err != nil || got != "abc123" {
		t.Errorf("k3s-arm64: got %q, %v", got, err)
	}

	if _, err := parseSHA256(sums, "k3s-armhf"); err == nil {
		t.Error("expected error when asset absent")
	}
}

func TestInstallBinary_AtomicSwapOverExisting(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "k3s")
	if err := os.WriteFile(dest, []byte("OLD BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}

	newData := []byte("NEW BINARY CONTENT")
	if err := installBinary(newData, dest); err != nil {
		t.Fatalf("installBinary: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newData) {
		t.Errorf("dest content = %q, want the new binary", got)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("dest mode = %v, want executable", info.Mode().Perm())
	}
	// No temp files should be left behind in the directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".k3s-upgrade-") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
}

// fakeRelease serves a checksum file + binary for one asset, computing the
// checksum from the binary bytes so the happy path verifies end to end.
func fakeReleaseDeps(t *testing.T, arch string, binary []byte, restart func() error) upgradeDeps {
	t.Helper()
	asset, err := k3sAssetName(arch)
	if err != nil {
		t.Fatal(err)
	}
	sumFile := sha256hex(binary) + "  " + asset + "\n"
	fetch := func(_ context.Context, url string) ([]byte, error) {
		switch {
		case strings.HasSuffix(url, checksumAssetName(arch)):
			return []byte(sumFile), nil
		case strings.HasSuffix(url, "/"+asset):
			return binary, nil
		default:
			return nil, fmt.Errorf("unexpected url %q", url)
		}
	}
	return upgradeDeps{
		arch:     arch,
		destPath: filepath.Join(t.TempDir(), "k3s"),
		baseURL:  "https://example.test/download",
		fetch:    fetch,
		restart:  restart,
	}
}

func TestRunUpgrade_Success(t *testing.T) {
	binary := []byte("pretend-k3s-binary")
	restarted := false
	d := fakeReleaseDeps(t, "amd64", binary, func() error { restarted = true; return nil })

	if err := runUpgrade(context.Background(), "v1.30.5+k3s1", d); err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	got, err := os.ReadFile(d.destPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Errorf("installed binary = %q, want the downloaded bytes", got)
	}
	if !restarted {
		t.Error("k3s was not restarted after the swap")
	}
}

func TestRunUpgrade_ChecksumMismatch(t *testing.T) {
	d := fakeReleaseDeps(t, "amd64", []byte("real"), func() error { return nil })
	// Override fetch so the binary bytes differ from what the checksum covers.
	orig := d.fetch
	d.fetch = func(ctx context.Context, url string) ([]byte, error) {
		if strings.HasSuffix(url, "/k3s") {
			return []byte("TAMPERED"), nil
		}
		return orig(ctx, url)
	}
	restarted := false
	d.restart = func() error { restarted = true; return nil }

	err := runUpgrade(context.Background(), "v1.30.5+k3s1", d)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch error, got %v", err)
	}
	if restarted {
		t.Error("must NOT restart k3s when the checksum fails")
	}
	if _, statErr := os.Stat(d.destPath); statErr == nil {
		t.Error("must NOT install the binary when the checksum fails")
	}
}

func TestRunUpgrade_FetchChecksumError(t *testing.T) {
	d := fakeReleaseDeps(t, "amd64", []byte("x"), func() error { return nil })
	d.fetch = func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("404 not found")
	}
	if err := runUpgrade(context.Background(), "v1.30.5+k3s1", d); err == nil {
		t.Fatal("expected error when the release cannot be fetched")
	}
}

func TestRunUpgrade_RestartError(t *testing.T) {
	binary := []byte("bin")
	d := fakeReleaseDeps(t, "arm64", binary, func() error { return errors.New("systemctl failed") })
	err := runUpgrade(context.Background(), "v1.31.0+k3s1", d)
	if err == nil || !strings.Contains(err.Error(), "restart k3s") {
		t.Fatalf("want restart error, got %v", err)
	}
	// The binary should still have been swapped before the restart failed.
	if _, statErr := os.Stat(d.destPath); statErr != nil {
		t.Error("binary should be installed even though restart failed")
	}
}

func TestRunUpgrade_RejectsBadVersion(t *testing.T) {
	d := fakeReleaseDeps(t, "amd64", []byte("x"), func() error { return nil })
	if err := runUpgrade(context.Background(), "latest", d); err == nil {
		t.Error("expected refusal for an unvalidated version")
	}
}

func TestUpgradeHandler(t *testing.T) {
	t.Run("rejects GET", func(t *testing.T) {
		h := makeUpgradeHandler(func(context.Context, string) error { return nil })
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/v1/upgrade/k3s", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("code = %d, want 405", rec.Code)
		}
	})

	t.Run("rejects bad version", func(t *testing.T) {
		called := false
		h := makeUpgradeHandler(func(context.Context, string) error { called = true; return nil })
		body, _ := json.Marshal(UpgradeRequest{Version: "v1.30; rm -rf"})
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/v1/upgrade/k3s", bytes.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("code = %d, want 400", rec.Code)
		}
		if called {
			t.Error("upgrade must not run for an invalid version")
		}
	})

	t.Run("runs upgrade on valid request", func(t *testing.T) {
		var gotVersion string
		h := makeUpgradeHandler(func(_ context.Context, v string) error { gotVersion = v; return nil })
		body, _ := json.Marshal(UpgradeRequest{Version: "v1.30.5+k3s1"})
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/v1/upgrade/k3s", bytes.NewReader(body)))
		if rec.Code != http.StatusNoContent {
			t.Errorf("code = %d, want 204", rec.Code)
		}
		if gotVersion != "v1.30.5+k3s1" {
			t.Errorf("upgrade got version %q", gotVersion)
		}
	})

	t.Run("surfaces upgrade failure as 500", func(t *testing.T) {
		h := makeUpgradeHandler(func(context.Context, string) error { return errors.New("boom") })
		body, _ := json.Marshal(UpgradeRequest{Version: "v1.30.5+k3s1"})
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/v1/upgrade/k3s", bytes.NewReader(body)))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("code = %d, want 500", rec.Code)
		}
	})
}
