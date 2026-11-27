package storage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
)

// ValidateSnapshot checks a standalone recovery database without migrating or
// changing it. The schema must exactly match a known embedded migration version;
// a restored older database is upgraded only when the daemon is started.
func ValidateSnapshot(ctx context.Context, path string) error {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&immutable=1")
	if err != nil {
		return err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("SQLite integrity check: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("SQLite integrity check failed: %s", integrity)
	}
	version, err := currentVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("read recovery schema version: %w", err)
	}
	migrations, err := pendingMigrations(0)
	if err != nil {
		return err
	}
	through := -1
	for i, migration := range migrations {
		if migration.version == version {
			through = i
			break
		}
	}
	if through < 0 {
		return fmt.Errorf("unsupported recovery schema version %d", version)
	}
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer reference.Close()
	reference.SetMaxOpenConns(1)
	if _, err := reference.ExecContext(ctx, schemaMetaSQL); err != nil {
		return err
	}
	for _, migration := range migrations[:through+1] {
		if err := apply(ctx, reference, migration); err != nil {
			return fmt.Errorf("build recovery schema reference: %w", err)
		}
	}
	readSchema := func(db *sql.DB) ([]string, error) {
		rows, err := db.QueryContext(ctx, "SELECT type || ':' || name || ':' || tbl_name || ':' || sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY type, name")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var schema []string
		for rows.Next() {
			var item string
			if err := rows.Scan(&item); err != nil {
				return nil, err
			}
			schema = append(schema, item)
		}
		return schema, rows.Err()
	}
	want, err := readSchema(reference)
	if err != nil {
		return err
	}
	got, err := readSchema(db)
	if err != nil {
		return err
	}
	if len(got) != len(want) {
		return fmt.Errorf("recovery database schema does not match this controller")
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("recovery database schema does not match this controller")
		}
	}
	return nil
}
