// Package server is the controller's gRPC server. It wires TLS, optional
// auth, the PingService, and reflection (for grpcurl) into a single
// grpc.Server.
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// ServerVersion is the controller binary version string echoed by Ping.
const ServerVersion = "0.1.0-controller"

// Options configures a Server. Token may be empty to disable auth (used for
// `--no-auth` localhost-only setups). Registry may be nil; the resource
// service still registers but every call will error with "no provider
// registered" until at least one provider is attached.
//
// Operations and Dispatcher together drive Phase 3's async lifecycle: the
// resource service inserts ops into Operations and Notifies the Dispatcher;
// the OperationService exposes the Operations store directly. If both are
// nil, Apply/Delete fall back to the Phase 2 synchronous behavior — useful
// for tests that don't want to spin up the full dispatcher loop.
type Options struct {
	Listen     string
	CertFile   string
	KeyFile    string
	Token      string
	Registry   *providers.Registry
	Operations *operations.Store
	Dispatcher *operations.Dispatcher
	// Manifests powers Phase 5 drift detection. Get/List compare observed
	// state against the stored desired manifest. May be nil — drift just
	// won't be populated.
	Manifests *manifests.Store
}

// Server is the controller's gRPC server.
type Server struct {
	opts Options
	grpc *grpc.Server
}

// New constructs a Server. The TLS keypair is loaded eagerly so config
// errors surface here, not on first request.
func New(opts Options) (*Server, error) {
	cert, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server keypair: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	srvOpts := []grpc.ServerOption{
		grpc.Creds(credentials.NewTLS(tlsCfg)),
	}
	if opts.Token != "" {
		v := auth.NewValidator(opts.Token)
		srvOpts = append(srvOpts, grpc.UnaryInterceptor(v.UnaryInterceptor()))
	}

	g := grpc.NewServer(srvOpts...)
	apiv1.RegisterPingServiceServer(g, &pingHandler{})

	registry := opts.Registry
	if registry == nil {
		registry = providers.NewRegistry()
	}
	apiv1.RegisterResourceServiceServer(g, newResourceHandler(registry, opts.Operations, opts.Dispatcher, opts.Manifests))
	if opts.Operations != nil {
		apiv1.RegisterOperationServiceServer(g, newOperationHandler(opts.Operations))
	}
	apiv1.RegisterSchemaServiceServer(g, newSchemaHandler())

	reflection.Register(g)

	return &Server{opts: opts, grpc: g}, nil
}

// Serve listens on the configured address and serves until Stop is called.
func (s *Server) Serve() error {
	ln, err := net.Listen("tcp", s.opts.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.opts.Listen, err)
	}
	return s.grpc.Serve(ln)
}

// ServeListener serves on an existing listener. Used by tests that pick an
// ephemeral port and want to know it before the server starts.
func (s *Server) ServeListener(ln net.Listener) error {
	return s.grpc.Serve(ln)
}

// Stop gracefully stops the server, draining in-flight RPCs.
func (s *Server) Stop() {
	s.grpc.GracefulStop()
}

// pingHandler is the trivial implementation of PingService used to verify
// TLS, auth, and the gRPC pipeline end-to-end.
type pingHandler struct {
	apiv1.UnimplementedPingServiceServer
}

func (h *pingHandler) Ping(_ context.Context, req *apiv1.PingRequest) (*apiv1.PingResponse, error) {
	return &apiv1.PingResponse{
		Echo:          req.GetMessage(),
		ServerVersion: ServerVersion,
	}, nil
}
