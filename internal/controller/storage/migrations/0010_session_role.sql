-- Multi-user auth — per-session role.
--
-- Sessions minted via SessionService.Login now carry the role of the caller
-- who created them (resolved from their bearer token by the auth interceptor),
-- so a browser cookie is scoped to that user's role rather than always admin.
--
-- Existing rows (and any session minted before roles landed) default to
-- 'admin', preserving prior behavior — this is a purely additive change.
ALTER TABLE sessions ADD COLUMN role TEXT NOT NULL DEFAULT 'admin';
