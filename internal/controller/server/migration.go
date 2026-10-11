package server

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/schema"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

const migrationBundleVersion = 1

func (h *resourceHandler) ExportResource(ctx context.Context, req *apiv1.ExportResourceRequest) (*apiv1.ExportResourceResponse, error) {
	if err := authorize(ctx, auth.RoleAdmin); err != nil {
		return nil, err
	}
	h.managementMu.Lock()
	defer h.managementMu.Unlock()
	if err := h.migrationReady(); err != nil {
		return nil, err
	}
	if req.GetApiVersion() == "" || req.GetKind() == "" || req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "api_version, kind, and name are required")
	}
	snapshot, err := h.manifests.ExportMigration(ctx, req.GetApiVersion(), req.GetKind(), req.GetName())
	if err != nil {
		return nil, migrationError(err)
	}
	wire, err := resourceToProto(snapshot.Resource)
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot encode migration manifest")
	}
	manifest, p, err := h.migrationResource(wire)
	if err != nil {
		return nil, err
	}
	identity, err := p.(providers.MigrationIdentityReader).MigrationIdentity(ctx, manifest)
	if err != nil {
		return nil, migrationIdentityError(err)
	}
	if identity == "" {
		return nil, status.Error(codes.FailedPrecondition, "provider returned an empty migration identity")
	}
	return &apiv1.ExportResourceResponse{Bundle: &apiv1.MigrationBundle{
		Version: migrationBundleVersion, Resource: wire, ControllerId: snapshot.ControllerID,
		ManifestDigest: snapshot.Digest, Identity: identity,
	}}, nil
}

func (h *resourceHandler) AdoptResource(ctx context.Context, req *apiv1.AdoptResourceRequest) (*apiv1.AdoptResourceResponse, error) {
	if err := authorize(ctx, auth.RoleAdmin); err != nil {
		return nil, err
	}
	h.managementMu.Lock()
	defer h.managementMu.Unlock()
	if err := h.migrationReady(); err != nil {
		return nil, err
	}
	if (req.GetResource() == nil) == (req.GetBundle() == nil) {
		return nil, status.Error(codes.InvalidArgument, "exactly one resource or bundle is required")
	}
	wire := req.GetResource()
	if req.GetBundle() != nil {
		if err := validateMigrationBundle(req.GetBundle()); err != nil {
			return nil, err
		}
		wire = req.GetBundle().GetResource()
	}
	// Clone the wire form before any destination override; source forget must
	// continue to use the original manifest and its digest.
	wire = proto.Clone(wire).(*apiv1.Resource)
	manifest, p, err := h.migrationResource(wire)
	if err != nil {
		return nil, err
	}
	if req.GetProviderContext() != "" {
		manifest.Spec["context"] = req.GetProviderContext()
		if err := schema.Validate(manifest); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid destination provider context")
		}
	}
	identity, err := p.(providers.MigrationIdentityReader).MigrationIdentity(ctx, manifest)
	if err != nil {
		return nil, migrationIdentityError(err)
	}
	if identity == "" || (req.GetBundle() != nil && identity != req.GetBundle().GetIdentity()) {
		return nil, status.Error(codes.FailedPrecondition, "existing VM identity does not match migration bundle")
	}
	// Identity and observation must both succeed before recording intent.
	// A read outage must not be replaced with fabricated observed state.
	observed, err := p.Get(ctx, manifest.Kind, manifest.Metadata.Name)
	if err != nil || observed == nil {
		return nil, status.Error(codes.FailedPrecondition, "cannot observe existing VM; retry adoption before forgetting the source")
	}
	out, err := resourceToProto(observed)
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot encode observed VM")
	}
	desiredSpec, observedSpec := providers.DriftSpecs(p, manifest.Kind, manifest.Spec, observed.Spec)
	out.Drift = computeDrift(desiredSpec, observedSpec)
	if err := h.manifests.Adopt(ctx, manifest); err != nil {
		return nil, migrationError(err)
	}
	if h.mirror != nil {
		if err := h.mirror.Materialize(ctx, manifest); err != nil {
			return nil, status.Error(codes.Internal, "VM adoption saved; disk projection failed, retry adoption to repair")
		}
	}
	return &apiv1.AdoptResourceResponse{Resource: out, Message: "existing VM adopted without provider changes"}, nil
}

func (h *resourceHandler) ForgetResource(ctx context.Context, req *apiv1.ForgetResourceRequest) (*apiv1.ForgetResourceResponse, error) {
	if err := authorize(ctx, auth.RoleAdmin); err != nil {
		return nil, err
	}
	h.managementMu.Lock()
	defer h.managementMu.Unlock()
	if err := h.migrationReady(); err != nil {
		return nil, err
	}
	bundle := req.GetBundle()
	if err := validateMigrationBundle(bundle); err != nil {
		return nil, err
	}
	manifest, p, err := h.migrationResource(bundle.GetResource())
	if err != nil {
		return nil, err
	}
	identity, err := p.(providers.MigrationIdentityReader).MigrationIdentity(ctx, manifest)
	if err != nil {
		return nil, migrationIdentityError(err)
	}
	if identity != bundle.GetIdentity() {
		return nil, status.Error(codes.FailedPrecondition, "existing VM identity does not match migration bundle")
	}
	if err := h.manifests.Forget(ctx, manifest, bundle.GetControllerId(), bundle.GetManifestDigest()); err != nil {
		return nil, migrationError(err)
	}
	if h.mirror != nil {
		if err := h.mirror.RemoveProjection(ctx, manifest.APIVersion, manifest.Kind, manifest.Metadata.Name); err != nil {
			return nil, status.Error(codes.Internal, "VM tracking released; disk projection failed, retry forget to repair")
		}
	}
	return &apiv1.ForgetResourceResponse{Message: "controller tracking released; provider VM unchanged"}, nil
}

