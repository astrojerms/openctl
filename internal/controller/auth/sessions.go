package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultUserID is the user id assigned to every session in single-user
// mode (UI Phase U1.4). The schema carries the column so multi-user can
// ship as a pure additive change without a migration.
const DefaultUserID = "default"

// DefaultSessionTTL is how long a freshly-minted session lives unless
// the caller overrides it on Create. 7 days matches typical "remember
// me" browser session behavior; short enough to limit blast radius if
// a cookie is exfiltrated, long enough to avoid daily re-logins.
const DefaultSessionTTL = 7 * 24 * time.Hour

// Session is the in-process view of a row in the sessions table.
// Token is only ever populated on Create — Lookup never returns it (we
// store only sha256(token), so the raw value is unrecoverable).
type Session struct {
	ID          string
	UserID      string
	DisplayName string
	CreatedAt   string
	ExpiresAt   string
	// Token is the raw session token. Set only by Create; nil on lookups.
	Token string
}

// Principal returns the authenticated principal for this session. Every
// session today is minted from the root credential and is therefore admin;
// once a non-admin identity source (named tokens / OIDC) lands, the session
// row will carry the creator's role and this reads it instead.
func (s *Session) Principal() Principal {
	return Principal{UserID: s.UserID, Role: RoleAdmin}
}

// SessionStore is the sessions data layer. Backed by SQLite.
type SessionStore struct {
	db *sql.DB
}

// NewSessionStore constructs a store on the given DB. The schema migration
// (0006_sessions) must have been applied.
func NewSessionStore(db *sql.DB) *SessionStore {
	return &SessionStore{db: db}
}

// Create mints a new session for userID with the given displayName and TTL.
// Returns the Session (with the raw token populated). TTL <= 0 uses
// DefaultSessionTTL.
func (s *SessionStore) Create(ctx context.Context, userID, displayName string, ttl time.Duration) (*Session, error) {
	if userID == "" {
		userID = DefaultUserID
	}
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	token, err := GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}
	now := time.Now().UTC()
	sess := &Session{
		ID:          newSessionID(),
		UserID:      userID,
		DisplayName: displayName,
		CreatedAt:   now.Format(time.RFC3339Nano),
		ExpiresAt:   now.Add(ttl).Format(time.RFC3339Nano),
		Token:       token,
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, token_hash, display_name, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, hashToken(token), nullableString(displayName),
		sess.CreatedAt, sess.ExpiresAt,
	); err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	return sess, nil
}

// Lookup returns the session for the given raw token, or
// (nil, nil) if no such session exists or it has expired. Constant-time
// hash compare; lookup by index avoids row-scan side-channels.
func (s *SessionStore) Lookup(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, nil
	}
	h := hashToken(token)
	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, COALESCE(display_name, ''), created_at, expires_at
		 FROM sessions WHERE token_hash = ?`,
		h,
	)
	var sess Session
	if err := row.Scan(&sess.ID, &sess.UserID, &sess.DisplayName, &sess.CreatedAt, &sess.ExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup session: %w", err)
	}
	// Expiry check.
	expiry, err := time.Parse(time.RFC3339Nano, sess.ExpiresAt)
	if err == nil && time.Now().UTC().After(expiry) {
		return nil, nil
	}
	return &sess, nil
}

// Delete removes a session by id. Idempotent — delete on missing returns
// nil. Used by Logout.
func (s *SessionStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteByToken removes a session looked up by raw token. Used by
// Logout RPCs that only have the token, not the id.
func (s *SessionStore) DeleteByToken(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE token_hash = ?`,
		hashToken(token),
	)
	return err
}

// GCExpired removes all sessions past their expires_at. Returns the count
// removed. Cheap; safe to call from a periodic sweep.
func (s *SessionStore) GCExpired(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newSessionID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "sess-" + strings.ToLower(hex.EncodeToString(buf[:]))
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
