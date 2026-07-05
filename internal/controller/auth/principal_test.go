package auth

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestRoleAtLeast(t *testing.T) {
	cases := []struct {
		have, min Role
		want      bool
	}{
		{RoleAdmin, RoleViewer, true},
		{RoleAdmin, RoleEditor, true},
		{RoleAdmin, RoleAdmin, true},
		{RoleEditor, RoleViewer, true},
		{RoleEditor, RoleEditor, true},
		{RoleEditor, RoleAdmin, false},
		{RoleViewer, RoleViewer, true},
		{RoleViewer, RoleEditor, false},
		{RoleViewer, RoleAdmin, false},
		{Role("bogus"), RoleViewer, false}, // unknown role authorizes nothing
		{Role(""), RoleViewer, false},
	}
	for _, c := range cases {
		if got := c.have.AtLeast(c.min); got != c.want {
			t.Errorf("Role(%q).AtLeast(%q) = %v, want %v", c.have, c.min, got, c.want)
		}
	}
}

func TestPrincipalContextRoundTrip(t *testing.T) {
	if _, ok := PrincipalFromContext(context.Background()); ok {
		t.Error("empty context should have no principal")
	}
	want := Principal{UserID: "alice", Role: RoleEditor}
	ctx := ContextWithPrincipal(context.Background(), want)
	got, ok := PrincipalFromContext(ctx)
	if !ok {
		t.Fatal("principal missing after injection")
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestRootPrincipal(t *testing.T) {
	p := RootPrincipal()
	if p.Role != RoleAdmin || !p.Root {
		t.Errorf("RootPrincipal = %+v, want admin+root", p)
	}
	if !p.Role.AtLeast(RoleAdmin) {
		t.Error("root should satisfy admin")
	}
}

// TestUnaryInterceptorInjectsPrincipal proves the interceptor makes the
// resolved principal visible to the wrapped handler.
func TestUnaryInterceptorInjectsPrincipal(t *testing.T) {
	v := NewValidator("root-token")
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer root-token"))

	var seen Principal
	var sawPrincipal bool
	handler := func(ctx context.Context, _ any) (any, error) {
		seen, sawPrincipal = PrincipalFromContext(ctx)
		return "ok", nil
	}
	_, err := v.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if !sawPrincipal {
		t.Fatal("handler saw no principal in context")
	}
	if seen.Role != RoleAdmin || !seen.Root {
		t.Errorf("injected principal = %+v, want admin+root", seen)
	}
}

// fakeStream is a minimal grpc.ServerStream returning a fixed context.
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context { return f.ctx }

func TestStreamInterceptorInjectsPrincipal(t *testing.T) {
	v := NewValidator("root-token")
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer root-token"))

	var seen Principal
	var ok bool
	handler := func(_ any, ss grpc.ServerStream) error {
		seen, ok = PrincipalFromContext(ss.Context())
		return nil
	}
	err := v.StreamInterceptor()(nil, &fakeStream{ctx: ctx}, &grpc.StreamServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if !ok || seen.Role != RoleAdmin {
		t.Errorf("stream handler saw principal %+v (ok=%v), want admin", seen, ok)
	}
}

func TestUnaryInterceptorBlocksBadTokenBeforeHandler(t *testing.T) {
	v := NewValidator("root-token")
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer wrong"))
	called := false
	handler := func(context.Context, any) (any, error) { called = true; return nil, nil }
	if _, err := v.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{}, handler); err == nil {
		t.Fatal("want error for bad token")
	}
	if called {
		t.Error("handler must not run when auth fails")
	}
}
