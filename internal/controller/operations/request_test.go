package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/secrets"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

func requestResource() *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		Metadata: protocol.ResourceMetadata{Name: "requested", Labels: map[string]string{"team": "home"}},
		Spec: map[string]any{
			"password": map[string]any{"$secret": map[string]any{"file": "request-password"}},
		},
	}
}

func requestOperation(t *testing.T, desired *protocol.Resource) *Operation {
	t.Helper()
	body, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	return &Operation{
		Type: TypeApply, APIVersion: desired.APIVersion, Kind: desired.Kind,
		ResourceName: desired.Metadata.Name, ManifestJSON: string(body), Source: manifests.SourceCLI,
	}
}

func requestBinding(t *testing.T, id, actor string, desired *protocol.Resource, flags ...bool) RequestBinding {
	t.Helper()
	var values [3]bool
	copy(values[:], flags)
	binding, err := NewApplyRequest(id, actor, desired, values[0], values[1], values[2])
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestRequestIDValidation(t *testing.T) {
	for _, id := range []string{"", "uuid-like-key", " ", "key with spaces", strings.Repeat("~", 128)} {
		if err := ValidateRequestID(id); err != nil {
			t.Errorf("valid key rejected: %q: %v", id, err)
		}
	}
	for _, id := range []string{strings.Repeat("a", 129), "line\nbreak", "\t", "nul\x00", "\x7f", "é"} {
		if err := ValidateRequestID(id); err == nil {
			t.Errorf("invalid key accepted: %q", id)
		}
	}
}

func TestApplyRequestFingerprintUsesUnresolvedDesiredIntent(t *testing.T) {
	desired := requestResource()
	desired.Spec["target"] = map[string]any{"$ref": map[string]any{"apiVersion": "fake.openctl.io/v1", "kind": "FakeKind", "name": "upstream", "field": "status.ip"}}
	binding := requestBinding(t, "key", "alice", desired)
	if len(binding.IntentHash) != 64 {
		t.Fatalf("not a SHA-256 digest: %q", binding.IntentHash)
	}
	// Observed state and JSON map insertion order must not change desired intent.
	desired.Status = map[string]any{"password": "not-desired-state"}
	desired.Metadata.Labels = map[string]string{"team": "home"}
	if got := requestBinding(t, "another-key", "bob", desired); got.IntentHash != binding.IntentHash {
		t.Fatal("key, actor, or observed status contaminated the intent digest")
	}
	desired.Spec["password"] = map[string]any{"$secret": map[string]any{"file": "rotated-reference"}}
	if got := requestBinding(t, "key", "alice", desired); got.IntentHash == binding.IntentHash {
		t.Fatal("a different unresolved secret reference reused the fingerprint")
	}
	if strings.Contains(binding.IntentHash, "request-password") {
		t.Fatal("request fingerprint retained the submitted payload")
	}
}

func TestSubmitRequestConcurrentSameBodyAcrossStores(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, 0)
	other := New(store.db, 0)
	desired := requestResource()
	binding := requestBinding(t, "concurrent", "alice", desired)
	const count = 24
	type result struct {
		op       *Operation
		admitted bool
		err      error
	}
	results := make(chan result, count)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range count {
		candidate := requestOperation(t, desired)
		selected := store
		if i%2 != 0 {
			selected = other
			candidate.Source = manifests.SourceUI
		}
		workers.Go(func() {
			<-start
			op, admitted, err := selected.SubmitRequest(ctx, candidate, binding)
			results <- result{op, admitted, err}
		})
	}
	close(start)
	workers.Wait()
	close(results)
	var original string
	admissions := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent submission: %v", result.err)
		}
		if original == "" {
			original = result.op.ID
		}
		if result.op.ID != original || result.op.RequestID != binding.ID {
			t.Fatalf("request returned another identity: %+v", result.op)
		}
		if result.admitted {
			admissions++
		}
	}
	all, err := store.List(ctx, ListFilter{})
	if err != nil || len(all) != 1 || admissions != 1 {
		t.Fatalf("duplicate admission: rows=%d admissions=%d err=%v", len(all), admissions, err)
	}
}

