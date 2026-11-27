-- Persist create-only admission through queued operations and controller restarts.
ALTER TABLE operations ADD COLUMN create_only INTEGER NOT NULL DEFAULT 0;
