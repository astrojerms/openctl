package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// --- fake Cloudflare API v4 ---

type fakeCF struct {
	mu       sync.Mutex
	records  map[string]cfDNSRecord
	tunnels  map[string]cfTunnel
	configs  map[string]any
	seq      int
	srv      *httptest.Server
	lastAuth string
}

func newFakeCF(t *testing.T) *fakeCF {
	t.Helper()
	f := &fakeCF{
		records: map[string]cfDNSRecord{},
		tunnels: map[string]cfTunnel{},
		configs: map[string]any{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCF) ok(w http.ResponseWriter, result any) {
	b, _ := json.Marshal(result)
	_ = json.NewEncoder(w).Encode(cfEnvelope{Success: true, Result: b})
}

func (f *fakeCF) notFound(w http.ResponseWriter, code int) {
	w.WriteHeader(http.StatusOK) // CF often 200s with success:false + a code
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"errors":  []cfAPIError{{Code: code, Message: "not found"}},
	})
}

func (f *fakeCF) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastAuth = r.Header.Get("Authorization")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")

	switch {
	// /zones/{zone}/dns_records ...
	case len(parts) >= 3 && parts[0] == "zones" && parts[2] == "dns_records":
		f.handleDNS(w, r, parts)
	// /accounts/{acct}/cfd_tunnel ...
	case len(parts) >= 3 && parts[0] == "accounts" && parts[2] == "cfd_tunnel":
		f.handleTunnel(w, r, parts)
	default:
		http.Error(w, "unknown path "+r.URL.Path, http.StatusNotFound)
	}
}

func (f *fakeCF) handleDNS(w http.ResponseWriter, r *http.Request, parts []string) {
	switch {
	case len(parts) == 3 && r.Method == "POST":
		var rec cfDNSRecord
		_ = json.NewDecoder(r.Body).Decode(&rec)
		f.seq++
		rec.ID = "rec-" + itoa(f.seq)
		rec.CreatedOn = "2026-01-01T00:00:00Z"
		f.records[rec.ID] = rec
		f.ok(w, rec)
	case len(parts) == 3 && r.Method == "GET":
		list := make([]cfDNSRecord, 0, len(f.records))
		for _, v := range f.records {
			list = append(list, v)
		}
		f.ok(w, list)
	case len(parts) == 4 && r.Method == "GET":
		rec, ok := f.records[parts[3]]
		if !ok {
			f.notFound(w, 81044)
			return
		}
		f.ok(w, rec)
	case len(parts) == 4 && r.Method == "PUT":
		if _, ok := f.records[parts[3]]; !ok {
			f.notFound(w, 81044)
			return
		}
		var rec cfDNSRecord
		_ = json.NewDecoder(r.Body).Decode(&rec)
		rec.ID = parts[3]
		rec.ModifiedOn = "2026-02-02T00:00:00Z"
		f.records[rec.ID] = rec
		f.ok(w, rec)
	case len(parts) == 4 && r.Method == "DELETE":
		if _, ok := f.records[parts[3]]; !ok {
			f.notFound(w, 81044)
			return
		}
		delete(f.records, parts[3])
		f.ok(w, map[string]any{"id": parts[3]})
	default:
		http.Error(w, "bad dns request", http.StatusBadRequest)
	}
}

