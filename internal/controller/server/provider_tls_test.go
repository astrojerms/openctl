package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/openctl/openctl/internal/config"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func seedProviderTrust(t *testing.T) *config.Context {
	t.Helper()
	sandboxHome(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("private-token-not-for-wire"), 0600); err != nil {
		t.Fatal(err)
	}
	other := &config.Context{Endpoint: "https://other.example:8006", Credentials: "other", CAFile: "/other/ca.pem"}
	cfg := &config.Config{Providers: map[string]*config.Provider{"proxmox": {DefaultContext: "lab", Contexts: map[string]*config.Context{"lab": {Endpoint: "https://lab.example:8006", Credentials: "lab", CAFile: "~/trusted.pem"}, "other": other}, Credentials: map[string]*config.Credential{"lab": {TokenID: "root@pam!lab", TokenSecretFile: tokenFile}, "other": {TokenID: "other-token", TokenSecret: "other-private-secret"}}}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return other
}
func TestProviderTrustPreservesOmittedFieldsAndOtherContexts(t *testing.T) {
	other := seedProviderTrust(t)
	h := newConfigHandler()
	ctx := context.Background()
	response, err := h.UpsertProvider(ctx, &apiv1.UpsertProviderRequest{Name: "proxmox", Endpoint: "https://edited.example:8006", TokenId: "root@pam!updated"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["proxmox"]
	if p.Contexts["lab"].CAFile != "~/trusted.pem" || p.Contexts["lab"].InsecureSkipVerify || !reflect.DeepEqual(p.Contexts["other"], other) {
		t.Fatalf("unrequested context trust changed: %+v", p.Contexts)
	}
	if p.Credentials["lab"].TokenSecretFile == "" || p.Credentials["lab"].TokenSecret != "" || p.Credentials["other"].TokenSecret != "other-private-secret" {
		t.Fatal("credential references or sibling secret changed")
	}
	if response.GetProvider().GetCaFile() != "~/trusted.pem" || !response.GetProvider().GetUsesSecretFile() {
		t.Fatal("saved trust/credential projection lost")
	}
	list, err := h.ListProviders(ctx, &apiv1.ListProvidersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := protojson.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-token-not-for-wire", "other-private-secret"} {
		if strings.Contains(string(wire), secret) {
			t.Fatal("provider projection leaked a credential")
		}
	}
}
func TestProviderTrustExplicitOptInAndClear(t *testing.T) {
	seedProviderTrust(t)
	h := newConfigHandler()
	ctx := context.Background()
	req := &apiv1.UpsertProviderRequest{Name: "proxmox", Endpoint: "https://lab.example:8006", TokenId: "root@pam!lab", CaFile: proto.String(""), InsecureSkipVerify: proto.Bool(true)}
	if _, err := h.UpsertProvider(ctx, req); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	saved := cfg.Providers["proxmox"].Contexts["lab"]
	if saved.CAFile != "" || !saved.InsecureSkipVerify {
		t.Fatalf("explicit insecure policy not persisted: %+v", saved)
	}
	req.CaFile = proto.String("~/verified.pem")
	req.InsecureSkipVerify = proto.Bool(false)
	if _, err := h.UpsertProvider(ctx, req); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	saved = cfg.Providers["proxmox"].Contexts["lab"]
	if saved.CAFile != "~/verified.pem" || saved.InsecureSkipVerify {
		t.Fatalf("explicit verified policy not persisted: %+v", saved)
	}
}
func TestProviderTrustRejectsInvalidEditsWithoutChangingDisk(t *testing.T) {
	seedProviderTrust(t)
	paths, err := config.GetPaths()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []*apiv1.UpsertProviderRequest{
		{Name: "proxmox", Endpoint: "https://lab.example:8006", CaFile: proto.String("/trusted.pem"), InsecureSkipVerify: proto.Bool(true)},
		{Name: "proxmox", Endpoint: "http://lab.example:8006", CaFile: proto.String(""), InsecureSkipVerify: proto.Bool(true)},
		{Name: "proxmox", Endpoint: "https://user:private-password@lab.example:8006"},
		{Name: "proxmox", Endpoint: "https://lab.example:8006?token=private-token"},
	} {
		_, err := newConfigHandler().UpsertProvider(context.Background(), req)
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid trust/endpoint accepted: %v", err)
		}
		if strings.Contains(err.Error(), "private-password") || strings.Contains(err.Error(), "private-token") {
			t.Fatal("validation error exposed URL credentials")
		}
		after, err := os.ReadFile(paths.ConfigFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatal("invalid trust edit changed on-disk configuration")
		}
	}
}
