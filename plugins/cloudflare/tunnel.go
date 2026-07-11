package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// tunnelState is the opaque blob openctl persists for a Tunnel: the
// Cloudflare tunnel ID and the account it lives in.
type tunnelState struct {
	TunnelID  string `json:"tunnelId"`
	AccountID string `json:"accountId"`
}

// cfTunnel is the Cloudflare cfd_tunnel wire shape (subset we manage).
type cfTunnel struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Status    string `json:"status,omitempty"`
	ConfigSrc string `json:"config_src,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	DeletedAt string `json:"deleted_at,omitempty"`
}

// tunnelAccount resolves the effective account (spec.accountId, else default).
func (p *provider) tunnelAccount(spec map[string]any) string {
	if acct := specString(spec, "accountId"); acct != "" {
		return acct
	}
	return p.cfg.Defaults["accountId"]
}

func (p *provider) applyTunnel(ctx context.Context, m *protocol.Resource, state json.RawMessage) (*pluginproto.ApplyResult, error) {
	acct := p.tunnelAccount(m.Spec)
	if acct == "" {
		return nil, fmt.Errorf("no account for Tunnel %q (set spec.accountId or provider defaults.accountId)", m.Metadata.Name)
	}

	var st tunnelState
	if len(state) > 0 {
		_ = json.Unmarshal(state, &st)
	}

	// The Cloudflare tunnel name is the manifest name, so the token action can
	// resolve name -> id without persisted state.
	createBody := map[string]any{"name": m.Metadata.Name, "config_src": "cloudflare"}

	var tun cfTunnel
	switch st.TunnelID {
	case "":
		if err := p.client.do(ctx, "POST", "/accounts/"+acct+"/cfd_tunnel", nil, createBody, &tun); err != nil {
			return nil, err
		}
	default:
		err := p.client.do(ctx, "GET", "/accounts/"+acct+"/cfd_tunnel/"+st.TunnelID, nil, nil, &tun)
		if errors.Is(err, errNotFound) {
			// Recreate a tunnel deleted out-of-band so apply converges.
			err = p.client.do(ctx, "POST", "/accounts/"+acct+"/cfd_tunnel", nil, createBody, &tun)
		}
		if err != nil {
			return nil, err
		}
	}

	// Push ingress configuration when the spec declares it.
	if ingress := tunnelIngress(m.Spec); len(ingress) > 0 {
		body := map[string]any{"config": map[string]any{"ingress": ingress}}
		if err := p.client.do(ctx, "PUT", "/accounts/"+acct+"/cfd_tunnel/"+tun.ID+"/configurations", nil, body, nil); err != nil {
			return nil, err
		}
	}

	newState, _ := json.Marshal(tunnelState{TunnelID: tun.ID, AccountID: acct})
	return &pluginproto.ApplyResult{Resource: tunnelObserved(m.Metadata.Name, &tun, acct), State: newState}, nil
}

func (p *provider) getTunnel(ctx context.Context, name string, state json.RawMessage) (*pluginproto.GetResult, error) {
	var st tunnelState
	if len(state) > 0 {
		_ = json.Unmarshal(state, &st)
	}
	if st.TunnelID == "" || st.AccountID == "" {
		return nil, pluginproto.NotFound(fmt.Sprintf("Tunnel %q has no prior state", name))
	}
	var tun cfTunnel
	if err := p.client.do(ctx, "GET", "/accounts/"+st.AccountID+"/cfd_tunnel/"+st.TunnelID, nil, nil, &tun); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, pluginproto.NotFound(fmt.Sprintf("Tunnel %q not found in Cloudflare", name))
		}
		return nil, err
	}
	if tun.DeletedAt != "" {
		return nil, pluginproto.NotFound(fmt.Sprintf("Tunnel %q is deleted", name))
	}
	return &pluginproto.GetResult{Resource: tunnelObserved(name, &tun, st.AccountID), State: state}, nil
}

func (p *provider) listTunnels(ctx context.Context) ([]*protocol.Resource, error) {
	acct := p.cfg.Defaults["accountId"]
	if acct == "" {
		return nil, nil
	}
	var tuns []cfTunnel
	q := url.Values{"is_deleted": {"false"}, "per_page": {"100"}}
	if err := p.client.do(ctx, "GET", "/accounts/"+acct+"/cfd_tunnel", q, nil, &tuns); err != nil {
		return nil, err
	}
	out := make([]*protocol.Resource, 0, len(tuns))
	for i := range tuns {
		out = append(out, tunnelObserved(tuns[i].Name, &tuns[i], acct))
	}
	return out, nil
}

func (p *provider) deleteTunnel(ctx context.Context, state json.RawMessage) error {
	var st tunnelState
	if len(state) > 0 {
		_ = json.Unmarshal(state, &st)
	}
	if st.TunnelID == "" || st.AccountID == "" {
		return nil
	}
	// Best-effort: drop active connections so the tunnel delete isn't rejected.
	_ = p.client.do(ctx, "DELETE", "/accounts/"+st.AccountID+"/cfd_tunnel/"+st.TunnelID+"/connections", nil, nil, nil)
	err := p.client.do(ctx, "DELETE", "/accounts/"+st.AccountID+"/cfd_tunnel/"+st.TunnelID, nil, nil, nil)
	if err != nil && !errors.Is(err, errNotFound) {
		return err
	}
	return nil
}

// tunnelToken implements the "get-token" action. DoAction carries no state, so
// resolve the tunnel by name (== the CF tunnel name) via the account default.
// The token is a secret used with `cloudflared tunnel run --token`, so it is
// returned as a downloadable payload and never persisted to status/git.
func (p *provider) tunnelToken(ctx context.Context, name string) (*pluginproto.DoActionResult, error) {
	acct := p.cfg.Defaults["accountId"]
	if acct == "" {
		return nil, fmt.Errorf("get-token for Tunnel %q: no accountId in provider defaults", name)
	}
	id, err := p.findTunnelID(ctx, acct, name)
	if err != nil {
		return nil, err
	}
	var token string
	if err := p.client.do(ctx, "GET", "/accounts/"+acct+"/cfd_tunnel/"+id+"/token", nil, nil, &token); err != nil {
		return nil, err
	}
	return &pluginproto.DoActionResult{
		Message:          fmt.Sprintf("Run token for tunnel %q — cloudflared tunnel run --token <downloaded>", name),
		DownloadContent:  token,
		DownloadFilename: name + "-tunnel.token",
	}, nil
}

func (p *provider) findTunnelID(ctx context.Context, acct, name string) (string, error) {
	var tuns []cfTunnel
	q := url.Values{"name": {name}, "is_deleted": {"false"}}
	if err := p.client.do(ctx, "GET", "/accounts/"+acct+"/cfd_tunnel", q, nil, &tuns); err != nil {
		return "", err
	}
	for i := range tuns {
		if tuns[i].Name == name {
			return tuns[i].ID, nil
		}
	}
	return "", pluginproto.NotFound(fmt.Sprintf("Tunnel %q not found in Cloudflare", name))
}

// tunnelIngress converts spec.ingress into the Cloudflare ingress rule list,
// appending a catch-all final rule when the last user rule has a hostname
// (Cloudflare rejects a config whose last rule is host-scoped).
func tunnelIngress(spec map[string]any) []map[string]any {
	raw, _ := spec["ingress"].([]any)
	if len(raw) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(raw)+1)
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		service := specString(m, "service")
		if service == "" {
			continue // service is required for a rule to be meaningful
		}
		rule := map[string]any{"service": service}
		if h := specString(m, "hostname"); h != "" {
			rule["hostname"] = h
		}
		if pth := specString(m, "path"); pth != "" {
			rule["path"] = pth
		}
		out = append(out, rule)
	}
	if len(out) == 0 {
		return nil
	}
	if _, hasHost := out[len(out)-1]["hostname"]; hasHost {
		out = append(out, map[string]any{"service": "http_status:404"})
	}
	return out
}

// tunnelObserved builds the observed Resource for a tunnel. The run token is
// deliberately NOT included (it's a secret; fetch it via the get-token action).
func tunnelObserved(name string, tun *cfTunnel, acct string) *protocol.Resource {
	status := map[string]any{"id": tun.ID, "name": tun.Name}
	if tun.Status != "" {
		status["connectionStatus"] = tun.Status
	}
	if tun.CreatedAt != "" {
		status["createdAt"] = tun.CreatedAt
	}
	r := &protocol.Resource{
		APIVersion: apiVersion,
		Kind:       kindTunnel,
		Spec:       map[string]any{"accountId": acct},
		Status:     status,
	}
	r.Metadata.Name = name
	return r
}