func TestSubmitRequestConcurrentDifferentIntentNeverSharesAcknowledgement(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, 0)
	const count = 16
	type result struct {
		binding  RequestBinding
		op       *Operation
		admitted bool
		err      error
	}
	results := make(chan result, count)
	start := make(chan struct{})
	for i := range count {
		desired := requestResource()
		desired.Spec["choice"] = i % 2
		binding := requestBinding(t, "different-intent", "alice", desired)
		candidate := requestOperation(t, desired)
		go func() {
			<-start
			op, admitted, err := store.SubmitRequest(ctx, candidate, binding)
			results <- result{binding, op, admitted, err}
		}()
	}
	close(start)
	var acceptedHash, acceptedID string
	var successes, conflicts, admissions int
	for range count {
		result := <-results
		if result.err != nil {
			var conflict *RequestConflictError
			if !errors.As(result.err, &conflict) {
				t.Fatalf("unexpected concurrent conflict: %v", result.err)
			}
			conflicts++
			continue
		}
		if acceptedHash == "" {
			acceptedHash, acceptedID = result.binding.IntentHash, result.op.ID
		}
		if result.binding.IntentHash != acceptedHash || result.op.ID != acceptedID {
			t.Fatal("different submitted intents shared an accepted request")
		}
		successes++
		if result.admitted {
			admissions++
		}
	}
	if successes != count/2 || conflicts != count/2 || admissions != 1 {
		t.Fatalf("successes=%d conflicts=%d admissions=%d", successes, conflicts, admissions)
	}
}

func TestSubmitRequestRejectsDifferentActorIntentAndFlags(t *testing.T) {
	store := openStore(t, 0)
	ctx := context.Background()
	desired := requestResource()
	binding := requestBinding(t, "bound", "alice", desired)
	original, _, err := store.SubmitRequest(ctx, requestOperation(t, desired), binding)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]RequestBinding{
		"actor":             requestBinding(t, "bound", "bob", desired),
		"create-only":       requestBinding(t, "bound", "alice", desired, true),
		"allow-destructive": requestBinding(t, "bound", "alice", desired, false, true),
		"break-cluster":     requestBinding(t, "bound", "alice", desired, false, false, true),
	}
	different := requestResource()
	different.Spec["value"] = "changed"
	cases["spec"] = requestBinding(t, "bound", "alice", different)
	different.Metadata.Annotations = map[string]string{"note": "changed"}
	cases["metadata"] = requestBinding(t, "bound", "alice", different)
	different.Metadata.Name = "different-resource"
	cases["resource"] = requestBinding(t, "bound", "alice", different)
	for name, changed := range cases {
		t.Run(name, func(t *testing.T) {
			op, admitted, err := store.SubmitRequest(ctx, requestOperation(t, different), changed)
			var conflict *RequestConflictError
			if !errors.As(err, &conflict) || conflict.OperationID != original.ID || op != nil || admitted {
				t.Fatalf("bound key was reusable: operation=%+v admitted=%v err=%v", op, admitted, err)
			}
		})
	}
}

func TestRequestBindingSurvivesTerminalStatesAndReopen(t *testing.T) {
	for _, terminal := range []string{StatusSucceeded, StatusFailed, StatusCancelled, StatusInterrupted} {
		t.Run(terminal, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")
			db, err := storage.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			store := New(db, 0)
			desired := requestResource()
			binding := requestBinding(t, "persisted", "alice", desired, true)
			candidate := requestOperation(t, desired)
			candidate.CreateOnly = true
			original, _, err := store.SubmitRequest(ctx, candidate, binding)
			if err != nil {
				t.Fatal(err)
			}
			if terminal == StatusInterrupted {
				if _, err := store.ClaimNextPending(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := store.MarkRunningInterrupted(ctx); err != nil {
					t.Fatal(err)
				}
			} else if err := store.Complete(ctx, original.ID, terminal, "", ""); err != nil {
				t.Fatal(err)
			}
			// Initial creation has now established desired state. Replays must
			// resolve before create-only admission rejects this existing resource.
			if err := manifests.New(db).Save(ctx, desired); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := storage.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			store = New(reopened, 0)
			candidate = requestOperation(t, desired)
			candidate.CreateOnly = true
			replayed, admitted, err := store.SubmitRequest(ctx, candidate, binding)
			if err != nil || admitted || replayed.ID != original.ID || replayed.Status != terminal {
				t.Fatalf("reopened request was not replayed: %+v admitted=%v err=%v", replayed, admitted, err)
			}
			found, err := store.GetByRequest(ctx, binding.ID)
			if err != nil || found.ID != original.ID || found.ManifestJSON != original.ManifestJSON {
				t.Fatalf("lost-ack lookup: %+v err=%v", found, err)
			}
		})
	}
}

