-- UI Phase U1.4 — browser-shaped session tokens.
--
-- The install-time bearer token (~/.openctl/controller/token) is the "root"
-- auth credential. Sessions are minted from it via SessionService.Login and
-- carry their own token (per-session, expiring) so the browser cookie
-- delivered by the future HTTP gateway isn't the root token. user_id is
-- a fixed string ("default") for v1 single-user mode — the column exists
-- so multi-user can ship as a pure additive change.
--
-- We store sha256(token), not the token itself, so a state.db read doesn't
-- leak live credentials.
CREATE TABLE IF NOT EXISTS sessions (
	id           TEXT NOT NULL PRIMARY KEY,
	user_id      TEXT NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	display_name TEXT,                          -- e.g. browser name / device
	created_at   TEXT NOT NULL,
	expires_at   TEXT NOT NULL                  -- RFC3339; rows past this are ignored
);

-- Auth middleware looks up by token_hash on every request — needs an index.
CREATE INDEX IF NOT EXISTS sessions_by_token_hash ON sessions(token_hash);
-- GC sweeps WHERE expires_at < now — secondary index optional but small.
CREATE INDEX IF NOT EXISTS sessions_by_expiry ON sessions(expires_at);
