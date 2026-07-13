package secrets

import (
	"context"
	"fmt"
	"strings"
)

// ActionProviderName is the registry name of the action-output secret provider.
const ActionProviderName = "action"

// ActionOutput is the subset of a resource action's result the secret provider
// can surface. Mirrors providers.ActionResult's value-bearing fields without
// importing that package (avoids a controller→secrets layering edge).
type ActionOutput struct {
	DownloadContent string
	Message         string
	URL             string
}

// ActionInvoker runs a resource action and returns its output. main.go wires
// this to the provider Registry's DoAction.
type ActionInvoker func(ctx context.Context, apiVersion, kind, name, action string) (*ActionOutput, error)

// ActionProvider resolves a $secret by running a resource action and returning
// one of its output fields — bridging an action's result (e.g. a Cloudflare
// Tunnel's run token from its get-token action) into a value the manifest can
// reference without the operator copying it by hand. Because it's an ordinary
// SecretProvider, the dispatcher's existing discipline applies: only the raw
// {$secret: …} marker is persisted (git-safe); the resolved token is transient.
//
// Key grammar: "<apiVersion>/<kind>/<name>#<action>[:<field>]" where <field> is
// one of download (default — where get-token puts the token), message, or url.
// Example:
//
//	token: {$secret: {provider: action,
//	  key: "cloudflare.openctl.io/v1/Tunnel/home#get-token"}}
//
// Note the action reruns on every resolve (each Apply); actions used this way
// should be idempotent reads.
type ActionProvider struct {
	invoke ActionInvoker
}

// NewActionProvider builds the provider around an action invoker.
func NewActionProvider(invoke ActionInvoker) *ActionProvider {
	return &ActionProvider{invoke: invoke}
}

func (p *ActionProvider) Name() string { return ActionProviderName }

func (p *ActionProvider) Resolve(ctx context.Context, key string) (string, error) {
	apiVersion, kind, name, action, field, err := parseActionKey(key)
	if err != nil {
		return "", err
	}
	if p.invoke == nil {
		return "", fmt.Errorf("action secret provider is not wired to an invoker")
	}
	out, err := p.invoke(ctx, apiVersion, kind, name, action)
	if err != nil {
		return "", fmt.Errorf("action secret %q: %w", key, err)
	}
	if out == nil {
		return "", fmt.Errorf("action secret %q: action returned no result", key)
	}
	var val string
	switch field {
	case "", "download":
		val = out.DownloadContent
	case "message":
		val = out.Message
	case "url":
		val = out.URL
	default:
		return "", fmt.Errorf("action secret %q: unknown field %q (want download|message|url)", key, field)
	}
	if val == "" {
		return "", fmt.Errorf("action secret %q: %s field is empty", key, fieldOrDefault(field))
	}
	return val, nil
}

func fieldOrDefault(field string) string {
	if field == "" {
		return "download"
	}
	return field
}

// parseActionKey splits "<apiVersion>/<kind>/<name>#<action>[:<field>]".
// apiVersion is a "<domain>/<version>" pair, so the ref before '#' has exactly
// four slash-separated segments.
func parseActionKey(key string) (apiVersion, kind, name, action, field string, err error) {
	ref, actionPart, ok := strings.Cut(key, "#")
	if !ok {
		return "", "", "", "", "", fmt.Errorf("action secret key %q: want \"<apiVersion>/<kind>/<name>#<action>\"", key)
	}
	segs := strings.Split(ref, "/")
	if len(segs) != 4 || segs[0] == "" || segs[1] == "" || segs[2] == "" || segs[3] == "" {
		return "", "", "", "", "", fmt.Errorf("action secret key %q: ref must be <domain>/<version>/<kind>/<name>", key)
	}
	apiVersion = segs[0] + "/" + segs[1]
	kind = segs[2]
	name = segs[3]
	action, field, _ = strings.Cut(actionPart, ":")
	if action == "" {
		return "", "", "", "", "", fmt.Errorf("action secret key %q: missing action after '#'", key)
	}
	return apiVersion, kind, name, action, field, nil
}
