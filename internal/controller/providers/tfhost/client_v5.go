package tfhost

import (
	"context"

	"github.com/openctl/openctl/pkg/tfplugin5"
	"github.com/openctl/openctl/pkg/tfplugin6"
)

// v5adapter drives a protocol-5 (SDKv2) provider while presenting the tfplugin6
// surface the rest of tfhost uses. The v5 and v6 wire messages tfhost touches
// are field-identical (only the RPC names and a few v6-only additions differ),
// so conversion is a mechanical field copy. Schema conversion is v5->v6 only:
// v5 has no NestedType attributes (a v6 addition), which blockType already
// tolerates.
type v5adapter struct{ c tfplugin5.ProviderClient }

func (a v5adapter) GetProviderSchema(ctx context.Context) (*tfplugin6.GetProviderSchema_Response, error) {
	resp, err := a.c.GetSchema(ctx, &tfplugin5.GetProviderSchema_Request{})
	if err != nil {
		return nil, err
	}
	schemas := make(map[string]*tfplugin6.Schema, len(resp.GetResourceSchemas()))
	for name, s := range resp.GetResourceSchemas() {
		schemas[name] = v6Schema(s)
	}
	return &tfplugin6.GetProviderSchema_Response{
		Provider:        v6Schema(resp.GetProvider()),
		ResourceSchemas: schemas,
		Diagnostics:     v6Diags(resp.GetDiagnostics()),
	}, nil
}

func (a v5adapter) ConfigureProvider(ctx context.Context, req *tfplugin6.ConfigureProvider_Request) (*tfplugin6.ConfigureProvider_Response, error) {
	resp, err := a.c.Configure(ctx, &tfplugin5.Configure_Request{
		TerraformVersion: req.GetTerraformVersion(),
		Config:           v5DV(req.GetConfig()),
	})
	if err != nil {
		return nil, err
	}
	return &tfplugin6.ConfigureProvider_Response{Diagnostics: v6Diags(resp.GetDiagnostics())}, nil
}

func (a v5adapter) PlanResourceChange(ctx context.Context, req *tfplugin6.PlanResourceChange_Request) (*tfplugin6.PlanResourceChange_Response, error) {
	resp, err := a.c.PlanResourceChange(ctx, &tfplugin5.PlanResourceChange_Request{
		TypeName:         req.GetTypeName(),
		PriorState:       v5DV(req.GetPriorState()),
		ProposedNewState: v5DV(req.GetProposedNewState()),
		Config:           v5DV(req.GetConfig()),
		PriorPrivate:     req.GetPriorPrivate(),
	})
	if err != nil {
		return nil, err
	}
	return &tfplugin6.PlanResourceChange_Response{
		PlannedState:   v6DV(resp.GetPlannedState()),
		PlannedPrivate: resp.GetPlannedPrivate(),
		Diagnostics:    v6Diags(resp.GetDiagnostics()),
	}, nil
}

func (a v5adapter) ApplyResourceChange(ctx context.Context, req *tfplugin6.ApplyResourceChange_Request) (*tfplugin6.ApplyResourceChange_Response, error) {
	resp, err := a.c.ApplyResourceChange(ctx, &tfplugin5.ApplyResourceChange_Request{
		TypeName:       req.GetTypeName(),
		PriorState:     v5DV(req.GetPriorState()),
		PlannedState:   v5DV(req.GetPlannedState()),
		Config:         v5DV(req.GetConfig()),
		PlannedPrivate: req.GetPlannedPrivate(),
	})
	if err != nil {
		return nil, err
	}
	return &tfplugin6.ApplyResourceChange_Response{
		NewState:    v6DV(resp.GetNewState()),
		Private:     resp.GetPrivate(),
		Diagnostics: v6Diags(resp.GetDiagnostics()),
	}, nil
}

