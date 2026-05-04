-- Phase 3 schema: the operations table powers the async dispatcher and
-- the persisted-operations history. parent_id is for Phase 4's parent/child
-- ops (Cluster apply spawns child VM ops); manifest_json holds the input
-- spec for apply ops; result_json holds the resource state at completion.
--
-- All timestamps are ISO-8601 in UTC (CURRENT_TIMESTAMP from SQLite gives
-- "YYYY-MM-DD HH:MM:SS"; the data layer normalizes to RFC3339 in/out).
CREATE TABLE IF NOT EXISTS operations (
	id              TEXT NOT NULL PRIMARY KEY,
	parent_id       TEXT,
	type            TEXT NOT NULL,
	api_version     TEXT NOT NULL,
	kind            TEXT NOT NULL,
	resource_name   TEXT NOT NULL,
	manifest_json   TEXT,
	result_json     TEXT,
	status          TEXT NOT NULL,
	error           TEXT,
	submitted_at    TEXT NOT NULL,
	started_at      TEXT,
	completed_at    TEXT
);

-- The fail-fast concurrency check is "any in-flight op for this resource?".
-- Index lets that query scan only the few rows for a given resource.
CREATE INDEX IF NOT EXISTS ops_by_resource
	ON operations(api_version, kind, resource_name, status);

-- The dispatcher pulls oldest pending ops by submitted_at; index supports it.
CREATE INDEX IF NOT EXISTS ops_by_status
	ON operations(status, submitted_at);