func (f *fakeCF) handleTunnel(w http.ResponseWriter, r *http.Request, parts []string) {
	switch {
	case len(parts) == 3 && r.Method == "POST":
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.seq++
		tun := cfTunnel{ID: "tun-" + itoa(f.seq), Name: body.Name, Status: "inactive", CreatedAt: "2026-01-01T00:00:00Z"}
		f.tunnels[tun.ID] = tun
		f.ok(w, tun)
	case len(parts) == 3 && r.Method == "GET":
		name := r.URL.Query().Get("name")
		list := make([]cfTunnel, 0, len(f.tunnels))
		for _, v := range f.tunnels {
			if name == "" || v.Name == name {
				list = append(list, v)
			}
		}
		f.ok(w, list)
	case len(parts) == 4 && r.Method == "GET":
		tun, ok := f.tunnels[parts[3]]
		if !ok {
			f.notFound(w, 1049)
			return
		}
		f.ok(w, tun)
	case len(parts) == 5 && parts[4] == "configurations" && r.Method == "PUT":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.configs[parts[3]] = body["config"]
		f.ok(w, body)
	case len(parts) == 5 && parts[4] == "configurations" && r.Method == "GET":
		f.ok(w, map[string]any{"config": f.configs[parts[3]]})
	case len(parts) == 5 && parts[4] == "token" && r.Method == "GET":
		f.ok(w, "run-token-"+parts[3])
	case len(parts) == 5 && parts[4] == "connections" && r.Method == "DELETE":
		f.ok(w, nil)
	case len(parts) == 4 && r.Method == "DELETE":
		if _, ok := f.tunnels[parts[3]]; !ok {
			f.notFound(w, 1049)
			return
		}
		delete(f.tunnels, parts[3])
		f.ok(w, map[string]any{"id": parts[3]})
	default:
		http.Error(w, "bad tunnel request", http.StatusBadRequest)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// configuredProvider wires a provider to the fake CF via the real Configure path.
func configuredProvider(t *testing.T, f *fakeCF, defaults map[string]string) *provider {
	t.Helper()
	p := newProvider()
	raw, _ := json.Marshal(cfConfig{Endpoint: f.srv.URL, TokenSecret: "test-token", Defaults: defaults})
	if err := p.Configure(context.Background(), raw); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return p
}

// --- tests ---

func TestHandshakeAdvertisesKindsAndCapabilities(t *testing.T) {
	hs, err := newProvider().Handshake(context.Background())
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if hs.ProviderName != "cloudflare" || hs.ProtocolVersion != pluginproto.ProtocolVersion {
		t.Errorf("identity = %q/%q", hs.ProviderName, hs.ProtocolVersion)
	}
	kinds := map[string]pluginproto.KindInfo{}
	for _, k := range hs.Kinds {
		kinds[k.Kind] = k
	}
	if kinds[kindDNSRecord].Schema == "" || kinds[kindTunnel].Schema == "" {
		t.Errorf("both kinds must carry a schema: %+v", hs.Kinds)
	}
	if acts := kinds[kindTunnel].Actions; len(acts) != 1 || acts[0] != actionGetToken {
		t.Errorf("tunnel actions = %v, want [get-token]", acts)
	}
	caps := strings.Join(hs.Capabilities, ",")
	for _, want := range []string{pluginproto.CapabilitySchema, pluginproto.CapabilityState, pluginproto.CapabilityActions} {
		if !strings.Contains(caps, want) {
			t.Errorf("missing capability %q in %v", want, hs.Capabilities)
		}
	}
}

func TestConfigureRequiresToken(t *testing.T) {
	raw, _ := json.Marshal(cfConfig{Endpoint: "https://x"})
	if err := newProvider().Configure(context.Background(), raw); err == nil {
		t.Fatal("expected error when no API token is configured")
	}
}

func TestDNSRecordLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newFakeCF(t)
	p := configuredProvider(t, f, map[string]string{"zoneId": "zone-1"})

	m := &protocol.Resource{APIVersion: apiVersion, Kind: kindDNSRecord}
	m.Metadata.Name = "www"
	m.Spec = map[string]any{"type": "A", "name": "www.example.com", "content": "1.2.3.4", "proxied": true}

	// Create.
	ar, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: m})
	if err != nil {
		t.Fatalf("apply(create): %v", err)
	}
	var st dnsState
	if err := json.Unmarshal(ar.State, &st); err != nil || st.RecordID == "" || st.ZoneID != "zone-1" {
		t.Fatalf("state after create = %s (err %v)", ar.State, err)
	}
	if ar.Resource.Status["id"] != st.RecordID {
		t.Errorf("status.id %v != state recordId %v", ar.Resource.Status["id"], st.RecordID)
	}

	// The token/auth header reached the API.
	if f.lastAuth != "Bearer test-token" {
		t.Errorf("auth header = %q", f.lastAuth)
	}

	// Get reflects observed state.
	gr, err := p.Get(ctx, pluginproto.GetParams{Kind: kindDNSRecord, Name: "www", State: ar.State})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gr.Resource.Spec["content"] != "1.2.3.4" {
		t.Errorf("get content = %v", gr.Resource.Spec["content"])
	}

	// Update in place: same record ID, new content.
	m.Spec["content"] = "5.6.7.8"
	ar2, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: m, State: ar.State})
	if err != nil {
		t.Fatalf("apply(update): %v", err)
	}
	var st2 dnsState
	_ = json.Unmarshal(ar2.State, &st2)
	if st2.RecordID != st.RecordID {
		t.Errorf("update changed record id: %s -> %s", st.RecordID, st2.RecordID)
	}
	if ar2.Resource.Spec["content"] != "5.6.7.8" {
		t.Errorf("updated content = %v", ar2.Resource.Spec["content"])
	}

	// List finds it (keyed by record ID).
	list, err := p.List(ctx, kindDNSRecord)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d (%v), err %v", len(list), list, err)
	}

	// Delete, then Get maps to NotFound.
	if err := p.Delete(ctx, pluginproto.DeleteParams{Kind: kindDNSRecord, State: ar2.State}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = p.Get(ctx, pluginproto.GetParams{Kind: kindDNSRecord, Name: "www", State: ar2.State})
	var e *pluginproto.Error
	if !errors.As(err, &e) || e.Code != pluginproto.CodeNotFound {
		t.Fatalf("get after delete = %v, want NotFound", err)
	}
	// Delete is idempotent.
	if err := p.Delete(ctx, pluginproto.DeleteParams{Kind: kindDNSRecord, State: ar2.State}); err != nil {
		t.Errorf("second delete should be nil, got %v", err)
	}
}

