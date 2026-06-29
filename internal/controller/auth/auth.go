// Package auth handles the controller's API token: generating the initial
// token on first start, persisting it, and validating bearer-token headers
// on incoming gRPC requests via a unary interceptor.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// tokenLength is the number of random bytes per token; the file holds
	// 2*tokenLength hex characters.
	tokenLength = 32

	bearerPrefix = "Bearer "
)

// GenerateToken returns a fresh 32-byte random token, hex-encoded.
func GenerateToken() (string, error) {
	buf := make([]byte, tokenLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// LoadOrCreateToken reads the API token from path, creating it (and the file)
// if it doesn't exist. The file is mode 0600. Returns the token value
// (without surrounding whitespace).
func LoadOrCreateToken(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is from controller config
	if err == nil {
		tok := strings.TrimSpace(string(data))
		if tok == "" {
			return "", fmt.Errorf("token file %s is empty", path)
		}
		return tok, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read token: %w", err)
	}
	tok, err := GenerateToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	return tok, nil
}

// Validator validates the bearer token on incoming gRPC requests. Accepts
// either the install-time root token OR a session token minted via
// SessionService.Login (UI Phase U1.4 — sessions optional, nil to disable).
type Validator struct {
	expected []byte
	sessions *SessionStore // optional; nil disables session-token auth
}

// NewValidator builds a Validator that accepts requests presenting the given
// token in the Authorization header as `Bearer <token>`.
func NewValidator(token string) *Validator {
	return &Validator{expected: []byte(token)}
}

// WithSessions attaches a SessionStore so the validator accepts session
// tokens (sha256-looked-up in the sessions table) alongside the root
// token. Idempotent — repeated calls overwrite.
func (v *Validator) WithSessions(s *SessionStore) *Validator {
	v.sessions = s
	return v
}

// UnaryInterceptor returns a gRPC unary interceptor that validates the
// bearer token on every request. Failed checks return Unauthenticated.
func (v *Validator) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := v.check(ctx); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor mirrors UnaryInterceptor for server-streaming RPCs
// like ResourceService.Watch. Same token semantics.
func (v *Validator) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := v.check(ss.Context()); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

func (v *Validator) check(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}
	auths := md.Get("authorization")
	if len(auths) == 0 {
		return status.Error(codes.Unauthenticated, "missing authorization header")
	}
	tok, ok := strings.CutPrefix(auths[0], bearerPrefix)
	if !ok {
		return status.Error(codes.Unauthenticated, "authorization header must start with Bearer")
	}
	if subtle.ConstantTimeCompare([]byte(tok), v.expected) == 1 {
		return nil
	}
	if v.sessions != nil {
		sess, err := v.sessions.Lookup(ctx, tok)
		if err == nil && sess != nil {
			return nil
		}
	}
	return status.Error(codes.Unauthenticated, "invalid token")
}
