package providers

import "fmt"

// NotFoundError is returned by Provider implementations when a resource lookup
// finds nothing. The server layer maps this to gRPC codes.NotFound.
type NotFoundError struct {
	Kind string
	Name string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s %q not found", e.Kind, e.Name)
}

// NotFound returns a sentinel "not found" error tagged with kind + name so
// callers can errors.As it.
func NotFound(kind, name string) error {
	return &NotFoundError{Kind: kind, Name: name}
}