func (h *resourceHandler) migrationReady() error {
	if h.gitopsEnabled {
		return status.Error(codes.FailedPrecondition, "disable manifest GitOps watch/pull automation and restart the controller before migration")
	}
	if h.manifests == nil {
		return status.Error(codes.FailedPrecondition, "migration requires a configured manifest store")
	}
	return nil
}

// migrationResource rejects relationship/state fields before proto conversion
// (which intentionally drops wire-only ownership and child fields).
func (h *resourceHandler) migrationResource(wire *apiv1.Resource) (*protocol.Resource, providers.Provider, error) {
	if wire == nil || wire.GetApiVersion() == "" || wire.GetKind() == "" || wire.GetMetadata().GetName() == "" || wire.GetSpec() == nil {
		return nil, nil, status.Error(codes.InvalidArgument, "complete desired resource is required")
	}
	if wire.GetStatus() != nil || len(wire.GetDrift()) != 0 {
		return nil, nil, status.Error(codes.InvalidArgument, "migration accepts desired intent without status or drift")
	}
	if len(wire.GetChildren()) != 0 || len(wire.GetMetadata().GetOwnerRefs()) != 0 {
		return nil, nil, status.Error(codes.FailedPrecondition, "migration does not support owned or composite resources")
	}
	manifest := protoToResource(wire)
	if manifest.APIVersion != "proxmox.openctl.io/v1" || manifest.Kind != "VirtualMachine" {
		return nil, nil, status.Error(codes.FailedPrecondition, "migration supports only standalone Proxmox VirtualMachine resources")
	}
	if err := schema.Validate(manifest); err != nil {
		return nil, nil, status.Error(codes.InvalidArgument, "migration manifest failed schema validation")
	}
	p, err := h.registry.For(manifest.APIVersion)
	if err != nil {
		return nil, nil, status.Error(codes.FailedPrecondition, "migration provider is not configured")
	}
	if _, ok := p.(providers.MigrationIdentityReader); !ok {
		return nil, nil, status.Error(codes.FailedPrecondition, "provider does not support migration identity verification")
	}
	if _, composite := p.(providers.Planner); composite {
		return nil, nil, status.Error(codes.FailedPrecondition, "migration does not support composite providers")
	}
	if _, _, owned := h.registry.OwnerOf(manifest.Kind, manifest.Metadata.Name); owned ||
		len(h.registry.ChildrenOf(manifest.Kind, manifest.Metadata.Name)) != 0 ||
		manifest.Metadata.Labels[providers.LabelOwnerKind] != "" || manifest.Metadata.Labels[providers.LabelOwnerName] != "" {
		return nil, nil, status.Error(codes.FailedPrecondition, "migration does not support owned or composite resources")
	}
	return manifest, p, nil
}

func validateMigrationBundle(bundle *apiv1.MigrationBundle) error {
	if bundle == nil || bundle.GetVersion() != migrationBundleVersion || bundle.GetResource() == nil ||
		bundle.GetControllerId() == "" || bundle.GetManifestDigest() == "" || bundle.GetIdentity() == "" {
		return status.Error(codes.InvalidArgument, "complete version 1 migration bundle is required")
	}
	digest, err := manifests.MigrationDigest(protoToResource(bundle.GetResource()))
	if err != nil || digest != bundle.GetManifestDigest() {
		return status.Error(codes.InvalidArgument, "migration bundle manifest digest does not match")
	}
	return nil
}

func migrationError(err error) error {
	var forgotten *manifests.ForgottenError
	if errors.As(err, &forgotten) {
		return status.Error(codes.FailedPrecondition, forgotten.Error())
	}
	switch {
	case errors.Is(err, manifests.ErrMigrationNotFound):
		return status.Error(codes.NotFound, "desired resource not found")
	case errors.Is(err, manifests.ErrMigrationBusy):
		return status.Error(codes.FailedPrecondition, "migration requires an idle controller; wait for all pending and running operations to finish")
	case errors.Is(err, manifests.ErrMigrationUnsupported):
		return status.Error(codes.FailedPrecondition, "migration requires a standalone VM without managed references, ownership, or opaque provider state")
	case errors.Is(err, manifests.ErrMigrationConflict):
		return status.Error(codes.FailedPrecondition, "migration conflicts with current desired intent or source controller; use an unchanged bundle from this source, or export current intent again")
	default:
		return status.Error(codes.Internal, "migration manifest store operation failed")
	}
}

func migrationIdentityError(err error) error {
	var notFound *providers.NotFoundError
	if errors.As(err, &notFound) {
		return status.Error(codes.NotFound, "existing migration VM not found")
	}
	return status.Error(codes.FailedPrecondition, "cannot verify existing migration VM identity; check provider credentials/context, desired node, and the VM's actual smbios1 UUID")
}

func (h *resourceHandler) checkNotForgotten(ctx context.Context, av, kind, name string) error {
	if h.manifests == nil {
		return nil
	}
	forgotten, err := h.manifests.IsForgotten(ctx, av, kind, name)
	if err != nil {
		return status.Error(codes.Internal, "cannot check released resource tracking")
	}
	if forgotten {
		return status.Error(codes.FailedPrecondition, fmt.Sprintf("%s %q was forgotten; explicitly adopt it before managing it", kind, name))
	}
	return nil
}
