// Command tf-fake is a small Terraform plugin-protocol-v6 provider used to
// test openctl's Terraform host (internal/controller/providers/tfhost) without
// downloading a real terraform-provider-* binary. It is served through
// terraform-plugin-go's public tf6server adapter, so the provider side of the
// wire uses the same code path as real providers.
package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const (
	providerAddress = "registry.openctl.test/openctl/fake"
	resourceType    = "fake_thing"
)

var (
	providerSchema = &tfprotov6.Schema{
		Version: 0,
		Block: &tfprotov6.SchemaBlock{
			Attributes: []*tfprotov6.SchemaAttribute{
				{Name: "endpoint", Type: tftypes.String, Optional: true},
			},
		},
	}

	// thingSchema carries a nested "network" block (list nesting) and typed
	// attributes so openctl's tfhost exercises nested-block config encoding and
	// msgpack state decoding against a real tf6server provider — the gaps the
	// old JSON-only fake could not reach.
	thingSchema = &tfprotov6.Schema{
		Version: 1,
		Block: &tfprotov6.SchemaBlock{
			Attributes: []*tfprotov6.SchemaAttribute{
				{Name: "name", Type: tftypes.String, Required: true},
				{Name: "note", Type: tftypes.String, Optional: true},
				{Name: "id", Type: tftypes.String, Computed: true},
			},
			BlockTypes: []*tfprotov6.SchemaNestedBlock{
				{
					TypeName: "network",
					Nesting:  tfprotov6.SchemaNestedBlockNestingModeList,
					Block: &tfprotov6.SchemaBlock{
						Attributes: []*tfprotov6.SchemaAttribute{
							{Name: "subnet", Type: tftypes.String, Required: true},
							{Name: "public", Type: tftypes.Bool, Optional: true},
						},
					},
				},
			},
		},
	}

	networkType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"subnet": tftypes.String,
		"public": tftypes.Bool,
	}}

	thingType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"name":    tftypes.String,
		"note":    tftypes.String,
		"id":      tftypes.String,
		"network": tftypes.List{ElementType: networkType},
	}}

	providerConfigType = tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"endpoint": tftypes.String,
	}}
)

type network struct {
	Subnet string
	Public bool
}

type fakeThing struct {
	Name     string
	Note     string
	ID       string
	Networks []network
}

type fakeProviderConfig struct {
	Endpoint string `json:"endpoint"`
}

type fakeProvider struct {
	mu       sync.Mutex
	endpoint string
	things   map[string]fakeThing
}

func newFakeProvider() tfprotov6.ProviderServer {
	return &fakeProvider{things: map[string]fakeThing{}}
}

func (p *fakeProvider) GetMetadata(context.Context, *tfprotov6.GetMetadataRequest) (*tfprotov6.GetMetadataResponse, error) {
	return &tfprotov6.GetMetadataResponse{
		Resources: []tfprotov6.ResourceMetadata{{TypeName: resourceType}},
	}, nil
}

func (p *fakeProvider) GetProviderSchema(context.Context, *tfprotov6.GetProviderSchemaRequest) (*tfprotov6.GetProviderSchemaResponse, error) {
	return &tfprotov6.GetProviderSchemaResponse{
		Provider: providerSchema,
		ResourceSchemas: map[string]*tfprotov6.Schema{
			resourceType: thingSchema,
		},
	}, nil
}

func (p *fakeProvider) GetResourceIdentitySchemas(context.Context, *tfprotov6.GetResourceIdentitySchemasRequest) (*tfprotov6.GetResourceIdentitySchemasResponse, error) {
	return &tfprotov6.GetResourceIdentitySchemasResponse{}, nil
}

func (p *fakeProvider) ValidateProviderConfig(context.Context, *tfprotov6.ValidateProviderConfigRequest) (*tfprotov6.ValidateProviderConfigResponse, error) {
	return &tfprotov6.ValidateProviderConfigResponse{}, nil
}

func (p *fakeProvider) ConfigureProvider(_ context.Context, req *tfprotov6.ConfigureProviderRequest) (*tfprotov6.ConfigureProviderResponse, error) {
	cfg, err := decodeProviderConfig(req.Config)
	if err != nil {
		return &tfprotov6.ConfigureProviderResponse{Diagnostics: diagError("Invalid provider config", err.Error())}, nil
	}
	p.mu.Lock()
	p.endpoint = cfg.Endpoint
	p.mu.Unlock()
	return &tfprotov6.ConfigureProviderResponse{}, nil
}

