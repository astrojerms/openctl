package refs_test

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/refs"
	"github.com/openctl/openctl/pkg/protocol"
)

type getterFunc func(context.Context, string, string, string) (*protocol.Resource, error)

func (f getterFunc) Get(ctx context.Context, apiVersion, kind, name string) (*protocol.Resource, error) {
	return f(ctx, apiVersion, kind, name)
}

func reference(field string) map[string]any {
	return map[string]any{refs.RefMarker: map[string]any{
		"apiVersion": "example.openctl.io/v1",
		"kind":       "Machine",
		"name":       "upstream",
		"field":      field,
	}}
}

func TestResolutionErrorKeepsProviderCausesPrivate(t *testing.T) {
	sentinel := errors.New("upstream private-body")
	certificateError := x509.HostnameError{
		Certificate: &x509.Certificate{DNSNames: []string{"certificate-secret.internal"}},
		Host:        "certificate-request-secret.internal",
	}
	providerError := &url.Error{
		Op:  "Get",
		URL: "https://provider-secret.internal/api?token=credential-token",
		Err: fmt.Errorf("response private-body: %w", errors.Join(sentinel, certificateError)),
	}
	resolver := refs.New(getterFunc(func(context.Context, string, string, string) (*protocol.Resource, error) {
		return nil, providerError
	}))
	out, err := resolver.Resolve(context.Background(), map[string]any{"target": reference("status.host")})
	if out != nil {
		t.Fatal("failed resolution returned partial output")
	}
	var failure *refs.ResolutionError
	if !errors.As(err, &failure) || failure.Kind != refs.ProviderReadFailed {
		t.Fatalf("expected typed provider read failure, got %T", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatal("provider sentinel was lost")
	}
	var gotURL *url.Error
	var gotCertificate x509.HostnameError
	if !errors.As(err, &gotURL) || gotURL != providerError || !errors.As(err, &gotCertificate) {
		t.Fatal("typed URL/certificate causes were lost")
	}
	if failure.Reference != (refs.Ref{APIVersion: "example.openctl.io/v1", Kind: "Machine", Name: "upstream", Field: "status.host"}) {
		t.Fatalf("declared reference was not retained: %+v", failure.Reference)
	}
	message := fmt.Errorf("resolve refs: %w", err).Error()
	for _, secret := range []string{"private-body", "credential-token", "provider-secret.internal", "certificate-secret.internal", "certificate-request-secret.internal"} {
		if strings.Contains(message, secret) {
			t.Fatalf("public error exposed provider data %q", secret)
		}
	}
	for _, declared := range []string{"example.openctl.io/v1", "Machine", "upstream", "status.host", "spec.target"} {
		if !strings.Contains(message, declared) {
			t.Errorf("public error omitted declared context %q", declared)
		}
	}
}

func TestResolutionErrorAttributesDestinationPaths(t *testing.T) {
	providerError := errors.New("private provider failure")
	resolver := refs.New(getterFunc(func(context.Context, string, string, string) (*protocol.Resource, error) {
		return nil, providerError
	}))
	tests := []struct {
		name string
		spec map[string]any
		path string
		full string
	}{
		{"root", reference("status.host"), "", "spec"},
		{"nested", map[string]any{"target": map[string]any{"host": reference("status.host")}}, "target.host", "spec.target.host"},
		{"array", map[string]any{"items": []any{map[string]any{"host": reference("status.host")}}}, "items[0].host", "spec.items[0].host"},
		{"dotted key", map[string]any{"a.b": map[string]any{"host": reference("status.host")}}, `["a.b"].host`, `spec["a.b"].host`},
		{"escaped key", map[string]any{"outer": map[string]any{"a\"b\\c\n": []any{map[string]any{"": reference("status.host")}}}}, `outer["a\"b\\c\n"][0][""]`, `spec.outer["a\"b\\c\n"][0][""]`},
		{"numeric key", map[string]any{"0": reference("status.host")}, `["0"]`, `spec["0"]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := resolver.Resolve(context.Background(), test.spec)
			var failure *refs.ResolutionError
			if out != nil || !errors.As(err, &failure) {
				t.Fatalf("expected typed failure without partial output, got %T", err)
			}
			if failure.Path != test.path || failure.SpecPath() != test.full {
				t.Fatalf("destination = %q / %q, want %q / %q", failure.Path, failure.SpecPath(), test.path, test.full)
			}
		})
	}
}

func TestResolutionErrorsForUnavailableTargetsAndFields(t *testing.T) {
	tests := []struct {
		name   string
		getter refs.Getter
		field  string
		kind   refs.FailureKind
		root   bool
	}{
		{name: "no getter", kind: refs.ProviderReadFailed},
		{name: "nil whole resource", getter: &nilGetter{}, kind: refs.UnavailableResource},
		{name: "nil field resource", getter: &nilGetter{}, field: "status.host", kind: refs.UnavailableResource},
		{name: "missing field", getter: resourceGetter(map[string]any{"other": "private-status-value"}), field: "status.host", kind: refs.UnavailableField},
		{name: "inaccessible field", getter: resourceGetter(map[string]any{"host": "private-status-value"}), field: "status.host.address", kind: refs.UnavailableField},
		{name: "missing status", getter: resourceGetter(nil), field: "status.host", kind: refs.UnavailableField},
		{name: "unsupported field root", getter: resourceGetter(nil), field: "metadata.name", kind: refs.UnavailableField},
		{name: "scalar spec root", getter: resourceGetter(map[string]any{"host": "private-status-value"}), field: "status.host", kind: refs.InvalidReference, root: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := map[string]any{"target": reference(test.field)}
			wantPath := "target"
			if test.root {
				spec = reference(test.field)
				wantPath = ""
			}
			out, err := refs.New(test.getter).Resolve(context.Background(), spec)
			var failure *refs.ResolutionError
			if out != nil || !errors.As(err, &failure) || failure.Kind != test.kind || failure.Path != wantPath {
				t.Fatalf("expected kind %d at %q without output, got %T: %v", test.kind, wantPath, err, err)
			}
			if strings.Contains(err.Error(), "private-status-value") {
				t.Fatal("error exposed resource data")
			}
		})
	}
}

type nilGetter struct{}

func (*nilGetter) Get(context.Context, string, string, string) (*protocol.Resource, error) {
	return nil, nil
}

func resourceGetter(status map[string]any) refs.Getter {
	return getterFunc(func(context.Context, string, string, string) (*protocol.Resource, error) {
		return &protocol.Resource{Status: status}, nil
	})
}

func TestMalformedReferencesDoNotEchoPartialValues(t *testing.T) {
	const secret = "private-malformed-value"
	markers := []any{
		secret,
		map[string]any{"kind": secret},
		map[string]any{"apiVersion": secret, "kind": "Machine", "name": "upstream", "field": []any{secret}},
		map[string]any{"apiVersion": secret, "kind": "Machine", "name": []any{secret}},
	}
	for i, marker := range markers {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			spec := map[string]any{"items": []any{map[string]any{"a.b": map[string]any{refs.RefMarker: marker}}}}
			resolver := refs.New(getterFunc(func(context.Context, string, string, string) (*protocol.Resource, error) {
				t.Fatal("malformed reference reached provider")
				return nil, nil
			}))
			out, resolveErr := resolver.Resolve(context.Background(), spec)
			collected, collectErr := refs.CollectStrict(spec)
			if out != nil || collected != nil {
				t.Fatal("malformed reference returned partial output")
			}
			for _, err := range []error{resolveErr, collectErr} {
				var failure *refs.ResolutionError
				if !errors.As(err, &failure) || failure.Kind != refs.InvalidReference {
					t.Fatalf("expected typed invalid reference, got %T", err)
				}
				if failure.Reference != (refs.Ref{}) || failure.Path != `items[0]["a.b"]` {
					t.Fatalf("malformed reference attribution = %+v at %q", failure.Reference, failure.Path)
				}
				if strings.Contains(err.Error(), secret) {
					t.Fatal("error echoed malformed marker values")
				}
			}
			if got := spec["items"].([]any)[0].(map[string]any)["a.b"].(map[string]any)[refs.RefMarker]; !reflect.DeepEqual(got, marker) {
				t.Fatal("failed resolution changed the input marker")
			}
		})
	}
}
