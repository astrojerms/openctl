package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestGenerateTokenLengthAndUniqueness(t *testing.T) {
	a, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	b, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if len(a) != tokenLength*2 {
		t.Errorf("token length = %d, want %d hex chars", len(a), tokenLength*2)
	}
	if a == b {
		t.Error("two GenerateToken calls produced the same token")
	}
}

func TestLoadOrCreateTokenCreatesOnFirstCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	tok, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}
	if tok == "" {
		t.Fatal("token is empty")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("token file should exist: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestLoadOrCreateTokenIsStableOnSubsequentCalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	first, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("first LoadOrCreateToken: %v", err)
	}
	second, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("second LoadOrCreateToken: %v", err)
	}
	if first != second {
		t.Errorf("token changed between calls: %q vs %q", first, second)
	}
}

func TestLoadOrCreateTokenRejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("\n  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreateToken(path)
	if err == nil {
		t.Error("want error for empty token file")
	}
}

func TestValidatorAcceptsCorrectBearer(t *testing.T) {
	v := NewValidator("secret-token")
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer secret-token"))
	p, err := v.check(ctx)
	if err != nil {
		t.Errorf("want nil, got %v", err)
	}
	if p.Role != RoleAdmin || !p.Root {
		t.Errorf("root token should map to admin RootPrincipal, got %+v", p)
	}
}

func TestValidatorRejectsCases(t *testing.T) {
	v := NewValidator("secret-token")
	cases := []struct {
		name string
		md   metadata.MD
	}{
		{"missing metadata", nil},
		{"missing header", metadata.Pairs("other", "value")},
		{"missing bearer prefix", metadata.Pairs("authorization", "secret-token")},
		{"wrong token", metadata.Pairs("authorization", "Bearer not-the-token")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.md != nil {
				ctx = metadata.NewIncomingContext(ctx, c.md)
			}
			_, err := v.check(ctx)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("error is not gRPC status: %v", err)
			}
			if st.Code() != codes.Unauthenticated {
				t.Errorf("code = %v, want Unauthenticated", st.Code())
			}
		})
	}
}

func TestUnaryInterceptorPassesAndBlocks(t *testing.T) {
	v := NewValidator("ok-token")
	intercept := v.UnaryInterceptor()
	called := false
	handler := func(_ context.Context, _ any) (any, error) {
		called = true
		return "value", nil
	}

	// Authorized call — handler should run.
	ctxOK := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer ok-token"))
	if _, err := intercept(ctxOK, nil, &grpc.UnaryServerInfo{}, handler); err != nil {
		t.Errorf("authorized: unexpected error %v", err)
	}
	if !called {
		t.Error("authorized: handler was not invoked")
	}

	// Unauthorized call — handler must not run.
	called = false
	ctxBad := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer wrong"))
	if _, err := intercept(ctxBad, nil, &grpc.UnaryServerInfo{}, handler); err == nil {
		t.Error("unauthorized: want error, got nil")
	}
	if called {
		t.Error("unauthorized: handler should not have been invoked")
	}
}
