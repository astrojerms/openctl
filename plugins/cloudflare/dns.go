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

// dnsState is the opaque blob openctl persists for a DNSRecord: the
// Cloudflare-assigned record ID plus the zone it lives in. Everything the
// provider needs to Get/Update/Delete the record later.
type dnsState struct {
	RecordID string `json:"recordId"`
	ZoneID   string `json:"zoneId"`
}

// cfDNSRecord is the Cloudflare DNS record wire shape (subset we manage).
type cfDNSRecord struct {
	ID         string `json:"id,omitempty"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	TTL        int    `json:"ttl,omitempty"`
	Proxied    *bool  `json:"proxied,omitempty"`
	Priority   *int   `json:"priority,omitempty"`
	Comment    string `json:"comment,omitempty"`
	CreatedOn  string `json:"created_on,omitempty"`
	ModifiedOn string `json:"modified_on,omitempty"`
}

// dnsRecordFromSpec builds the Cloudflare record body from a manifest spec and
// resolves the effective zone (spec.zoneId, else the provider default).
func (p *provider) dnsRecordFromSpec(spec map[string]any) (cfDNSRecord, string, error) {
	rec := cfDNSRecord{
		Type:    specString(spec, "type"),
		Name:    specString(spec, "name"),
		Content: specString(spec, "content"),
		Comment: specString(spec, "comment"),
	}
	if rec.Type == "" || rec.Name == "" {
		return rec, "", fmt.Errorf("spec.type and spec.name are required for a DNSRecord")
	}
	if ttl, ok := specInt(spec, "ttl"); ok {
		rec.TTL = ttl
	}
	if proxied, ok := specBool(spec, "proxied"); ok {
		rec.Proxied = &proxied
	}
	if prio, ok := specInt(spec, "priority"); ok {
		rec.Priority = &prio
	}
	zoneID := specString(spec, "zoneId")
	if zoneID == "" {
		zoneID = p.cfg.Defaults["zoneId"]
	}
	return rec, zoneID, nil
}

func (p *provider) applyDNSRecord(ctx context.Context, m *protocol.Resource, state json.RawMessage) (*pluginproto.ApplyResult, error) {
	rec, zoneID, err := p.dnsRecordFromSpec(m.Spec)
	if err != nil {
		return nil, err
	}
	if zoneID == "" {
		return nil, fmt.Errorf("no zone for DNSRecord %q (set spec.zoneId or provider defaults.zoneId)", m.Metadata.Name)
	}

	var st dnsState
	if len(state) > 0 {
		_ = json.Unmarshal(state, &st)
	}

	var out cfDNSRecord
	switch st.RecordID {
	case "":
		// First apply: create.
		if err := p.client.do(ctx, "POST", "/zones/"+zoneID+"/dns_records", nil, rec, &out); err != nil {
			return nil, err
		}
	default:
		// Subsequent apply: update in place. If the record was deleted
		// out-of-band, recreate it so apply is convergent.
		err := p.client.do(ctx, "PUT", "/zones/"+zoneID+"/dns_records/"+st.RecordID, nil, rec, &out)
		if errors.Is(err, errNotFound) {
			err = p.client.do(ctx, "POST", "/zones/"+zoneID+"/dns_records", nil, rec, &out)
		}
		if err != nil {
			return nil, err
		}
	}

	newState, _ := json.Marshal(dnsState{RecordID: out.ID, ZoneID: zoneID})
	return &pluginproto.ApplyResult{Resource: dnsObserved(m.Metadata.Name, &out, zoneID), State: newState}, nil
}

func (p *provider) getDNSRecord(ctx context.Context, name string, state json.RawMessage) (*pluginproto.GetResult, error) {
	var st dnsState
	if len(state) > 0 {
		_ = json.Unmarshal(state, &st)
	}
	if st.RecordID == "" || st.ZoneID == "" {
		return nil, pluginproto.NotFound(fmt.Sprintf("DNSRecord %q has no prior state", name))
	}
	var out cfDNSRecord
	if err := p.client.do(ctx, "GET", "/zones/"+st.ZoneID+"/dns_records/"+st.RecordID, nil, nil, &out); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, pluginproto.NotFound(fmt.Sprintf("DNSRecord %q not found in Cloudflare", name))
		}
		return nil, err
	}
	newState, _ := json.Marshal(dnsState{RecordID: out.ID, ZoneID: st.ZoneID})
	return &pluginproto.GetResult{Resource: dnsObserved(name, &out, st.ZoneID), State: newState}, nil
}

func (p *provider) listDNSRecords(ctx context.Context) ([]*protocol.Resource, error) {
	zoneID := p.cfg.Defaults["zoneId"]
	if zoneID == "" {
		// No default zone means we can't enumerate; a lean, honest empty list.
		return nil, nil
	}
	var recs []cfDNSRecord
	if err := p.client.do(ctx, "GET", "/zones/"+zoneID+"/dns_records", url.Values{"per_page": {"100"}}, nil, &recs); err != nil {
		return nil, err
	}
	out := make([]*protocol.Resource, 0, len(recs))
	for i := range recs {
		// List is a live inventory independent of manifest names; key by the
		// stable record ID to avoid collisions among same-name records.
		out = append(out, dnsObserved(recs[i].ID, &recs[i], zoneID))
	}
	return out, nil
}

func (p *provider) deleteDNSRecord(ctx context.Context, state json.RawMessage) error {
	var st dnsState
	if len(state) > 0 {
		_ = json.Unmarshal(state, &st)
	}
	if st.RecordID == "" || st.ZoneID == "" {
		return nil // never created / no state — nothing to delete
	}
	err := p.client.do(ctx, "DELETE", "/zones/"+st.ZoneID+"/dns_records/"+st.RecordID, nil, nil, nil)
	if err != nil && !errors.Is(err, errNotFound) {
		return err
	}
	return nil // idempotent: already gone == deleted
}

// dnsObserved builds the observed Resource for a DNS record: spec echoes the
// record's managed fields (so drift compares), status carries server metadata.
func dnsObserved(name string, rec *cfDNSRecord, zoneID string) *protocol.Resource {
	spec := map[string]any{
		"zoneId":  zoneID,
		"type":    rec.Type,
		"name":    rec.Name,
		"content": rec.Content,
	}
	if rec.TTL != 0 {
		spec["ttl"] = rec.TTL
	}
	if rec.Proxied != nil {
		spec["proxied"] = *rec.Proxied
	}
	if rec.Priority != nil {
		spec["priority"] = *rec.Priority
	}
	status := map[string]any{"id": rec.ID, "zoneId": zoneID}
	if rec.CreatedOn != "" {
		status["createdOn"] = rec.CreatedOn
	}
	if rec.ModifiedOn != "" {
		status["modifiedOn"] = rec.ModifiedOn
	}
	r := &protocol.Resource{APIVersion: apiVersion, Kind: kindDNSRecord, Spec: spec, Status: status}
	r.Metadata.Name = name
	return r
}
