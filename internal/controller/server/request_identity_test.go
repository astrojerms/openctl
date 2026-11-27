package server

import (
	"context"
	"testing"

	"github.com/openctl/openctl/internal/controller/auth"
)

func TestRootRequestIdentitySurvivesBearerSessionAndLocalCredentials(t *testing.T) {
	h, _, db := requestHandlers(t, &fakeProvider{}, 0)
	ctx := t.Context()
	session, err := auth.NewSessionStore(db).Create(ctx, auth.DefaultUserID, "same-operator", auth.RoleAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}
	request := keyedApply(t, "one-root-operator-attempt")
	var operationID string
	for _, caller := range []context.Context{
		auth.ContextWithPrincipal(ctx, auth.RootPrincipal()),
		auth.ContextWithPrincipal(ctx, session.Principal()),
		ctx,
	} {
		response, err := h.Apply(caller, request)
		if err != nil {
			t.Fatal(err)
		}
		if operationID == "" {
			operationID = response.GetOperationId()
		}
		if response.GetOperationId() != operationID {
			t.Fatal("credential change submitted a second operation for the same operator request")
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM operations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("root credential forms did not resolve the original accepted work: %d; %v", count, err)
	}
}
