// Package storage owns the controller's SQLite-backed state. Phase 1 only
// initializes the schema_meta migration tracker; later phases add tables for
// resources, operations, and operation_steps via numbered migrations under
// migrations/.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (or creates) the SQLite database at the given path and applies
// any pending schema migrations. The returned *sql.DB is the caller's to
// close.
//
// Connection pragmas:
//   - busy_timeout=5000: writes wait up to 5s for a contended lock instead
//     of returning SQLITE_BUSY immediately. Important because the dispatcher
//     and the gRPC handlers compete for write locks on the same file.
//   - journal_mode=WAL: enables write-ahead logging for better concurrent
//     read+write throughput.
//   - foreign_keys=on: SQLite defaults to off for backward compatibility;
//     we want enforcement.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_meta (
		version INTEGER NOT NULL PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create schema_meta: %w", err)
	}

	current, err := currentVersion(ctx, db)
	if err != nil {
		return err
	}

	migrations, err := pendingMigrations(current)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if err := apply(ctx, db, m); err != nil {
			return fmt.Errorf("apply %s: %w", m.name, err)
		}
	}
	return nil
}

type migration struct {
	version int
	name    string
}

func pendingMigrations(currentVersion int) ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, err := versionOf(e.Name())
		if err != nil {
			return nil, err
		}
		if v <= currentVersion {
			continue
		}
		out = append(out, migration{version: v, name: e.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func apply(ctx context.Context, db *sql.DB, m migration) error {
	body, err := migrationsFS.ReadFile("migrations/" + m.name)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_meta (version) VALUES (?)", m.version); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func currentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_meta").Scan(&v)
	if err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

func versionOf(filename string) (int, error) {
	parts := strings.SplitN(filename, "_", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("migration filename %q must be NNNN_description.sql", filename)
	}
	v, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("migration filename %q has non-numeric prefix: %w", filename, err)
	}
	return v, nil
}