func TestRequestLedgerOutlivesOperationGC(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, 1)
	desired := requestResource()
	binding := requestBinding(t, "permanent-key", "alice", desired)
	first, _, err := store.SubmitRequest(ctx, requestOperation(t, desired), binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, first.ID, StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	second, err := store.Submit(ctx, requestOperation(t, desired))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, second.ID, StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, first.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old operation was not pruned: %v", err)
	}
	for name, lookup := range map[string]func() error{
		"read-only lookup": func() error { _, err := store.GetByRequest(ctx, binding.ID); return err },
		"early replay":     func() error { _, err := store.ReplayRequest(ctx, binding); return err },
		"admission replay": func() error { _, _, err := store.SubmitRequest(ctx, requestOperation(t, desired), binding); return err },
	} {
		t.Run(name, func(t *testing.T) {
			var pruned *RequestPrunedError
			if err := lookup(); !errors.As(err, &pruned) || pruned.OperationID != first.ID || !strings.Contains(err.Error(), first.ID) {
				t.Fatalf("key became reusable after GC: %v", err)
			}
		})
	}
	changed := binding
	changed.Actor = "bob"
	_, _, err = store.SubmitRequest(ctx, requestOperation(t, desired), changed)
	var conflict *RequestConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("GC tombstone did not retain actor/intent fence: %v", err)
	}
	all, err := store.List(ctx, ListFilter{})
	if err != nil || len(all) != 1 || all[0].ID != second.ID {
		t.Fatalf("tombstone replay mutated operation history: %+v err=%v", all, err)
	}
}

func TestRejectedAdmissionDoesNotReserveRequestKey(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, 0)
	desired := requestResource()
	if _, err := store.Submit(ctx, requestOperation(t, desired)); err != nil {
		t.Fatal(err)
	}
	binding := requestBinding(t, "not-accepted", "alice", desired)
	if _, _, err := store.SubmitRequest(ctx, requestOperation(t, desired), binding); err == nil {
		t.Fatal("per-resource admission fence was bypassed")
	}
	if _, err := store.GetByRequest(ctx, binding.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected admission reserved a request ID: %v", err)
	}
}

func TestKeyedOperationDoesNotPersistExpandedSecret(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, 0)
	secretDir := t.TempDir()
	const plaintext = "request-ledger-secret-fixture"
	if err := os.WriteFile(filepath.Join(secretDir, "request-password"), []byte(plaintext), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	registry := providers.NewRegistry()
	registry.Register(provider)
	dispatcher := NewDispatcher(store, registry, manifests.New(store.db), 0)
	secretRegistry := secrets.NewRegistry()
	secrets.RegisterBuiltins(secretRegistry, secretDir)
	dispatcher.SetSecrets(secretRegistry)
	desired := requestResource()
	binding := requestBinding(t, "secret-request", "alice", desired)
	original, _, err := store.SubmitRequest(ctx, requestOperation(t, desired), binding)
	if err != nil {
		t.Fatal(err)
	}
	runPendingOp(t, dispatcher, store, original.ID, StatusSucceeded)
	if provider.applies.Load() != 1 {
		t.Fatal("keyed operation did not reach the provider")
	}
	found, err := store.GetByRequest(ctx, binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(found.ManifestJSON, "$secret") || strings.Contains(found.ManifestJSON+found.ResultJSON, plaintext) {
		t.Fatal("operation recovery lost the marker or persisted expanded credentials")
	}
	var actor, hash, operationID string
	if err := store.db.QueryRowContext(ctx, `SELECT actor, intent_hash, operation_id FROM operation_requests WHERE request_id = ?`, binding.ID).Scan(&actor, &hash, &operationID); err != nil {
		t.Fatal(err)
	}
	if actor != "alice" || hash != binding.IntentHash || operationID != original.ID || strings.Contains(fmt.Sprint(actor, hash, operationID), plaintext) {
		t.Fatal("request ledger did not retain only the intended binding")
	}
	if err := os.WriteFile(filepath.Join(secretDir, "request-password"), []byte("rotated-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	replayed, admitted, err := store.SubmitRequest(ctx, requestOperation(t, desired), binding)
	if err != nil || admitted || replayed.ID != original.ID || provider.applies.Load() != 1 {
		t.Fatalf("secret rotation changed an accepted request: %+v admitted=%v err=%v", replayed, admitted, err)
	}
}
