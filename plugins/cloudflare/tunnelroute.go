package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// kindTunnelRoute is the "expose one app" primitive (G1). A Tunnel's ingress is
// a single ordered list, so managing it from N per-app resources would clobber
// (last-writer-wins). TunnelRoute instead contributes ONE host-scoped rule to a
// named Tunnel via a read-modify-write keyed by hostname, so many apps coexist
// on one tunnel — each owns its own resource. Pair it with a CNAME DNSRecord
// that $refs the Tunnel's status.cnameTarget to finish the public wiring.
const kindTunnelRoute = "TunnelRoute"

// tunnelRouteState records what a route owns so Delete can pull exactly its
// rule (and Get can probe for it) without re-reading the spec.
type tunnelRouteState struct {
	AccountID  string `json:"accountId"`
	TunnelID   string `json:"tunnelId"`
	TunnelName string `json:"tunnelName"`
	Hostname   string `json:"hostname"`
}

// cfTunnelConfig is a tunnel's ingress configuration as Cloudflare returns it
// from GET .../configurations.
type cfTunnelConfig struct {
	Config struct {
		Ingress []map[string]any `json:"ingress"`
	} `json:"config"`
}

// mergeIngressRule upserts a host-scoped rule into a tunnel's ingress list,
// keyed by hostname, and guarantees exactly one catch-all (http_status:404) as
// the final rule (Cloudflare rejects a config whose last rule is host-scoped).
// Rules for OTHER hostnames are preserved, so many TunnelRoutes coexist on one
// tunnel without clobbering each other. Pure/deterministic.
func mergeIngressRule(existing []map[string]any, hostname, service, path string) []map[string]any {
	out := make([]map[string]any, 0, len(existing)+2)
	for _, r := range existing {
		h, _ := r["hostname"].(string)
		if h == "" || h == hostname {
			continue // drop the catch-all and any prior rule for this hostname
		}
		out = append(out, r)
	}
	rule := map[string]any{"hostname": hostname, "service": service}
	if path != "" {
		rule["path"] = path
	}
	out = append(out, rule)
	out = append(out, map[string]any{"service": "http_status:404"})
	return out
}

// removeIngressRule drops the rule for hostname (and any catch-all) and
// re-appends a single trailing catch-all. Idempotent — removing an absent
// hostname just re-normalizes the catch-all.
func removeIngressRule(existing []map[string]any, hostname string) []map[string]any {
	out := make([]map[string]any, 0, len(existing)+1)
	for _, r := range existing {
		h, _ := r["hostname"].(string)
		if h == "" || h == hostname {
			continue
		}
		out = append(out, r)
	}
	out = append(out, map[string]any{"service": "http_status:404"})
	return out
}

func (p *provider) readTunnelIngress(ctx context.Context, acct, tunnelID string) ([]map[string]any, error) {
	var cfg cfTunnelConfig
	err := p.client.do(ctx, "GET", "/accounts/"+acct+"/cfd_tunnel/"+tunnelID+"/configurations", nil, nil, &cfg)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil // no configuration pushed yet
		}
		return nil, err
	}
	return cfg.Config.Ingress, nil
}

func (p *provider) writeTunnelIngress(ctx context.Context, acct, tunnelID string, ingress []map[string]any) error {
	body := map[string]any{"config": map[string]any{"ingress": ingress}}
	return p.client.do(ctx, "PUT", "/accounts/"+acct+"/cfd_tunnel/"+tunnelID+"/configurations", nil, body, nil)
}

