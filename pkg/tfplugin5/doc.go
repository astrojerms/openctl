// Package tfplugin5 holds the Terraform plugin protocol v5 gRPC stubs,
// vendored from
// github.com/hashicorp/terraform-plugin-go/tfprotov5/internal/tfplugin5
// (v0.31.0). Terraform keeps those stubs under internal/, so a client of the
// protocol cannot import them — openctl needs them to act as a CLIENT that
// launches and drives SDKv2 terraform-provider-* binaries, which serve
// protocol 5. Framework providers serve protocol 6 (see pkg/tfplugin6); the
// tfhost client negotiates whichever the launched provider offers.
//
// tfplugin5.pb.go and tfplugin5_grpc.pb.go are generated code — DO NOT EDIT.
// tfplugin5.proto is kept alongside for provenance/regeneration.
package tfplugin5