func TestTunnelLifecycleAndToken(t *testing.T) {
	ctx := context.Background()
	f := newFakeCF(t)
	p := configuredProvider(t, f, map[string]string{"accountId": "acct-1"})

	m := &protocol.Resource{APIVersion: apiVersion, Kind: kindTunnel}
	m.Metadata.Name = "home"
	m.Spec = map[string]any{
		"ingress": []any{
			map[string]any{"hostname": "app.example.com", "service": "http://localhost:8080"},
		},
	}

	ar, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: m})
	if err != nil {
		t.Fatalf("apply(create tunnel): %v", err)
	}
	var st tunnelState
	if err := json.Unmarshal(ar.State, &st); err != nil || st.TunnelID == "" {
		t.Fatalf("tunnel state = %s (err %v)", ar.State, err)
	}
	// Token must NOT be in the observed status (it's a secret).
	if _, leaked := ar.Resource.Status["token"]; leaked {
		t.Error("tunnel run token leaked into status")
	}
	// cnameTarget is the ready-to-$ref DNS value, so a DNSRecord's content can
	// pull it without the operator copying the tunnel id by hand.
	if want := st.TunnelID + ".cfargotunnel.com"; ar.Resource.Status["cnameTarget"] != want {
		t.Errorf("status.cnameTarget = %v, want %q", ar.Resource.Status["cnameTarget"], want)
	}

	// Ingress config was pushed, and a catch-all appended (host-scoped last rule).
	cfg, _ := f.configs[st.TunnelID].(map[string]any)
	ingress, _ := cfg["ingress"].([]any)
	if len(ingress) != 2 {
		t.Fatalf("ingress rules pushed = %d, want 2 (rule + catch-all): %v", len(ingress), ingress)
	}
	last, _ := ingress[1].(map[string]any)
	if last["service"] != "http_status:404" {
		t.Errorf("catch-all = %v, want http_status:404", last)
	}

	// get-token action returns the run token as a downloadable payload.
	res, err := p.DoAction(ctx, pluginproto.DoActionParams{Kind: kindTunnel, Name: "home", Action: actionGetToken})
	if err != nil {
		t.Fatalf("get-token: %v", err)
	}
	if res.DownloadContent != "run-token-"+st.TunnelID {
		t.Errorf("token = %q", res.DownloadContent)
	}
	if res.DownloadFilename != "home-tunnel.token" {
		t.Errorf("filename = %q", res.DownloadFilename)
	}

	// List + delete.
	if list, err := p.List(ctx, kindTunnel); err != nil || len(list) != 1 {
		t.Fatalf("list tunnels = %d, err %v", len(list), err)
	}
	if err := p.Delete(ctx, pluginproto.DeleteParams{Kind: kindTunnel, State: ar.State}); err != nil {
		t.Fatalf("delete tunnel: %v", err)
	}
	if _, err := p.Get(ctx, pluginproto.GetParams{Kind: kindTunnel, Name: "home", State: ar.State}); err == nil {
		t.Error("expected NotFound after tunnel delete")
	}
}