func (p *fakeProvider) StopProvider(context.Context, *tfprotov6.StopProviderRequest) (*tfprotov6.StopProviderResponse, error) {
	return &tfprotov6.StopProviderResponse{}, nil
}

func (p *fakeProvider) ValidateResourceConfig(context.Context, *tfprotov6.ValidateResourceConfigRequest) (*tfprotov6.ValidateResourceConfigResponse, error) {
	return &tfprotov6.ValidateResourceConfigResponse{}, nil
}

func (p *fakeProvider) UpgradeResourceState(context.Context, *tfprotov6.UpgradeResourceStateRequest) (*tfprotov6.UpgradeResourceStateResponse, error) {
	return &tfprotov6.UpgradeResourceStateResponse{}, nil
}

func (p *fakeProvider) UpgradeResourceIdentity(context.Context, *tfprotov6.UpgradeResourceIdentityRequest) (*tfprotov6.UpgradeResourceIdentityResponse, error) {
	return &tfprotov6.UpgradeResourceIdentityResponse{}, nil
}

func (p *fakeProvider) ReadResource(_ context.Context, req *tfprotov6.ReadResourceRequest) (*tfprotov6.ReadResourceResponse, error) {
	if diag := checkType(req.TypeName); diag != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diag}, nil
	}
	current, isNull, err := decodeThing(req.CurrentState)
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("Invalid current state", err.Error())}, nil
	}
	if isNull {
		return &tfprotov6.ReadResourceResponse{NewState: nullDynamicValue(), Private: req.Private}, nil
	}

	p.mu.Lock()
	observed, ok := p.things[current.Name]
	p.mu.Unlock()
	if !ok {
		return &tfprotov6.ReadResourceResponse{NewState: nullDynamicValue(), Private: req.Private}, nil
	}
	dv, err := encodeThing(observed)
	if err != nil {
		return nil, err
	}
	return &tfprotov6.ReadResourceResponse{NewState: dv, Private: []byte("read:" + observed.ID)}, nil
}

func (p *fakeProvider) PlanResourceChange(_ context.Context, req *tfprotov6.PlanResourceChangeRequest) (*tfprotov6.PlanResourceChangeResponse, error) {
	if diag := checkType(req.TypeName); diag != nil {
		return &tfprotov6.PlanResourceChangeResponse{Diagnostics: diag}, nil
	}
	proposed, isDelete, err := decodeThing(req.ProposedNewState)
	if err != nil {
		return &tfprotov6.PlanResourceChangeResponse{Diagnostics: diagError("Invalid proposed state", err.Error())}, nil
	}
	if isDelete {
		return &tfprotov6.PlanResourceChangeResponse{
			PlannedState:   nullDynamicValue(),
			PlannedPrivate: append([]byte(nil), req.PriorPrivate...),
		}, nil
	}

	if proposed.ID == "" {
		proposed.ID = "fake-" + proposed.Name
	}
	p.mu.Lock()
	endpoint := p.endpoint
	p.mu.Unlock()
	if proposed.Note == "" {
		proposed.Note = endpoint
	}
	dv, err := encodeThing(proposed)
	if err != nil {
		return nil, err
	}
	return &tfprotov6.PlanResourceChangeResponse{
		PlannedState:   dv,
		PlannedPrivate: []byte("plan:" + proposed.ID),
	}, nil
}

func (p *fakeProvider) ApplyResourceChange(_ context.Context, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	if diag := checkType(req.TypeName); diag != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diag}, nil
	}
	planned, isDelete, err := decodeThing(req.PlannedState)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("Invalid planned state", err.Error())}, nil
	}
	if isDelete {
		prior, priorNull, err := decodeThing(req.PriorState)
		if err != nil {
			return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("Invalid prior state", err.Error())}, nil
		}
		if !priorNull {
			p.mu.Lock()
			delete(p.things, prior.Name)
			p.mu.Unlock()
		}
		return &tfprotov6.ApplyResourceChangeResponse{NewState: nullDynamicValue()}, nil
	}

	if planned.ID == "" {
		planned.ID = "fake-" + planned.Name
	}
	p.mu.Lock()
	p.things[planned.Name] = planned
	p.mu.Unlock()

	dv, err := encodeThing(planned)
	if err != nil {
		return nil, err
	}
	return &tfprotov6.ApplyResourceChangeResponse{
		NewState: dv,
		Private:  []byte("state:" + planned.ID),
	}, nil
}

