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

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_meta").Scan(&count); err != nil {
		t.Fatalf("count schema_meta: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_meta row count = %d after multiple Opens, want 1 (migration should not re-apply)", count)
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
