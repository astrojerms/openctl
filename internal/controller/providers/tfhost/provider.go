package tfhost

import (
	"context"
	"fmt"
	"maps"
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

type providerOptions struct {
	providerConfig map[string]any
}

// ProviderOption customizes a Terraform-hosted provider adapter.
type ProviderOption func(*providerOptions)

// WithProviderConfig supplies provider-level Terraform config. Keys must match
// attributes in the Terraform provider schema.
func WithProviderConfig(config map[string]any) ProviderOption {
	return func(opts *providerOptions) {
		opts.providerConfig = cloneMap(config)
	}
}

// NewProvider constructs a Terraform-hosted provider adapter from an already
// launched Client and a set of explicit resource mappings.
func NewProvider(ctx context.Context, name string, client *Client, store StateStore, mappings []ResourceMapping, opts ...ProviderOption) (*Provider, error) {
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

	var options providerOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	schemaResp, err := client.GetProviderSchema(ctx)
	if err != nil {
		return nil, err
	}
	if err := diagnosticsError("GetProviderSchema", schemaResp.GetDiagnostics()); err != nil {
		return nil, err
	}

	config, err := configDynamicValue(schemaResp.GetProvider(), options.providerConfig)
	if err != nil {
		return nil, fmt.Errorf("encode provider config: %w", err)
	}
	configured, err := client.ConfigureProvider(ctx, &tfplugin6.ConfigureProvider_Request{
		TerraformVersion: "openctl",
		Config:           config,
	})
	if err != nil {
		return nil, err
	}
	if err := diagnosticsError("ConfigureProvider", configured.GetDiagnostics()); err != nil {
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
	if err := RegisterExternalSchemas(p.apiVersion, mappings, schemaResp.GetResourceSchemas()); err != nil {
		return nil, fmt.Errorf("register generated schemas: %w", err)
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

// configDynamicValue encodes an openctl spec into a msgpack DynamicValue typed
// by the resource schema (nested blocks included). See values.go.
func configDynamicValue(schema *tfplugin6.Schema, spec map[string]any) (*tfplugin6.DynamicValue, error) {
	return encodeConfig(schema, spec)
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
	if len(dv.GetJson()) == 0 && len(dv.GetMsgpack()) == 0 {
		return true
	}
	if string(dv.GetJson()) == "null" {
		return true
	}
	// A real (framework) provider signals "gone" with a msgpack-encoded null,
	// which is the single byte 0xc0 — detect it so ReadResource-returns-null is
	// honored regardless of encoding.
	if mp := dv.GetMsgpack(); len(mp) == 1 && mp[0] == 0xc0 {
		return true
	}
	return false
}

func observedResource(apiVersion, kind, name string, spec map[string]any, schema *tfplugin6.Schema, state *tfplugin6.DynamicValue) *protocol.Resource {
	status := map[string]any{"phase": "Ready"}
	observedSpec, computed := splitState(schema, state)
	if len(observedSpec) == 0 {
		observedSpec = cloneMap(spec)
	}
	maps.Copy(status, computed)
	return &protocol.Resource{
		APIVersion: apiVersion,
		Kind:       kind,
		Metadata:   protocol.ResourceMetadata{Name: name},
		Spec:       observedSpec,
		Status:     status,
	}
}

// splitState decodes a resource's DynamicValue state (msgpack or JSON) and
// classifies each field into spec (configurable attributes + nested blocks) vs
// status (computed-only attributes).
func splitState(schema *tfplugin6.Schema, state *tfplugin6.DynamicValue) (map[string]any, map[string]any) {
	spec := map[string]any{}
	status := map[string]any{}
	decoded, err := decodeState(schema, state)
	if err != nil {
		status["state_decode_error"] = err.Error()
		return spec, status
	}
	if decoded == nil {
		return spec, status
	}
	computed := computedOnlyAttrs(schema)
	for k, v := range decoded {
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

// computedOnlyAttrs returns the set of top-level attribute names that are
// computed and neither optional nor required (provider-owned outputs). Nested
// block names are absent here, so they classify as spec (configurable).
func computedOnlyAttrs(schema *tfplugin6.Schema) map[string]bool {
	out := map[string]bool{}
	if attrs, err := SchemaAttributes(schema); err == nil {
		for _, a := range attrs {
			out[a.Name] = a.Computed && !a.Optional && !a.Required
		}
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
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