func (p *fakeProvider) ImportResourceState(context.Context, *tfprotov6.ImportResourceStateRequest) (*tfprotov6.ImportResourceStateResponse, error) {
	return &tfprotov6.ImportResourceStateResponse{}, nil
}

func (p *fakeProvider) MoveResourceState(context.Context, *tfprotov6.MoveResourceStateRequest) (*tfprotov6.MoveResourceStateResponse, error) {
	return &tfprotov6.MoveResourceStateResponse{}, nil
}

func (p *fakeProvider) GenerateResourceConfig(context.Context, *tfprotov6.GenerateResourceConfigRequest) (*tfprotov6.GenerateResourceConfigResponse, error) {
	return &tfprotov6.GenerateResourceConfigResponse{}, nil
}

func (p *fakeProvider) ValidateDataResourceConfig(context.Context, *tfprotov6.ValidateDataResourceConfigRequest) (*tfprotov6.ValidateDataResourceConfigResponse, error) {
	return &tfprotov6.ValidateDataResourceConfigResponse{}, nil
}

func (p *fakeProvider) ReadDataSource(context.Context, *tfprotov6.ReadDataSourceRequest) (*tfprotov6.ReadDataSourceResponse, error) {
	return &tfprotov6.ReadDataSourceResponse{}, nil
}

func (p *fakeProvider) GetFunctions(context.Context, *tfprotov6.GetFunctionsRequest) (*tfprotov6.GetFunctionsResponse, error) {
	return &tfprotov6.GetFunctionsResponse{}, nil
}

func (p *fakeProvider) CallFunction(context.Context, *tfprotov6.CallFunctionRequest) (*tfprotov6.CallFunctionResponse, error) {
	return &tfprotov6.CallFunctionResponse{}, nil
}

func (p *fakeProvider) ValidateEphemeralResourceConfig(context.Context, *tfprotov6.ValidateEphemeralResourceConfigRequest) (*tfprotov6.ValidateEphemeralResourceConfigResponse, error) {
	return &tfprotov6.ValidateEphemeralResourceConfigResponse{}, nil
}

func (p *fakeProvider) OpenEphemeralResource(context.Context, *tfprotov6.OpenEphemeralResourceRequest) (*tfprotov6.OpenEphemeralResourceResponse, error) {
	return &tfprotov6.OpenEphemeralResourceResponse{}, nil
}

func (p *fakeProvider) RenewEphemeralResource(context.Context, *tfprotov6.RenewEphemeralResourceRequest) (*tfprotov6.RenewEphemeralResourceResponse, error) {
	return &tfprotov6.RenewEphemeralResourceResponse{}, nil
}

func (p *fakeProvider) CloseEphemeralResource(context.Context, *tfprotov6.CloseEphemeralResourceRequest) (*tfprotov6.CloseEphemeralResourceResponse, error) {
	return &tfprotov6.CloseEphemeralResourceResponse{}, nil
}

func decodeThing(dv *tfprotov6.DynamicValue) (fakeThing, bool, error) {
	if dv == nil {
		return fakeThing{}, true, nil
	}
	isNull, err := dv.IsNull()
	if err != nil {
		return fakeThing{}, false, err
	}
	if isNull {
		return fakeThing{}, true, nil
	}
	v, err := dv.Unmarshal(thingType)
	if err != nil {
		return fakeThing{}, false, err
	}
	attrs := map[string]tftypes.Value{}
	if err := v.As(&attrs); err != nil {
		return fakeThing{}, false, err
	}

	var thing fakeThing
	if err := stringAttr(attrs, "name", &thing.Name); err != nil {
		return fakeThing{}, false, err
	}
	if err := stringAttr(attrs, "note", &thing.Note); err != nil {
		return fakeThing{}, false, err
	}
	if err := stringAttr(attrs, "id", &thing.ID); err != nil {
		return fakeThing{}, false, err
	}
	nets, err := decodeNetworks(attrs["network"])
	if err != nil {
		return fakeThing{}, false, err
	}
	thing.Networks = nets
	return thing, false, nil
}

