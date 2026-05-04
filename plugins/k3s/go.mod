module github.com/openctl/openctl-k3s

go 1.25.0

require github.com/openctl/openctl v0.0.0

require (
	golang.org/x/crypto v0.31.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/openctl/openctl => ../..
