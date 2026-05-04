-- Phase 5 schema: applied_manifests stores the user's *intent* (the spec the
-- last successful apply submitted) keyed by (api_version, kind, name). The
-- operations table can't serve this purpose because completed ops are GC'd,
-- and we need a stable source of desired state for drift detection.
--
-- Single row per resource: an apply overwrites it, a delete removes it.
-- spec_json is the canonical JSON-encoded spec; metadata_json carries the
-- resource's metadata block (labels, annotations) so the comparison can
-- include those if a later phase wants to.
CREATE TABLE IF NOT EXISTS applied_manifests (
	api_version   TEXT NOT NULL,
	kind          TEXT NOT NULL,
	name          TEXT NOT NULL,
	spec_json     TEXT NOT NULL,
	metadata_json TEXT,
	applied_at    TEXT NOT NULL,
	PRIMARY KEY (api_version, kind, name)
);
