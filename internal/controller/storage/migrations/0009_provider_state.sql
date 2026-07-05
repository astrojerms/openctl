-- provider_state stores OPAQUE per-resource state that a stateful provider
-- hands back and expects to see again on the next call — distinct from
-- applied_manifests, which holds the user's *desired* manifest + verifying-
-- trace cache hashes. This is the "observed provider state" store from
-- docs/plugin-architecture.md, added for stateful external plugins (and, later,
-- the Terraform/OpenTofu provider host).
--
-- openctl never parses these blobs: it stores what the plugin returns and
-- replays it verbatim. Because the store is per-resource and opaque, none of
-- the painful monolithic-tfstate properties (global lock, whole-world file,
-- state surgery) apply — each resource's blob is independent.
--
--   state_blob     — the provider's observed state (msgpack/JSON/whatever)
--   private_blob   — provider-private data threaded plan -> apply (may be NULL)
--   schema_version — the provider schema version the blob was written under, so
--                    a future version bump can trigger the provider's own
--                    state-upgrade path (unused by stateless/simple plugins; 0).
--
-- Single row per (api_version, kind, name): apply upserts, delete removes.
CREATE TABLE IF NOT EXISTS provider_state (
	api_version    TEXT NOT NULL,
	kind           TEXT NOT NULL,
	name           TEXT NOT NULL,
	state_blob     BLOB,
	private_blob   BLOB,
	schema_version INTEGER NOT NULL DEFAULT 0,
	updated_at     TEXT NOT NULL,
	PRIMARY KEY (api_version, kind, name)
);
