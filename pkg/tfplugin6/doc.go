// Package tfplugin6 holds the Terraform plugin protocol v6 gRPC stubs,
// vendored verbatim from
// github.com/hashicorp/terraform-plugin-go/tfprotov6/internal/tfplugin6
// (v0.31.0). Terraform keeps those stubs under internal/, so a client of the
// protocol cannot import them — openctl needs them to act as a CLIENT that
// launches and drives terraform-provider-* binaries (the Terraform/OpenTofu
// provider host; see docs/plugin-architecture.md).
//
// tfplugin6.pb.go and tfplugin6_grpc.pb.go are generated code — DO NOT EDIT.
// tfplugin6.proto is kept alongside for provenance/regeneration. To bump the
// protocol, re-copy the three files from the module cache (the wire format is
// stable within the protocol major version).
package tfplugin6
