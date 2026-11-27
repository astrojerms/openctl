package operations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openctl/openctl/internal/controller/refs"
	"github.com/openctl/openctl/pkg/protocol"
)

func dependentApply(t *testing.T, target refs.Ref) *Operation {
	t.Helper()
	raw := &protocol.Resource{
		APIVersion: "consumer.openctl.io/v1", Kind: "Application",
		Metadata: protocol.ResourceMetadata{Name: "consumer"},
		Spec: map[string]any{"nested": []any{map[string]any{
			"target": map[string]any{"$ref": map[string]any{
				"apiVersion": target.APIVersion, "kind": target.Kind, "name": target.Name, "field": "status.ip",
			}},
		}}},
	}
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &Operation{Type: TypeApply, APIVersion: raw.APIVersion, Kind: raw.Kind, ResourceName: raw.Metadata.Name, ManifestJSON: string(body)}
}

func targetDelete(target refs.Ref) *Operation {
	return &Operation{Type: TypeDelete, APIVersion: target.APIVersion, Kind: target.Kind, ResourceName: target.Name}
}

func TestSubmitFencesReferenceDeletionInBothAdmissionOrders(t *testing.T) {
	for _, firstType := range []string{TypeApply, TypeDelete} {
		for _, firstStatus := range []string{StatusPending, StatusRunning} {
			t.Run(firstType+"/"+firstStatus, func(t *testing.T) {
				ctx := context.Background()
				store := openStore(t, 0)
				target := refs.Ref{APIVersion: "target.openctl.io/v1", Kind: "VirtualMachine", Name: "target"}
				first, second := dependentApply(t, target), targetDelete(target)
				if firstType == TypeDelete {
					first, second = second, first
				}
				first, err := store.Submit(ctx, first)
				if err != nil {
					t.Fatal(err)
				}
				if firstStatus == StatusRunning {
					claimed, err := store.ClaimNextPending(ctx)
					if err != nil || claimed.ID != first.ID {
						t.Fatalf("claim first operation: %+v, %v", claimed, err)
					}
				}
				_, err = store.Submit(ctx, second)
				var conflict *ConflictError
				if !errors.As(err, &conflict) || conflict.InflightID != first.ID || conflict.Reason == "" {
					t.Fatalf("dependent/delete admission was not fenced by first operation: %v", err)
				}
				ops, err := store.List(ctx, ListFilter{})
				if err != nil || len(ops) != 1 || ops[0].Status != firstStatus {
					t.Fatalf("rejected operation changed persisted admission state: %+v, %v", ops, err)
				}
				if err := store.Complete(ctx, first.ID, StatusCancelled, "canceled", ""); err != nil {
					t.Fatal(err)
				}
				if _, err := store.Submit(ctx, second); err != nil {
					t.Fatalf("terminal operation retained active reference fence: %v", err)
				}
			})
		}
	}
}

func TestReferenceDeletionFenceUsesFullResourceIdentity(t *testing.T) {
	for _, firstType := range []string{TypeApply, TypeDelete} {
		for _, differing := range []string{"apiVersion", "kind", "name"} {
			t.Run(firstType+"/"+differing, func(t *testing.T) {
				store := openStore(t, 0)
				target := refs.Ref{APIVersion: "target.openctl.io/v1", Kind: "VirtualMachine", Name: "target"}
				other := target
				switch differing {
				case "apiVersion":
					other.APIVersion = "target.openctl.io/v2"
				case "kind":
					other.Kind = "Container"
				case "name":
					other.Name = "another-target"
				}
				first, second := dependentApply(t, other), targetDelete(target)
				if firstType == TypeDelete {
					first, second = second, first
				}
				if _, err := store.Submit(context.Background(), first); err != nil {
					t.Fatal(err)
				}
				if _, err := store.Submit(context.Background(), second); err != nil {
					t.Fatalf("unrelated resource identity incorrectly fenced: %v", err)
				}
			})
		}
	}
}

func TestMalformedReferencesCannotBypassAdmissionFence(t *testing.T) {
	for _, marker := range []any{
		"not an object",
		map[string]any{"kind": "VirtualMachine", "name": "target"},
		map[string]any{"apiVersion": "target.openctl.io/v1", "kind": "VirtualMachine", "name": 42},
		map[string]any{"apiVersion": "target.openctl.io/v1", "kind": "VirtualMachine", "name": "target", "field": 42},
	} {
		store := openStore(t, 0)
		body, err := json.Marshal(map[string]any{"spec": map[string]any{"nested": []any{map[string]any{"$ref": marker}}}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.Submit(context.Background(), &Operation{
			Type: TypeApply, APIVersion: "consumer.openctl.io/v1", Kind: "Application", ResourceName: "consumer", ManifestJSON: string(body),
		})
		if err == nil {
			t.Fatalf("malformed reference was admitted: %s", body)
		}
		ops, err := store.List(context.Background(), ListFilter{})
		if err != nil || len(ops) != 0 {
			t.Fatalf("malformed reference left an active operation: %+v, %v", ops, err)
		}
	}
}

func TestDeleteFailsClosedForMalformedLegacyActiveReferences(t *testing.T) {
	for _, body := range []string{
		`{"spec":{"target":{"$ref":{"kind":"VirtualMachine","name":"target"}}}}`,
		`{"spec":`,
	} {
		store := openStore(t, 0)
		ctx := context.Background()
		op, err := store.Submit(ctx, &Operation{Type: TypeApply, APIVersion: "consumer.openctl.io/v1", Kind: "Application", ResourceName: "consumer"})
		if err != nil {
			t.Fatal(err)
		}
		// Previous controllers admitted malformed manifests; reproduce their
		// durable pending rows without bypassing current Submit validation.
		if _, err := store.db.ExecContext(ctx, `UPDATE operations SET manifest_json = ? WHERE id = ?`, body, op.ID); err != nil {
			t.Fatal(err)
		}
		_, err = store.Submit(ctx, targetDelete(refs.Ref{APIVersion: "target.openctl.io/v1", Kind: "VirtualMachine", Name: "target"}))
		var conflict *ConflictError
		if !errors.As(err, &conflict) || conflict.InflightID != op.ID || conflict.Reason == "" {
			t.Fatalf("delete ignored unknown legacy apply dependencies: %v", err)
		}
	}
}
