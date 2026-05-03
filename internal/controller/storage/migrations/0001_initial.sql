-- Phase 1 baseline. The schema_meta table is created by the migration runner
-- itself; this file exists so the runner has at least one migration to apply,
-- which exercises the apply path (and lets us confirm the runner works) on a
-- fresh database. Real schemas (resources, operations, operation_steps) land
-- in subsequent numbered migrations as later phases need them.
CREATE TABLE IF NOT EXISTS controller_meta (
	key TEXT NOT NULL PRIMARY KEY,
	value TEXT NOT NULL
);

INSERT OR REPLACE INTO controller_meta (key, value)
VALUES ('initialized_at', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
