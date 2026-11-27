package storage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
)

// ValidateSnapshot checks a standalone recovery database without migrating or
// changing it. Recovery accepts precisely the schema understood by this binary.
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
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer reference.Close()
	reference.SetMaxOpenConns(1)
	if err := migrate(ctx, reference); err != nil {
		return err
	}
	wantVersion, err := currentVersion(ctx, reference)
	if err != nil {
		return err
	}
	version, err := currentVersion(ctx, db)
	if err != nil || version != wantVersion {
		return fmt.Errorf("unsupported recovery schema version %d (requires %d): %v", version, wantVersion, err)
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
