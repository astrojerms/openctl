package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/instance"
	"github.com/openctl/openctl/internal/controller/storage"
)

type fixture struct {
	root, state, archive string
	db                   *sql.DB
}

func newFixture(t *testing.T, separate bool) fixture {
	t.Helper()
	base := t.TempDir()
	f := fixture{root: filepath.Join(base, "original"), archive: filepath.Join(base, "backup.tar")}
	f.state = filepath.Join(f.root, "controller")
	if separate {
		f.state = filepath.Join(base, "separate-state")
	}
	for _, dir := range []string{f.root, f.state, filepath.Join(f.root, "secrets"), filepath.Join(f.state, "tls")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{
		filepath.Join(f.root, "config.yaml"):         "sshKey: /external/ssh-key\nmanifestDir: /external/manifests\n",
		filepath.Join(f.root, "secrets", "password"): "test-sensitive-secret",
		filepath.Join(f.state, "token"):              "test-root-token",
		filepath.Join(f.state, "users.json"):         "{\"operator\":\"password-hash\"}",
		filepath.Join(f.state, "tls", "server.key"):  "test-private-key",
	} {
		if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	f.db, err = storage.Open(context.Background(), filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	for _, statement := range []string{
		"PRAGMA wal_autocheckpoint=0",
		"INSERT INTO controller_meta(key,value) VALUES ('controller_id','durable-controller-id')",
		"INSERT INTO provider_identities VALUES ('vm1','home','https://pve.example:8006','node1',123,'persistent-vm-identity')",
		"INSERT INTO provider_state VALUES ('test/v1','VM','vm1',X'000102FF',X'FEED',7,'2026-01-01T00:00:00Z')",
		"INSERT INTO operations(id,type,api_version,kind,resource_name,status,submitted_at) VALUES ('operation-1','apply','test/v1','VM','vm1','pending','2026-01-01T00:00:00Z')",
	} {
		if _, err := f.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := os.Stat(filepath.Join(f.state, "state.db-wal")); err != nil || info.Size() == 0 {
		t.Fatal("fixture must have uncheckpointed WAL")
	}
	return f
}

func treeBytes(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == instance.Filename {
			return nil
		}
		b, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, name)
		out[rel] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRoundTripIncludesWALAndIdentityWithoutSourceMutation(t *testing.T) {
	for _, sourceSeparate := range []bool{false, true} {
		for _, targetSeparate := range []bool{false, true} {
			name := "nested"
			if sourceSeparate {
				name = "external"
			}
			if targetSeparate {
				name += "-to-external"
			} else {
				name += "-to-nested"
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t, sourceSeparate)
				beforeRoot, beforeState := treeBytes(t, f.root), treeBytes(t, f.state)
				if _, err := Backup(context.Background(), Options{Root: f.root, StateDir: f.state, Archive: f.archive}); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(beforeRoot, treeBytes(t, f.root)) || !reflect.DeepEqual(beforeState, treeBytes(t, f.state)) {
					t.Fatal("backup changed source database, WAL, SHM, or other source data")
				}
				info, err := os.Stat(f.archive)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatal("archive must be private 0600")
				}
				root := filepath.Join(t.TempDir(), "restored")
				state := ""
				if targetSeparate {
					state = filepath.Join(t.TempDir(), "restored-state")
				}
				result, err := Restore(context.Background(), Options{Root: root, StateDir: state, Archive: f.archive})
				if err != nil {
					t.Fatal(err)
				}
				if result.OriginalRoot != f.root || result.OriginalStateDir != f.state {
					t.Fatal("original locations lost")
				}
				if err := storage.ValidateSnapshot(context.Background(), filepath.Join(result.StateDir, "state.db")); err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", filepath.Join(result.StateDir, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				for query, want := range map[string]string{
					"SELECT value FROM controller_meta WHERE key='controller_id'":                             "durable-controller-id",
					"SELECT identity FROM provider_identities WHERE name='vm1'":                               "persistent-vm-identity",
					"SELECT hex(state_blob)||':'||hex(private_blob)||':'||schema_version FROM provider_state": "000102FF:FEED:7",
					"SELECT status FROM operations WHERE id='operation-1'":                                    "pending",
				} {
					var got string
					if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
						t.Fatalf("persisted data mismatch: got %q want %q err=%v", got, want, err)
					}
				}
				for rel, want := range beforeState {
					if strings.HasPrefix(rel, "state.db") {
						continue
					}
					got, err := os.ReadFile(filepath.Join(result.StateDir, rel))
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("state file lost: %s: %v", rel, err)
					}
				}
				got, err := os.ReadFile(filepath.Join(root, "config.yaml"))
				if err != nil || !bytes.Equal(got, beforeRoot["config.yaml"]) {
					t.Fatal("configured paths were rewritten")
				}
				for _, transient := range []string{instance.Filename, "state.db-wal", "state.db-shm"} {
					// Inspect the archive, not the reopened DB (opening SQLite can create sidecars).
					for _, e := range readArchive(t, f.archive).meta.Entries {
						if e.Path == "state/"+transient {
							t.Fatalf("archived transient file %s", transient)
						}
					}
				}
			})
		}
	}
}

