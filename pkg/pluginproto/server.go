package pluginproto

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/openctl/openctl/pkg/protocol"
)

// Handler is the plugin-author-facing contract. A provider binary implements
// it and passes it to Serve; the SDK handles all framing, dispatch, and error
// translation. Only Handshake, Apply, Get, List, and Delete are required —
// embed UnimplementedHandler to inherit safe "unsupported" defaults for the
// optional methods, then override the ones the provider supports (and
// advertise the matching capability in Handshake).
type Handler interface {
	// Handshake describes the provider: name, kinds, capabilities, schema.
	Handshake(ctx context.Context) (*HandshakeResult, error)
	// Configure injects the opaque provider config bag (may be nil).
	Configure(ctx context.Context, config json.RawMessage) error

	Apply(ctx context.Context, p ApplyParams) (*ApplyResult, error)
	// Get returns observed state; return NotFound(...) for a genuine miss.
	Get(ctx context.Context, p GetParams) (*GetResult, error)
	List(ctx context.Context, kind string) ([]*protocol.Resource, error)
	Delete(ctx context.Context, p DeleteParams) error

	// Optional — advertise the matching capability to have openctl call these.
	Plan(ctx context.Context, manifest *protocol.Resource) (*PlanResult, error)
	DryRun(ctx context.Context, manifest *protocol.Resource) (*DryRunResult, error)
	DoAction(ctx context.Context, p DoActionParams) (*DoActionResult, error)
	OwnerOf(ctx context.Context, p RefParams) (*OwnerOfResult, error)
	ChildrenOf(ctx context.Context, p RefParams) ([]ResourceRef, error)
}

// NotFound builds a CodeNotFound error for Handler.Get to signal a genuine
// miss (as opposed to a transient failure), which the openctl adapter maps to
// providers.NotFoundError.
func NotFound(msg string) *Error { return &Error{Code: CodeNotFound, Message: msg} }

// Unsupported builds a CodeUnsupported error for optional methods a provider
// doesn't implement.
func Unsupported(msg string) *Error { return &Error{Code: CodeUnsupported, Message: msg} }

// UnimplementedHandler provides safe defaults for the optional Handler
// methods. Embed it in a provider Handler so adding new optional protocol
// methods later doesn't break existing plugins.
type UnimplementedHandler struct{}

func (UnimplementedHandler) Configure(context.Context, json.RawMessage) error { return nil }

func (UnimplementedHandler) Plan(context.Context, *protocol.Resource) (*PlanResult, error) {
	return nil, Unsupported("plan not supported")
}

func (UnimplementedHandler) DryRun(context.Context, *protocol.Resource) (*DryRunResult, error) {
	return nil, Unsupported("dryRun not supported")
}

func (UnimplementedHandler) DoAction(context.Context, DoActionParams) (*DoActionResult, error) {
	return nil, Unsupported("actions not supported")
}

func (UnimplementedHandler) OwnerOf(context.Context, RefParams) (*OwnerOfResult, error) {
	return &OwnerOfResult{Owned: false}, nil
}

func (UnimplementedHandler) ChildrenOf(context.Context, RefParams) ([]ResourceRef, error) {
	return nil, nil
}

// Serve runs the plugin's message loop on os.Stdin/os.Stdout until stdin
// closes or a shutdown request arrives. Diagnostic output must go to stderr —
// stdout is the protocol channel. This is the normal entry point for a
// provider binary's serve subcommand.
func Serve(h Handler) error {
	return ServeConn(context.Background(), os.Stdin, os.Stdout, h)
}

// ServeConn runs the message loop over an arbitrary reader/writer pair. Used
// by Serve and, in tests, over an io.Pipe so a Handler can be exercised
// without spawning a process. Requests are handled sequentially in receive
// order; responses preserve request IDs.
func ServeConn(ctx context.Context, r io.Reader, w io.Writer, h Handler) error {
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(w)
	var writeMu sync.Mutex
	respond := func(msg *Message) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return enc.Encode(msg)
	}

	for {
		var req Message
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if req.Method == MethodShutdown {
			// Acknowledge, then stop the loop so the process can exit.
			_ = respond(&Message{ID: req.ID})
			return nil
		}
		result, herr := dispatch(ctx, h, &req)
		resp := &Message{ID: req.ID}
		if herr != nil {
			resp.Error = toWireError(herr)
		} else if result != nil {
			b, err := json.Marshal(result)
			if err != nil {
				resp.Error = &Error{Code: CodeInternal, Message: "marshal result: " + err.Error()}
			} else {
				resp.Result = b
			}
		}
		if err := respond(resp); err != nil {
			return err
		}
	}
}

// dispatch routes one request to the Handler and returns the result value to
// marshal (or an error). Params decode failures become CodeInvalid errors.
func dispatch(ctx context.Context, h Handler, req *Message) (any, error) {
	switch req.Method {
	case MethodHandshake:
		return h.Handshake(ctx)
	case MethodConfigure:
		var p ConfigureParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return nil, h.Configure(ctx, p.Config)
	case MethodApply:
		var p ApplyParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return h.Apply(ctx, p)
	case MethodGet:
		var p GetParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return h.Get(ctx, p)
	case MethodList:
		var p ListParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		resources, err := h.List(ctx, p.Kind)
		if err != nil {
			return nil, err
		}
		return ListResult{Resources: resources}, nil
	case MethodDelete:
		var p DeleteParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return nil, h.Delete(ctx, p)
	case MethodPlan:
		var p PlanParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return h.Plan(ctx, p.Manifest)
	case MethodDryRun:
		var p DryRunParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return h.DryRun(ctx, p.Manifest)
	case MethodDoAction:
		var p DoActionParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return h.DoAction(ctx, p)
	case MethodOwnerOf:
		var p RefParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		return h.OwnerOf(ctx, p)
	case MethodChildrenOf:
		var p RefParams
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
		children, err := h.ChildrenOf(ctx, p)
		if err != nil {
			return nil, err
		}
		return ChildrenOfResult{Children: children}, nil
	default:
		return nil, &Error{Code: CodeInvalid, Message: "unknown method: " + req.Method}
	}
}

func decodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return &Error{Code: CodeInvalid, Message: "decode params: " + err.Error()}
	}
	return nil
}

// toWireError preserves a *Error's code; any other error becomes CodeInternal.
func toWireError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}
