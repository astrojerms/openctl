package main

import "testing"

func TestUnauthenticatedControllerRejectsPublicGatewayOrGRPC(t *testing.T) {
	for _, tc := range []struct {
		name, grpc, http string
		allowed          bool
	}{
		{"IPv4 loopback", "127.0.0.1:9444", "127.0.0.1:9445", true},
		{"IPv6 loopback", "[::1]:9444", "[::1]:9445", true},
		{"localhost", "localhost:9444", "localhost:9445", true},
		{"gateway disabled", "127.0.0.1:9444", "", true},
		{"wildcard grpc", "0.0.0.0:9444", "127.0.0.1:9445", false},
		{"wildcard gateway", "127.0.0.1:9444", ":9445", false},
		{"public gateway", "127.0.0.1:9444", "192.0.2.10:9445", false},
		{"IPv6 wildcard", "127.0.0.1:9444", "[::]:9445", false},
		{"unresolved hostname", "controller.example:9444", "", false},
		{"invalid address", "127.0.0.1", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUnauthenticatedListeners(tc.grpc, tc.http)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
		})
	}
}