func TestBackupRefusesActiveControllerAndExistingOutput(t *testing.T) {
	f := newFixture(t, false)
	lock, err := instance.Acquire(f.state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(context.Background(), Options{Root: f.root, Archive: f.archive}); err == nil {
		t.Fatal("backup accepted active controller lock")
	}
	if _, err := os.Stat(f.archive); !os.IsNotExist(err) {
		t.Fatal("failed backup left output")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.archive, []byte("existing archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(context.Background(), Options{Root: f.root, Archive: f.archive}); err == nil {
		t.Fatal("overwrote existing output")
	}
	got, err := os.ReadFile(f.archive)
	if err != nil || string(got) != "existing archive" {
		t.Fatal("existing archive changed")
	}
}

type archiveData struct {
	meta    metadata
	headers []*tar.Header
	bodies  [][]byte
}

func readArchive(t *testing.T, name string) archiveData {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	if _, err := tr.Next(); err != nil {
		t.Fatal(err)
	}
	var a archiveData
	body, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &a.meta); err != nil {
		t.Fatal(err)
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		a.headers = append(a.headers, h)
		a.bodies = append(a.bodies, body)
	}
	return a
}

func writeArchive(t *testing.T, name string, a archiveData) {
	t.Helper()
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	body, err := json.Marshal(a.meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: metadataName, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	for i, h := range a.headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write(a.bodies[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRejectsMaliciousArchivesBeforePublication(t *testing.T) {
	f := newFixture(t, false)
	if _, err := Backup(context.Background(), Options{Root: f.root, Archive: f.archive}); err != nil {
		t.Fatal(err)
	}
	for _, attack := range []string{"traversal", "absolute", "symlink", "hardlink", "device", "checksum", "sqlite", "schema", "duplicate", "oversize", "version", "truncated", "truncated-trailer", "trailing"} {
		t.Run(attack, func(t *testing.T) {
			a := readArchive(t, f.archive)
			index := -1
			for i, e := range a.meta.Entries {
				if e.Path == "state/state.db" {
					index = i
				}
			}
			if index < 0 {
				t.Fatal("missing fixture DB")
			}
			switch attack {
			case "traversal":
				a.meta.Entries[index].Path = "state/../../escaped"
				a.headers[index].Name = a.meta.Entries[index].Path
			case "absolute":
				a.meta.Entries[index].Path = "/tmp/escaped"
				a.headers[index].Name = a.meta.Entries[index].Path
			case "symlink":
				a.headers[index].Typeflag = tar.TypeSymlink
				a.headers[index].Linkname = "/tmp/victim"
				a.headers[index].Size = 0
			case "hardlink":
				a.headers[index].Typeflag = tar.TypeLink
				a.headers[index].Linkname = "root/config.yaml"
				a.headers[index].Size = 0
			case "device":
				a.headers[index].Typeflag = tar.TypeChar
				a.headers[index].Size = 0
			case "checksum":
				a.bodies[index][0] ^= 1
			case "sqlite":
				a.bodies[index][0] ^= 1
				hash := sha256.Sum256(a.bodies[index])
				a.meta.Entries[index].SHA256 = hex.EncodeToString(hash[:])
			case "schema":
				dbPath := filepath.Join(t.TempDir(), "modified.db")
				if err := os.WriteFile(dbPath, a.bodies[index], 0o600); err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("DROP TABLE provider_identities"); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				a.bodies[index], err = os.ReadFile(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(a.bodies[index])
				a.meta.Entries[index].SHA256 = hex.EncodeToString(hash[:])
				a.meta.Entries[index].Size = int64(len(a.bodies[index]))
				a.headers[index].Size = int64(len(a.bodies[index]))
			case "duplicate":
				a.meta.Entries[index].Path = a.meta.Entries[0].Path
			case "oversize":
				a.meta.Entries[index].Size = maxFileBytes + 1
			case "version":
				a.meta.Version++
			}
			bad := filepath.Join(t.TempDir(), "bad.tar")
			writeArchive(t, bad, a)
			if attack == "truncated" {
				info, _ := os.Stat(bad)
				if err := os.Truncate(bad, info.Size()/2); err != nil {
					t.Fatal(err)
				}
			}
			if attack == "truncated-trailer" {
				info, _ := os.Stat(bad)
				if err := os.Truncate(bad, info.Size()-1024); err != nil {
					t.Fatal(err)
				}
			}
			if attack == "trailing" {
				out, err := os.OpenFile(bad, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := out.WriteString("hidden payload"); err != nil {
					t.Fatal(err)
				}
				if err := out.Close(); err != nil {
					t.Fatal(err)
				}
			}
			parent := t.TempDir()
			root, state := filepath.Join(parent, "new-root"), filepath.Join(parent, "new-state")
			if _, err := Restore(context.Background(), Options{Root: root, StateDir: state, Archive: bad}); err == nil {
				t.Fatalf("accepted %s archive", attack)
			}
			for _, p := range []string{root, state} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatal("failed restore published a destination")
				}
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed restore left staging artifacts")
			}
		})
	}
}

func TestRestorePreservesExistingDestinations(t *testing.T) {
	f := newFixture(t, false)
	if _, err := Backup(context.Background(), Options{Root: f.root, Archive: f.archive}); err != nil {
		t.Fatal(err)
	}
	for _, existingState := range []bool{false, true} {
		root, state := filepath.Join(t.TempDir(), "new-root"), filepath.Join(t.TempDir(), "new-state")
		existing := root
		if existingState {
			existing = state
		}
		if err := os.Mkdir(existing, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Restore(context.Background(), Options{Root: root, StateDir: state, Archive: f.archive}); err == nil {
			t.Fatal("accepted existing empty destination")
		}
		entries, err := os.ReadDir(existing)
		if err != nil || len(entries) != 0 {
			t.Fatal("existing destination changed")
		}
	}
	before := treeBytes(t, f.root)
	if _, err := Restore(context.Background(), Options{Root: f.root, Archive: f.archive}); err == nil {
		t.Fatal("restored over original lab")
	}
	if !reflect.DeepEqual(before, treeBytes(t, f.root)) {
		t.Fatal("restore changed original lab")
	}
}

func TestBackupRefusesSourceSymlinks(t *testing.T) {
	f := newFixture(t, false)
	victim := filepath.Join(t.TempDir(), "external-secret")
	if err := os.WriteFile(victim, []byte("external-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(f.root, "linked-secret")); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(context.Background(), Options{Root: f.root, Archive: f.archive}); err == nil {
		t.Fatal("backup followed source symlink")
	}
	if _, err := os.Stat(f.archive); !os.IsNotExist(err) {
		t.Fatal("failed backup left an archive")
	}
}

func TestExclusivePublicationPreservesRacingDestination(t *testing.T) {
	parent := t.TempDir()
	source, dest := filepath.Join(parent, "stage"), filepath.Join(parent, "racing-destination")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := publish(source, dest); err == nil {
		t.Fatal("exclusive rename replaced racing empty directory")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("failed publication consumed staged data")
	}
}

func TestNestedStateAndLongExecutablePath(t *testing.T) {
	f := newFixture(t, false)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	stateParent := filepath.Join(f.root, "controllers")
	if err := os.Mkdir(stateParent, 0o700); err != nil {
		t.Fatal(err)
	}
	customState := filepath.Join(stateParent, "private")
	if err := os.Rename(f.state, customState); err != nil {
		t.Fatal(err)
	}
	f.state = customState
	longRelative := filepath.Join(strings.Repeat("a", 120), strings.Repeat("b", 120), strings.Repeat("c", 80))
	longSource := filepath.Join(f.root, longRelative)
	if err := os.MkdirAll(filepath.Dir(longSource), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(longSource, []byte("private plugin binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(context.Background(), Options{Root: f.root, StateDir: f.state, Archive: f.archive}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "restored")
	result, err := Restore(context.Background(), Options{Root: root, Archive: f.archive})
	if err != nil {
		t.Fatal(err)
	}
	if result.StateDir != filepath.Join(root, "controllers", "private") {
		t.Fatal("lost nested state layout")
	}
	got, err := os.ReadFile(filepath.Join(root, longRelative))
	if err != nil || string(got) != "private plugin binary" {
		t.Fatalf("long archive path did not roundtrip: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, longRelative))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("executable permissions lost")
	}
}

func TestRestoreRefusesSymlinkDestinationAncestors(t *testing.T) {
	f := newFixture(t, false)
	if _, err := Backup(context.Background(), Options{Root: f.root, Archive: f.archive}); err != nil {
		t.Fatal(err)
	}
	parent, victim := t.TempDir(), t.TempDir()
	link := filepath.Join(parent, "link")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(context.Background(), Options{Root: filepath.Join(link, "new-root"), Archive: f.archive}); err == nil {
		t.Fatal("restore followed symlink ancestor")
	}
	entries, err := os.ReadDir(victim)
	if err != nil || len(entries) != 0 {
		t.Fatal("symlink target changed")
	}
}

func TestBackupRefusesNamesThatCannotBeRestored(t *testing.T) {
	for _, name := range []string{"secret:version", `secret\version`} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, false)
			if err := os.WriteFile(filepath.Join(f.root, name), []byte("sensitive"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Backup(t.Context(), Options{Root: f.root, Archive: f.archive}); err == nil {
				t.Fatal("published an archive containing an unsupported restore path")
			}
			if _, err := os.Stat(f.archive); !os.IsNotExist(err) {
				t.Fatal("refused backup left an archive")
			}
		})
	}
}
