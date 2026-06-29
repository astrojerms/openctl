-- UI Phase U2.2 — record whether an op was submitted by the CLI or the
-- browser UI, so the git layer can include the source in its commit
-- messages ("apply X/Y via CLI" vs "apply X/Y via UI").
--
-- Empty/NULL is treated as "cli" by readers — that's the safer default
-- since CLI is what existed before the UI gateway shipped.
ALTER TABLE operations ADD COLUMN source TEXT;
