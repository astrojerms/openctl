package main

import (
	"fmt"
	"net"
)

// The HTTP gateway forwards requests to gRPC with controller authority. Both
// listeners must stay local when authentication is disabled.
func validateUnauthenticatedListeners(grpcAddress, httpAddress string) error {
	for _, listener := range []struct{ name, address string }{{"gRPC", grpcAddress}, {"HTTP", httpAddress}} {
		if listener.name == "HTTP" && listener.address == "" {
			continue
		}
		host, _, err := net.SplitHostPort(listener.address)
		if err != nil {
			return fmt.Errorf("--no-auth requires a valid loopback %s listen address", listener.name)
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("--no-auth requires loopback-only %s binding; configure authentication before exposing the controller", listener.name)
		}
	}
	return nil
}
