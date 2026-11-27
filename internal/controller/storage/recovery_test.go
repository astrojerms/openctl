package storage

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const historicalIntent = `{"apiVersion":"apps.openctl.io/v1","kind":"ComposeApplication","metadata":{"name":"media"},"spec":{"environment":{"$secret":{"file":"media-env"}}}}`

func historicalSnapshot(t *testing.T, version int) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(schemaMetaSQL); err != nil {
		t.Fatal(err)
	}
	migrations, err := pendingMigrations(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.version > version {
			break
		}
		if err := apply(t.Context(), db, migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO controller_meta(key,value) VALUES ('migration_controller_id','original-controller')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO operations(id,type,api_version,kind,resource_name,status,submitted_at,manifest_json) VALUES ('original-op','apply','apps.openctl.io/v1','ComposeApplication','media','pending','2026-11-27T00:00:00Z',?)", historicalIntent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO provider_state VALUES ('apps.openctl.io/v1','ComposeApplication','media',X'00FF',X'AA01',7,'2026-11-27T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestHistoricalSnapshotValidationDefersUpgradeAndPreservesState(t *testing.T) {
	// These are released schemas, before create-only admission and before
	// durable request identity. Their backups must remain usable after updates.
	for _, version := range []int{13, 14} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			name := historicalSnapshot(t, version)
			before, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateSnapshot(t.Context(), name); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(name)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("offline validation changed archived database bytes")
			}
			for _, suffix := range []string{"-wal", "-shm"} {
				if _, err := os.Stat(name + suffix); !os.IsNotExist(err) {
					t.Fatalf("offline validation created a source sidecar: %v", err)
				}
			}
			db, err := Open(t.Context(), name)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for query, want := range map[string]string{
				"SELECT value FROM controller_meta WHERE key='migration_controller_id'":                 "original-controller",
				"SELECT manifest_json FROM operations WHERE id='original-op'":                           historicalIntent,
				"SELECT status FROM operations WHERE id='original-op'":                                  "pending",
				"SELECT hex(state_blob)||':'||hex(private_blob) FROM provider_state WHERE name='media'": "00FF:AA01",
			} {
				var got string
				if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
					t.Fatalf("upgrade lost original state: got %q; %v", got, err)
				}
			}
		})
	}
}

func TestHistoricalSnapshotValidationRejectsForgedOrUnknownSchemas(t *testing.T) {
	for _, mutation := range []string{
		"UPDATE schema_meta SET version=999999 WHERE version=14",
		"DELETE FROM schema_meta WHERE version=14",
		"CREATE TRIGGER unexpected_recovery_trigger AFTER UPDATE ON operations BEGIN SELECT 1; END",
	} {
		t.Run(mutation, func(t *testing.T) {
			name := historicalSnapshot(t, 14)
			db, err := sql.Open("sqlite", name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := ValidateSnapshot(context.Background(), name); err == nil {
				t.Fatal("accepted a future, forged historical, or extended schema")
			}
		})
	}
}
