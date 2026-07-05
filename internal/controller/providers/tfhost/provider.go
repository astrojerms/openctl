package tfhost

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
	"github.com/openctl/openctl/pkg/tfplugin6"
)

// StateStore is the provider_state persistence contract the TF host needs.
// internal/controller/providerstate.Store satisfies it.
type StateStore interface {
	LoadState(ctx context.Context, apiVersion, kind, name string) (state, private []byte, schemaVersion int, err error)
	SaveState(ctx context.Context, apiVersion, kind, name string, state, private []byte, schemaVersion int) error
	DeleteState(ctx context.Context, apiVersion, kind, name string) error
}

// ResourceMapping binds an openctl Kind to a Terraform resource type name.
// Terraform providers expose names like "aws_instance"; openctl manifests use
// Kubernetes-style Kinds like "Instance".
type ResourceMapping struct {
	Kind     string
	TypeName string
}

// Provider adapts one launched Terraform provider process to openctl's
// providers.Provider contract.
type Provider struct {
	client     *Client
	name       string
	apiVersion string
	store      StateStore

	typeByKind   map[string]string
	schemaByKind map[string]*tfplugin6.Schema
}

// NewProvider constructs a Terraform-hosted provider adapter from an already
// launched Client and a set of explicit resource mappings.
func NewProvider(ctx context.Context, name string, client *Client, store StateStore, mappings []ResourceMapping) (*Provider, error) {
	if name == "" {
		return nil, fmt.Errorf("tfhost provider name is required")
	}
	if client == nil {
		return nil, fmt.Errorf("tfhost client is required")
	}
	if store == nil {
		return nil, fmt.Errorf("tfhost state store is required")
	}
	if len(mappings) == 0 {
		return nil, fmt.Errorf("tfhost provider requires at least one resource mapping")
	}

	schemaResp, err := client.GetProviderSchema(ctx)
	if err != nil {
		return nil, err
	}
	if err := diagnosticsError("GetProviderSchema", schemaResp.GetDiagnostics()); err != nil {
		return nil, err
	}

	p := &Provider{
		client:       client,
		name:         name,
		apiVersion:   name + ".openctl.io/v1",
		store:        store,
		typeByKind:   map[string]string{},
		schemaByKind: map[string]*tfplugin6.Schema{},
	}
	for _, m := range mappings {
		if m.Kind == "" || m.TypeName == "" {
			return nil, fmt.Errorf("tfhost resource mapping requires kind and typeName")
		}
		schema, ok := schemaResp.GetResourceSchemas()[m.TypeName]
		if !ok {
			return nil, fmt.Errorf("terraform resource type %q for kind %q not offered by provider", m.TypeName, m.Kind)
		}
		p.typeByKind[m.Kind] = m.TypeName
		p.schemaByKind[m.Kind] = schema
	}
	return p, nil
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Kinds() []string {
	out := make([]string, 0, len(p.typeByKind))
	for kind := range p.typeByKind {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

func (p *Provider) Apply(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error) {
	typeName, schema, err := p.lookup(manifest.Kind)
	if err != nil {
		return nil, err
	}
	config, err := configDynamicValue(schema, manifest.Spec)
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	state, private, _, err := p.store.LoadState(ctx, p.apiVersion, manifest.Kind, manifest.Metadata.Name)
	if err != nil {
		return nil, err
	}
	prior, err := storedDynamicValue(state)
	if err != nil {
		return nil, err
	}

	plan, err := p.client.PlanResourceChange(ctx, &tfplugin6.PlanResourceChange_Request{
		TypeName:         typeName,
		PriorState:       prior,
		ProposedNewState: config,
		Config:           config,
		PriorPrivate:     private,
	})
	if err != nil {
		return nil, err
	}
	if err := diagnosticsError("PlanResourceChange", plan.GetDiagnostics()); err != nil {
		return nil, err
	}

	applied, err := p.client.ApplyResourceChange(ctx, &tfplugin6.ApplyResourceChange_Request{
		TypeName:       typeName,
		PriorState:     prior,
		PlannedState:   plan.GetPlannedState(),
		Config:         config,
		PlannedPrivate: plan.GetPlannedPrivate(),
	})
	if err != nil {
		return nil, err
	}
	if err := diagnosticsError("ApplyResourceChange", applied.GetDiagnostics()); err != nil {
		return nil, err
	}
	blob, err := marshalDynamicValue(applied.GetNewState())
	if err != nil {
		return nil, err
	}
	if err := p.store.SaveState(ctx, p.apiVersion, manifest.Kind, manifest.Metadata.Name, blob, applied.GetPrivate(), int(schema.GetVersion())); err != nil {
		return nil, fmt.Errorf("persist provider state: %w", err)
	}
	return observedResource(p.apiVersion, manifest.Kind, manifest.Metadata.Name, manifest.Spec, schema, applied.GetNewState()), nil
}

func (p *Provider) Get(ctx context.Context, kind, name string) (*protocol.Resource, error) {
	typeName, schema, err := p.lookup(kind)
	if err != nil {
		return nil, err
	}
	state, private, _, err := p.store.LoadState(ctx, p.apiVersion, kind, name)
	if err != nil {
		return nil, err
	}
	if len(state) == 0 {
		return nil, providers.NotFound(kind, name)
	}
	current, err := storedDynamicValue(state)
	if err != nil {
		return nil, err
	}

	read, err := p.client.ReadResource(ctx, &tfplugin6.ReadResource_Request{
		TypeName:     typeName,
		CurrentState: current,
		Private:      private,
	})
	if err != nil {
		return nil, err
	}
	if err := diagnosticsError("ReadResource", read.GetDiagnostics()); err != nil {
		return nil, err
	}
	if isNullDynamicValue(read.GetNewState()) {
		if err := p.store.DeleteState(ctx, p.apiVersion, kind, name); err != nil {
			return nil, fmt.Errorf("delete missing provider state: %w", err)
		}
		return nil, providers.NotFound(kind, name)
	}
	blob, err := marshalDynamicValue(read.GetNewState())
	if err != nil {
		return nil, err
	}
	if err := p.store.SaveState(ctx, p.apiVersion, kind, name, blob, read.GetPrivate(), int(schema.GetVersion())); err != nil {
		return nil, fmt.Errorf("persist refreshed provider state: %w", err)
	}

	spec, status := splitState(schema, read.GetNewState())
	status["phase"] = "Ready"
	return &protocol.Resource{
		APIVersion: p.apiVersion,
		Kind:       kind,
		Metadata:   protocol.ResourceMetadata{Name: name},
		Spec:       spec,
		Status:     status,
	}, nil
}

func (p *Provider) List(_ context.Context, kind string) ([]*protocol.Resource, error) {
	if _, _, err := p.lookup(kind); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("tfhost provider %q cannot list Terraform resources; use Get for managed resources", p.name)
}

func (p *Provider) Delete(ctx context.Context, kind, name string) error {
	typeName, _, err := p.lookup(kind)
	if err != nil {
		return err
	}
	state, private, _, err := p.store.LoadState(ctx, p.apiVersion, kind, name)
	if err != nil {
		return err
	}
	if len(state) == 0 {
		return nil
	}
	prior, err := storedDynamicValue(state)
	if err != nil {
		return err
	}
	null := nullDynamicValue()

	plan, err := p.client.PlanResourceChange(ctx, &tfplugin6.PlanResourceChange_Request{
		TypeName:         typeName,
		PriorState:       prior,
		ProposedNewState: null,
		Config:           null,
		PriorPrivate:     private,
	})
	if err != nil {
		return err
	}
	if err := diagnosticsError("PlanResourceChange delete", plan.GetDiagnostics()); err != nil {
		return err
	}

	applied, err := p.client.ApplyResourceChange(ctx, &tfplugin6.ApplyResourceChange_Request{
		TypeName:       typeName,
		PriorState:     prior,
		PlannedState:   plan.GetPlannedState(),
		Config:         null,
		PlannedPrivate: plan.GetPlannedPrivate(),
	})
	if err != nil {
		return err
	}
	if err := diagnosticsError("ApplyResourceChange delete", applied.GetDiagnostics()); err != nil {
		return err
	}
	if err := p.store.DeleteState(ctx, p.apiVersion, kind, name); err != nil {
		return fmt.Errorf("delete provider state: %w", err)
	}
	return nil
}

func (p *Provider) lookup(kind string) (string, *tfplugin6.Schema, error) {
	typeName, ok := p.typeByKind[kind]
	if !ok {
		return "", nil, fmt.Errorf("tfhost provider %q does not handle kind %q", p.name, kind)
	}
	return typeName, p.schemaByKind[kind], nil
}

func configDynamicValue(schema *tfplugin6.Schema, spec map[string]any) (*tfplugin6.DynamicValue, error) {
	attrs, err := SchemaAttributes(schema)
	if err != nil {
		return nil, err
	}
	body := make(map[string]any, len(attrs))
	for _, attr := range attrs {
		v, ok := spec[attr.Name]
		if !ok {
			if attr.Required {
				return nil, fmt.Errorf("required attribute %q missing from spec", attr.Name)
			}
			body[attr.Name] = nil
			continue
		}
		body[attr.Name] = v
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &tfplugin6.DynamicValue{Json: b}, nil
}

func marshalDynamicValue(dv *tfplugin6.DynamicValue) ([]byte, error) {
	if dv == nil {
		dv = nullDynamicValue()
	}
	return proto.Marshal(dv)
}

func storedDynamicValue(blob []byte) (*tfplugin6.DynamicValue, error) {
	if len(blob) == 0 {
		return nullDynamicValue(), nil
	}
	var dv tfplugin6.DynamicValue
	if err := proto.Unmarshal(blob, &dv); err != nil {
		return nil, fmt.Errorf("decode stored DynamicValue: %w", err)
	}
	return &dv, nil
}

func nullDynamicValue() *tfplugin6.DynamicValue {
	return &tfplugin6.DynamicValue{Json: []byte("null")}
}

func isNullDynamicValue(dv *tfplugin6.DynamicValue) bool {
	if dv == nil {
		return true
	}
	if string(dv.GetJson()) == "null" {
		return true
	}
	return len(dv.GetJson()) == 0 && len(dv.GetMsgpack()) == 0
}

func observedResource(apiVersion, kind, name string, spec map[string]any, schema *tfplugin6.Schema, state *tfplugin6.DynamicValue) *protocol.Resource {
	status := map[string]any{"phase": "Ready"}
	_, computed := splitState(schema, state)
	for k, v := range computed {
		status[k] = v
	}
	return &protocol.Resource{
		APIVersion: apiVersion,
		Kind:       kind,
		Metadata:   protocol.ResourceMetadata{Name: name},
		Spec:       cloneMap(spec),
		Status:     status,
	}
}

func splitState(schema *tfplugin6.Schema, state *tfplugin6.DynamicValue) (map[string]any, map[string]any) {
	spec := map[string]any{}
	status := map[string]any{}
	if state == nil || len(state.GetJson()) == 0 || isNullDynamicValue(state) {
		return spec, status
	}
	var fields map[string]any
	if err := json.Unmarshal(state.GetJson(), &fields); err != nil {
		status["state_json_error"] = err.Error()
		return spec, status
	}
	computed := map[string]bool{}
	if attrs, err := SchemaAttributes(schema); err == nil {
		for _, attr := range attrs {
			computed[attr.Name] = attr.Computed && !attr.Optional && !attr.Required
		}
	}
	for k, v := range fields {
		if v == nil {
			continue
		}
		if computed[k] {
			status[k] = v
		} else {
			spec[k] = v
		}
	}
	return spec, status
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func diagnosticsError(rpc string, diags []*tfplugin6.Diagnostic) error {
	var parts []string
	for _, d := range diags {
		if d.GetSeverity() != tfplugin6.Diagnostic_ERROR {
			continue
		}
		msg := strings.TrimSpace(d.GetSummary())
		if detail := strings.TrimSpace(d.GetDetail()); detail != "" {
			if msg != "" {
				msg += ": "
			}
			msg += detail
		}
		if msg == "" {
			msg = "unspecified diagnostic"
		}
		parts = append(parts, msg)
	}
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("%s diagnostics: %s", rpc, strings.Join(parts, "; "))
}
