-- Phase 4.5 schema additions for parent-child operation rows.
--
-- `label` carries a human-readable description for sub-step ops (e.g.
-- "install-k3s") that don't correspond to a real resource apply. It's
-- optional for all op types — top-level applies/deletes leave it NULL.
ALTER TABLE operations ADD COLUMN label TEXT;

-- The UI/CLI fetches an op's children with WHERE parent_id = ?. Index lets
-- that scan exactly the rows that belong to the parent instead of the
-- whole table.
CREATE INDEX IF NOT EXISTS ops_by_parent ON operations(parent_id);
