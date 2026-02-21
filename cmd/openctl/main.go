package main

import (
	"os"

	"github.com/openctl/openctl/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
