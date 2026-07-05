package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/openctl/openctl/pkg/k3s/handler"
	"github.com/openctl/openctl/pkg/protocol"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--capabilities" {
		printCapabilities()
		return
	}

	if err := handleRequest(); err != nil {
		writeError(err)
		os.Exit(1)
	}
}

func printCapabilities() {
	caps := protocol.Capabilities{
		ProviderName:     "k3s",
		ProtocolVersion:  protocol.ProtocolVersion,
		SupportsDispatch: true,
		Resources: []protocol.ResourceDefinition{
			{
				Kind:    "Cluster",
				Plural:  "clusters",
				Actions: []string{"get", "list", "create", "delete"},
			},
		},
		Subcommands: []protocol.SubcommandDefinition{
			{
				Name:   "logs",
				Short:  "Fetch k3s service logs from a cluster node",
				Long:   "Fetch the k3s systemd journal from a node's agent over mTLS. If the cluster has one node it is chosen automatically; otherwise pass --node.",
				Action: "logs",
				PositionalArgs: []protocol.ArgSpec{
					{Name: "cluster", Required: true, Help: "cluster name"},
				},
				Flags: []protocol.FlagSpec{
					{Name: "node", Short: "n", Type: "string", Help: "node to read logs from (defaults to the only node)"},
					{Name: "lines", Short: "l", Type: "int", Help: "number of trailing log lines (0 = agent default)"},
				},
			},
			{
				Name:   "restart",
				Short:  "Restart the k3s service on a cluster node",
				Long:   "Restart the k3s systemd service on a specific node via its agent over mTLS.",
				Action: "restart",
				PositionalArgs: []protocol.ArgSpec{
					{Name: "cluster", Required: true, Help: "cluster name"},
				},
				Flags: []protocol.FlagSpec{
					{Name: "node", Short: "n", Type: "string", Required: true, Help: "node to restart k3s on"},
				},
			},
		},
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.Encode(caps)
}

func handleRequest() error {
	var req protocol.Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		return fmt.Errorf("failed to decode request: %w", err)
	}

	h := handler.New(&req.Config)
	resp, err := h.Handle(&req)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(resp)
}

func writeError(err error) {
	resp := protocol.Response{
		Status: protocol.StatusError,
		Error: &protocol.Error{
			Code:    protocol.ErrorCodeInternal,
			Message: err.Error(),
		},
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.Encode(resp)
}
