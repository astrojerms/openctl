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
	users    []userCred    // named user tokens (WithUsers); may be empty
	sessions *SessionStore // optional; nil disables session-token auth
}

// userCred is a resolved named-user token and the principal it authenticates.
type userCred struct {
	token     []byte
	principal Principal
}

// NewValidator builds a Validator that accepts requests presenting the given
// token in the Authorization header as `Bearer <token>`.
func NewValidator(token string) *Validator {
	return &Validator{expected: []byte(token)}
}

// WithUsers attaches named-user tokens: a request presenting one authenticates
// as that user with its role (rather than the admin root principal).
// Idempotent — repeated calls overwrite.
func (v *Validator) WithUsers(users []User) *Validator {
	creds := make([]userCred, 0, len(users))
	for _, u := range users {
		creds = append(creds, userCred{
			token:     []byte(u.Token),
			principal: Principal{UserID: u.UserID, Role: u.Role},
		})
	}
	v.users = creds
	return v
}

// WithSessions attaches a SessionStore so the validator accepts session
// tokens (sha256-looked-up in the sessions table) alongside the root
// token. Idempotent — repeated calls overwrite.
func (v *Validator) WithSessions(s *SessionStore) *Validator {
	v.sessions = s
	return v
}

// UnaryInterceptor returns a gRPC unary interceptor that validates the
// bearer token on every request and injects the resolved Principal into the
// context. Failed checks return Unauthenticated.
func (v *Validator) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		p, err := v.check(ctx)
		if err != nil {
			return nil, err
		}
		return handler(ContextWithPrincipal(ctx, p), req)
	}
}

// StreamInterceptor mirrors UnaryInterceptor for server-streaming RPCs
// like ResourceService.Watch. Same token semantics; the resolved Principal is
// injected into the stream's context via a wrapper.
func (v *Validator) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		p, err := v.check(ss.Context())
		if err != nil {
			return err
		}
		return handler(srv, &principalStream{ServerStream: ss, ctx: ContextWithPrincipal(ss.Context(), p)})
	}
}

// principalStream overrides Context so downstream handlers see the injected
// Principal on server-streaming RPCs.
type principalStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *principalStream) Context() context.Context { return s.ctx }

// check validates the bearer token and returns the caller's Principal. The
// root token maps to the admin RootPrincipal; a session token maps to a
// principal carrying the session's user and role.
func (v *Validator) check(ctx context.Context) (Principal, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return Principal{}, status.Error(codes.Unauthenticated, "missing metadata")
	}
	auths := md.Get("authorization")
	if len(auths) == 0 {
		return Principal{}, status.Error(codes.Unauthenticated, "missing authorization header")
	}
	tok, ok := strings.CutPrefix(auths[0], bearerPrefix)
	if !ok {
		return Principal{}, status.Error(codes.Unauthenticated, "authorization header must start with Bearer")
	}
	if subtle.ConstantTimeCompare([]byte(tok), v.expected) == 1 {
		return RootPrincipal(), nil
	}
	tokBytes := []byte(tok)
	for _, u := range v.users {
		if subtle.ConstantTimeCompare(tokBytes, u.token) == 1 {
			return u.principal, nil
		}
	}
	if v.sessions != nil {
		sess, err := v.sessions.Lookup(ctx, tok)
		if err == nil && sess != nil {
			return sess.Principal(), nil
		}
	}
	return Principal{}, status.Error(codes.Unauthenticated, "invalid token")
}
