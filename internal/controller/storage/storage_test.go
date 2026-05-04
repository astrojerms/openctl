package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenAppliesMigrationsOnFirstStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	v, err := currentVersion(context.Background(), db)
	if err != nil {
		t.Fatalf("currentVersion: %v", err)
	}
	if v < 1 {
		t.Errorf("schema version after Open = %d, want >= 1", v)
	}

	// The 0001_initial migration writes a row to controller_meta.
	var marker string
	if err := db.QueryRow("SELECT value FROM controller_meta WHERE key = 'initialized_at'").Scan(&marker); err != nil {
		t.Fatalf("controller_meta should have initialized_at row: %v", err)
	}
	if marker == "" {
		t.Error("initialized_at value is empty")
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	for i := range 3 {
		db, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		_ = db.Close()
	}

	// The marker should not be re-written across calls because INSERT OR REPLACE
	// on the same key is a no-op behaviorally; what matters is no errors.
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("final Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	// schema_meta should have one row per applied migration, and the count
	// should be stable across re-Opens (the migration runner skips already-
	// applied versions). We don't pin a specific number because new
	// migrations land in subsequent phases.
	var initial, after int
	db1, _ := Open(context.Background(), path)
	_ = db1.QueryRow("SELECT COUNT(*) FROM schema_meta").Scan(&initial)
	_ = db1.Close()
	db2, _ := Open(context.Background(), path)
	_ = db2.QueryRow("SELECT COUNT(*) FROM schema_meta").Scan(&after)
	_ = db2.Close()
	if initial == 0 {
		t.Error("schema_meta empty after Open")
	}
	if initial != after {
		t.Errorf("schema_meta row count grew from %d to %d across Opens (migration re-applied?)", initial, after)
	}
}

func TestVersionOf(t *testing.T) {
	cases := []struct {
		filename string
		want     int
		wantErr  bool
	}{
		{"0001_initial.sql", 1, false},
		{"0042_resources.sql", 42, false},
		{"no_prefix.sql", 0, true},
		{"abc_x.sql", 0, true},
	}
	for _, c := range cases {
		got, err := versionOf(c.filename)
		if c.wantErr {
			if err == nil {
				t.Errorf("versionOf(%q): want error, got %d", c.filename, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("versionOf(%q): unexpected error: %v", c.filename, err)
		}
		if got != c.want {
			t.Errorf("versionOf(%q) = %d, want %d", c.filename, got, c.want)
		}
	}
}