func (a v5adapter) ReadResource(ctx context.Context, req *tfplugin6.ReadResource_Request) (*tfplugin6.ReadResource_Response, error) {
	resp, err := a.c.ReadResource(ctx, &tfplugin5.ReadResource_Request{
		TypeName:     req.GetTypeName(),
		CurrentState: v5DV(req.GetCurrentState()),
		Private:      req.GetPrivate(),
	})
	if err != nil {
		return nil, err
	}
	return &tfplugin6.ReadResource_Response{
		NewState:    v6DV(resp.GetNewState()),
		Private:     resp.GetPrivate(),
		Diagnostics: v6Diags(resp.GetDiagnostics()),
	}, nil
}

// --- message conversions (only the fields tfhost uses) ---

func v5DV(d *tfplugin6.DynamicValue) *tfplugin5.DynamicValue {
	if d == nil {
		return nil
	}
	return &tfplugin5.DynamicValue{Msgpack: d.GetMsgpack(), Json: d.GetJson()}
}

func v6DV(d *tfplugin5.DynamicValue) *tfplugin6.DynamicValue {
	if d == nil {
		return nil
	}
	return &tfplugin6.DynamicValue{Msgpack: d.GetMsgpack(), Json: d.GetJson()}
}

func v6Diags(in []*tfplugin5.Diagnostic) []*tfplugin6.Diagnostic {
	if len(in) == 0 {
		return nil
	}
	out := make([]*tfplugin6.Diagnostic, 0, len(in))
	for _, d := range in {
		out = append(out, &tfplugin6.Diagnostic{
			Severity: tfplugin6.Diagnostic_Severity(d.GetSeverity()),
			Summary:  d.GetSummary(),
			Detail:   d.GetDetail(),
		})
	}
	return out
}

func v6Schema(s *tfplugin5.Schema) *tfplugin6.Schema {
	if s == nil {
		return nil
	}
	return &tfplugin6.Schema{Version: s.GetVersion(), Block: v6Block(s.GetBlock())}
}

func v6Block(b *tfplugin5.Schema_Block) *tfplugin6.Schema_Block {
	if b == nil {
		return nil
	}
	out := &tfplugin6.Schema_Block{
		Version:         b.GetVersion(),
		Description:     b.GetDescription(),
		DescriptionKind: tfplugin6.StringKind(b.GetDescriptionKind()),
		Deprecated:      b.GetDeprecated(),
	}
	for _, a := range b.GetAttributes() {
		out.Attributes = append(out.Attributes, v6Attr(a))
	}
	for _, bt := range b.GetBlockTypes() {
		out.BlockTypes = append(out.BlockTypes, v6NestedBlock(bt))
	}
	return out
}

func v6Attr(a *tfplugin5.Schema_Attribute) *tfplugin6.Schema_Attribute {
	if a == nil {
		return nil
	}
	// v5 has no NestedType (a v6-only object-attribute feature); its structured
	// data uses nested blocks, handled via BlockTypes.
	return &tfplugin6.Schema_Attribute{
		Name:            a.GetName(),
		Type:            a.GetType(),
		Description:     a.GetDescription(),
		Required:        a.GetRequired(),
		Optional:        a.GetOptional(),
		Computed:        a.GetComputed(),
		Sensitive:       a.GetSensitive(),
		DescriptionKind: tfplugin6.StringKind(a.GetDescriptionKind()),
		Deprecated:      a.GetDeprecated(),
	}
}

func v6NestedBlock(bt *tfplugin5.Schema_NestedBlock) *tfplugin6.Schema_NestedBlock {
	if bt == nil {
		return nil
	}
	return &tfplugin6.Schema_NestedBlock{
		TypeName: bt.GetTypeName(),
		Block:    v6Block(bt.GetBlock()),
		Nesting:  tfplugin6.Schema_NestedBlock_NestingMode(bt.GetNesting()),
		MinItems: bt.GetMinItems(),
		MaxItems: bt.GetMaxItems(),
	}
}
