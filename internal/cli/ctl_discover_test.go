package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"
)

type discoveryConnectionRPC struct {
	apiv1.UnimplementedResourceServiceServer
	connected bool
}

func (s *discoveryConnectionRPC) TestDiscoveryConnection(context.Context, *apiv1.TestDiscoveryConnectionRequest) (*apiv1.TestDiscoveryConnectionResponse, error) {
	response := &apiv1.TestDiscoveryConnectionResponse{
		FailureCode: "tls_untrusted",
		Message:     "Server certificate is not trusted; configure controller-side CA trust.",
	}
	if s.connected {
		response.Connected = true
		response.FailureCode = ""
		response.Message = "authenticated read-only inventory succeeded"
	}
	return response, nil
}

func TestDiscoveryConnectionExitStatusPreservesOutput(t *testing.T) {
	for _, connected := range []bool{false, true} {
		for _, format := range []string{"table", "json", "yaml"} {
			name := format + "/disconnected"
			if connected {
				name = format + "/connected"
			}
			t.Run(name, func(t *testing.T) {
				configureControllerRPC(t, func(server *grpc.Server) {
					apiv1.RegisterResourceServiceServer(server, &discoveryConnectionRPC{connected: connected})
				})
				previous := outputFormat
				outputFormat = format
				t.Cleanup(func() { outputFormat = previous })
				var stdout bytes.Buffer
				cmd := newCtlDiscoverCommand()
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetOut(&stdout)
				cmd.SetErr(&bytes.Buffer{})
				cmd.SetArgs([]string{"--test", "--provider-context", "lab"})
				err := cmd.Execute()
				if (err == nil) != connected {
					t.Fatalf("connected=%t, exit error=%v, output=%s", connected, err, stdout.String())
				}
				if !connected && (!strings.Contains(err.Error(), "tls_untrusted") || !strings.Contains(stdout.String(), "tls_untrusted")) {
					t.Fatalf("failure category missing from output or exit diagnostic: %s (%v)", stdout.String(), err)
				}
				if format == "table" {
					value := "false"
					if connected {
						value = "true"
					}
					if !strings.Contains(stdout.String(), value) || !strings.Contains(stdout.String(), "CONNECTED") {
						t.Fatalf("connection outcome missing from table: %s", stdout.String())
					}
					return
				}
				var response struct {
					Connected   bool   `json:"connected" yaml:"connected"`
					Message     string `json:"message" yaml:"message"`
					FailureCode string `json:"failureCode" yaml:"failureCode"`
				}
				if format == "json" {
					err = json.Unmarshal(stdout.Bytes(), &response)
				} else {
					err = yaml.Unmarshal(stdout.Bytes(), &response)
				}
				if err != nil || response.Connected != connected || response.Message == "" {
					t.Fatalf("connection outcome lost from %s: %s (%v)", format, stdout.String(), err)
				}
				if connected && response.FailureCode != "" || !connected && response.FailureCode != "tls_untrusted" {
					t.Fatalf("incorrect structured failure category: %+v", response)
				}
			})
		}
	}
}