// TestSchemasCompileAndValidate registers the plugin's advertised CUE schemas
// through openctl's real external-schema path and checks they accept a valid
// manifest and reject an invalid one — the same validation the controller runs.
func TestSchemasCompileAndValidate(t *testing.T) {
	t.Cleanup(schema.ResetExternal)
	schema.RegisterExternal(apiVersion, kindDNSRecord, dnsRecordSchema)
	schema.RegisterExternal(apiVersion, kindTunnel, tunnelSchema)

	valid := &protocol.Resource{APIVersion: apiVersion, Kind: kindDNSRecord}
	valid.Metadata.Name = "www"
	valid.Spec = map[string]any{"type": "A", "name": "www.example.com", "content": "1.2.3.4"}
	if err := schema.Validate(valid); err != nil {
		t.Errorf("valid DNSRecord rejected: %v", err)
	}

	// content may be a $ref to another resource's status (e.g. a Tunnel's
	// status.cnameTarget) — the raw marker must pass schema validation, since
	// refs are resolved later (in the dispatcher), not at validation time.
	refd := &protocol.Resource{APIVersion: apiVersion, Kind: kindDNSRecord}
	refd.Metadata.Name = "app"
	refd.Spec = map[string]any{
		"type": "CNAME", "name": "app.example.com", "proxied": true,
		"content": map[string]any{"$ref": map[string]any{
			"apiVersion": apiVersion, "kind": kindTunnel, "name": "home", "field": "status.cnameTarget",
		}},
	}
	if err := schema.Validate(refd); err != nil {
		t.Errorf("DNSRecord with a $ref content rejected: %v", err)
	}

	// Missing required spec.type must fail validation.
	invalid := &protocol.Resource{APIVersion: apiVersion, Kind: kindDNSRecord}
	invalid.Metadata.Name = "bad"
	invalid.Spec = map[string]any{"name": "x.example.com", "content": "1.2.3.4"}
	if err := schema.Validate(invalid); err == nil {
		t.Error("DNSRecord missing spec.type should fail validation")
	}

	// A malformed content object (neither a string nor a well-formed $ref) is
	// still rejected — the disjunction didn't open the field to anything.
	badContent := &protocol.Resource{APIVersion: apiVersion, Kind: kindDNSRecord}
	badContent.Metadata.Name = "bad2"
	badContent.Spec = map[string]any{"type": "A", "name": "x.example.com", "content": map[string]any{"bogus": true}}
	if err := schema.Validate(badContent); err == nil {
		t.Error("DNSRecord with a non-string, non-ref content should fail validation")
	}
}

func TestTunnelObservedCnameTarget(t *testing.T) {
	// Populated id → ready-to-$ref CNAME target.
	r := tunnelObserved("home", &cfTunnel{ID: "abc123", Name: "home"}, "acct-1")
	if r.Status["cnameTarget"] != "abc123.cfargotunnel.com" {
		t.Errorf("cnameTarget = %v", r.Status["cnameTarget"])
	}
	// No id (shouldn't happen, but guard) → no cnameTarget rather than a bogus
	// ".cfargotunnel.com".
	r = tunnelObserved("home", &cfTunnel{Name: "home"}, "acct-1")
	if _, ok := r.Status["cnameTarget"]; ok {
		t.Errorf("cnameTarget should be absent without an id, got %v", r.Status["cnameTarget"])
	}
}

func TestTunnelSchemaDeclaresOutputs(t *testing.T) {
	t.Cleanup(schema.ResetExternal)
	schema.RegisterExternal(apiVersion, kindTunnel, tunnelSchema)

	outs, ok := schema.OutputsFor(apiVersion, kindTunnel)
	if !ok {
		t.Fatal("Tunnel schema should declare status outputs")
	}
	byPath := map[string]string{}
	for _, o := range outs {
		byPath[o.Path] = o.Type
	}
	// cnameTarget is the value a DNSRecord $refs to route a hostname.
	if byPath["status.cnameTarget"] != "string" {
		t.Errorf("Tunnel should declare status.cnameTarget:string; got %v", byPath)
	}
	if byPath["status.id"] != "string" {
		t.Errorf("Tunnel should declare status.id:string; got %v", byPath)
	}
}
