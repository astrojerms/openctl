package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/openctl/openctl-k3s/internal/handler"
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