func decodeNetworks(v tftypes.Value) ([]network, error) {
	if !v.IsKnown() || v.IsNull() {
		return nil, nil
	}
	var list []tftypes.Value
	if err := v.As(&list); err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	out := make([]network, 0, len(list))
	for i, item := range list {
		var attrs map[string]tftypes.Value
		if err := item.As(&attrs); err != nil {
			return nil, fmt.Errorf("network[%d]: %w", i, err)
		}
		var n network
		if err := stringAttr(attrs, "subnet", &n.Subnet); err != nil {
			return nil, err
		}
		if err := boolAttr(attrs, "public", &n.Public); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func decodeProviderConfig(dv *tfprotov6.DynamicValue) (fakeProviderConfig, error) {
	if dv == nil {
		return fakeProviderConfig{}, nil
	}
	isNull, err := dv.IsNull()
	if err != nil {
		return fakeProviderConfig{}, err
	}
	if isNull {
		return fakeProviderConfig{}, nil
	}
	v, err := dv.Unmarshal(providerConfigType)
	if err != nil {
		return fakeProviderConfig{}, err
	}
	attrs := map[string]tftypes.Value{}
	if err := v.As(&attrs); err != nil {
		return fakeProviderConfig{}, err
	}
	var cfg fakeProviderConfig
	if err := stringAttr(attrs, "endpoint", &cfg.Endpoint); err != nil {
		return fakeProviderConfig{}, err
	}
	return cfg, nil
}

func stringAttr(attrs map[string]tftypes.Value, name string, dst *string) error {
	v, ok := attrs[name]
	if !ok || v.IsNull() {
		return nil
	}
	if !v.IsKnown() {
		return fmt.Errorf("%s is unknown", name)
	}
	if err := v.As(dst); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func boolAttr(attrs map[string]tftypes.Value, name string, dst *bool) error {
	v, ok := attrs[name]
	if !ok || v.IsNull() || !v.IsKnown() {
		return nil
	}
	if err := v.As(dst); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// encodeThing marshals a thing to a msgpack DynamicValue via NewDynamicValue —
// the same encoding real framework providers use, so openctl's msgpack state
// decoding is exercised end-to-end.
func encodeThing(thing fakeThing) (*tfprotov6.DynamicValue, error) {
	networks := make([]tftypes.Value, 0, len(thing.Networks))
	for _, n := range thing.Networks {
		networks = append(networks, tftypes.NewValue(networkType, map[string]tftypes.Value{
			"subnet": tftypes.NewValue(tftypes.String, n.Subnet),
			"public": tftypes.NewValue(tftypes.Bool, n.Public),
		}))
	}
	val := tftypes.NewValue(thingType, map[string]tftypes.Value{
		"name":    tftypes.NewValue(tftypes.String, thing.Name),
		"note":    tftypes.NewValue(tftypes.String, thing.Note),
		"id":      tftypes.NewValue(tftypes.String, thing.ID),
		"network": tftypes.NewValue(tftypes.List{ElementType: networkType}, networks),
	})
	dv, err := tfprotov6.NewDynamicValue(thingType, val)
	if err != nil {
		return nil, err
	}
	return &dv, nil
}

func nullDynamicValue() *tfprotov6.DynamicValue {
	return &tfprotov6.DynamicValue{JSON: []byte("null")}
}

func checkType(typeName string) []*tfprotov6.Diagnostic {
	if typeName == resourceType {
		return nil
	}
	return diagError("Unsupported resource type", fmt.Sprintf("tf-fake only supports %q, got %q.", resourceType, typeName))
}

func diagError(summary, detail string) []*tfprotov6.Diagnostic {
	return []*tfprotov6.Diagnostic{{
		Severity: tfprotov6.DiagnosticSeverityError,
		Summary:  summary,
		Detail:   detail,
	}}
}

func main() {
	if os.Getenv("TF_PLUGIN_MAGIC_COOKIE") != "d602bf8f470bc67ca7faa0386276bbdd4330efaf76d1a219cb4d6991ca9872b2" {
		fmt.Fprintln(os.Stderr, "tf-fake is a Terraform-protocol test provider; run it via the openctl tfhost, not directly")
		os.Exit(2)
	}
	if err := tf6server.Serve(providerAddress, newFakeProvider); err != nil {
		fmt.Fprintf(os.Stderr, "serve tf-fake: %v\n", err)
		os.Exit(1)
	}
}