// applyTunnelRoute upserts this route's ingress rule into the named tunnel.
//
// Concurrency note: this is a read-modify-write on the tunnel's shared config.
// Applies of the same resource are serialized, and routes are normally ordered
// after the Tunnel via a $ref; two routes to the SAME tunnel applied
// concurrently could race (last write wins). For a homelab that's acceptable;
// apply routes to one tunnel serially if it matters.
func (p *provider) applyTunnelRoute(ctx context.Context, m *protocol.Resource, _ json.RawMessage) (*pluginproto.ApplyResult, error) {
	acct := p.tunnelAccount(m.Spec)
	if acct == "" {
		return nil, fmt.Errorf("no account for TunnelRoute %q (set spec.accountId or provider defaults.accountId)", m.Metadata.Name)
	}
	tunnelName := specString(m.Spec, "tunnel")
	hostname := specString(m.Spec, "hostname")
	service := specString(m.Spec, "service")
	if tunnelName == "" || hostname == "" || service == "" {
		return nil, fmt.Errorf("TunnelRoute %q requires spec.tunnel, spec.hostname and spec.service", m.Metadata.Name)
	}
	path := specString(m.Spec, "path")

	tunnelID, err := p.findTunnelID(ctx, acct, tunnelName)
	if err != nil {
		return nil, err
	}
	ingress, err := p.readTunnelIngress(ctx, acct, tunnelID)
	if err != nil {
		return nil, err
	}
	if err := p.writeTunnelIngress(ctx, acct, tunnelID, mergeIngressRule(ingress, hostname, service, path)); err != nil {
		return nil, err
	}

	newState, _ := json.Marshal(tunnelRouteState{AccountID: acct, TunnelID: tunnelID, TunnelName: tunnelName, Hostname: hostname})
	return &pluginproto.ApplyResult{Resource: tunnelRouteObserved(m, tunnelID), State: newState}, nil
}

func (p *provider) getTunnelRoute(ctx context.Context, name string, state json.RawMessage) (*pluginproto.GetResult, error) {
	var st tunnelRouteState
	if len(state) > 0 {
		_ = json.Unmarshal(state, &st)
	}
	if st.TunnelID == "" || st.AccountID == "" || st.Hostname == "" {
		return nil, pluginproto.NotFound(fmt.Sprintf("TunnelRoute %q has no prior state", name))
	}
	ingress, err := p.readTunnelIngress(ctx, st.AccountID, st.TunnelID)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil, pluginproto.NotFound(fmt.Sprintf("TunnelRoute %q: tunnel config gone", name))
		}
		return nil, err
	}
	for _, r := range ingress {
		if h, _ := r["hostname"].(string); h == st.Hostname {
			res := &protocol.Resource{APIVersion: apiVersion, Kind: kindTunnelRoute, Status: map[string]any{
				"tunnelId": st.TunnelID, "hostname": st.Hostname, "phase": "Ready",
			}}
			res.Metadata.Name = name
			return &pluginproto.GetResult{Resource: res, State: state}, nil
		}
	}
	return nil, pluginproto.NotFound(fmt.Sprintf("TunnelRoute %q: no rule for %s in tunnel %s", name, st.Hostname, st.TunnelName))
}

func (p *provider) deleteTunnelRoute(ctx context.Context, state json.RawMessage) error {
	var st tunnelRouteState
	if len(state) > 0 {
		_ = json.Unmarshal(state, &st)
	}
	if st.TunnelID == "" || st.AccountID == "" || st.Hostname == "" {
		return nil
	}
	ingress, err := p.readTunnelIngress(ctx, st.AccountID, st.TunnelID)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil // tunnel/config already gone
		}
		return err
	}
	return p.writeTunnelIngress(ctx, st.AccountID, st.TunnelID, removeIngressRule(ingress, st.Hostname))
}

// tunnelRouteObserved echoes the desired spec plus a Ready status.
func tunnelRouteObserved(m *protocol.Resource, tunnelID string) *protocol.Resource {
	spec := make(map[string]any, len(m.Spec))
	maps.Copy(spec, m.Spec)
	r := &protocol.Resource{
		APIVersion: apiVersion,
		Kind:       kindTunnelRoute,
		Spec:       spec,
		Status: map[string]any{
			"tunnelId": tunnelID,
			"hostname": specString(m.Spec, "hostname"),
			"phase":    "Ready",
		},
	}
	r.Metadata.Name = m.Metadata.Name
	return r
}
