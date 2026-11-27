package server

import (
	"errors"
	"fmt"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/refs"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// referenceFieldError fails closed even if a future resolver returns an untyped
// error. Only declared reference identity and static guidance may reach viewers.
func referenceFieldError(err error) *apiv1.FieldError {
	out := &apiv1.FieldError{Path: "spec", Code: "reference_read_failed", Message: "Reference resolution failed. Check the declared references and controller-side provider configuration."}
	var resolution *refs.ResolutionError
	if !errors.As(err, &resolution) {
		return out
	}
	out.Path = resolution.SpecPath()
	out.Message = resolution.Error()
	switch resolution.Kind {
	case refs.InvalidReference:
		out.Code = "reference_invalid"
		out.Message += " Correct the reference apiVersion, kind, name and optional field."
	case refs.UnavailableResource:
		out.Code = "reference_not_found"
		out.Message += " Check that the declared resource exists in the configured provider context."
	case refs.UnavailableField:
		out.Code = "reference_field_unavailable"
		out.Message += " Check the declared field and wait for the referenced resource to publish its output."
	case refs.ProviderReadFailed:
		var missing *providers.NotFoundError
		if errors.As(resolution.Cause, &missing) {
			out.Code = "reference_not_found"
			out.Message += " Check that the declared resource exists in the configured provider context."
			break
		}
		code, hint := providerReadFailure(resolution.Cause)
		if code != "connection_failed" {
			out.Code = code
		} else {
			hint = "Check the controller-side provider context, read permissions and provider service health; restart after changing saved configuration."
		}
		out.Message += " " + hint
	default:
		// Do not trust formatting of a future, unsupported failure kind.
		out.Message = fmt.Sprintf("Reference resolution failed at %s. Check the declared reference and controller-side provider configuration.", out.Path)
	}
	return out
}
