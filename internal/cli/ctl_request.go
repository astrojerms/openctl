package cli

import (
	"fmt"

	"github.com/google/uuid"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func prepareApplyRequest(req *apiv1.ApplyRequest) error {
	if req.GetRequestId() != "" {
		return validateOperationRequestID(req.GetRequestId())
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return fmt.Errorf("generate Apply request identity: %w", err)
	}
	req.RequestId = id.String()
	return nil
}

func prepareDeleteRequest(req *apiv1.DeleteRequest) error {
	if req.GetRequestId() != "" {
		return validateOperationRequestID(req.GetRequestId())
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return fmt.Errorf("generate Delete request identity: %w", err)
	}
	req.RequestId = id.String()
	return nil
}

func validateOperationRequestID(id string) error {
	if len(id) < 1 || len(id) > 128 {
		return fmt.Errorf("request ID must contain 1-128 printable ASCII characters")
	}
	for i := range len(id) {
		if id[i] < 0x20 || id[i] > 0x7e {
			return fmt.Errorf("request ID must contain 1-128 printable ASCII characters")
		}
	}
	return nil
}
