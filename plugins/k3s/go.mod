module github.com/openctl/openctl-k3s

go 1.23.0

require (
	github.com/openctl/openctl v0.0.0
	golang.org/x/crypto v0.31.0
	gopkg.in/yaml.v3 v3.0.1
)

require golang.org/x/sys v0.28.0 // indirect

replace github.com/openctl/openctl => ../..
