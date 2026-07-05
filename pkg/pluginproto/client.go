package pluginproto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/openctl/openctl/pkg/protocol"
)

// Client is openctl's end of the v2 plugin protocol. It writes requests to
// the plugin's stdin and correlates responses read from its stdout by ID. A
// single background reader goroutine dispatches each response to the pending
// call that owns its ID, so concurrent Call invocations are safe.
//
// Construct a Client either by attaching to an existing stream pair
// (NewClient — used in tests over an io.Pipe) or by spawning a process
// (Spawn). Close shuts the reader down and, for spawned clients, waits for
// the process to exit.
type Client struct {
	w   io.WriteCloser
	enc *json.Encoder

	writeMu sync.Mutex // serializes writes to w

	mu      sync.Mutex // guards pending, nextID, closedErr
	pending map[uint64]chan *Message
	nextID  uint64
	closed  bool
	closerr error

	cmd  *exec.Cmd     // non-nil for Spawn'd clients
	done chan struct{} // closed when the reader loop exits
}

// NewClient attaches a Client to an already-open stream pair. r is the
// plugin's stdout (responses); w is its stdin (requests). The caller owns
// process lifecycle. Used directly in tests; Spawn wraps this for real
// plugin binaries.
func NewClient(r io.Reader, w io.WriteCloser) *Client {
	c := &Client{
		w:       w,
		enc:     json.NewEncoder(w),
		pending: make(map[uint64]chan *Message),
		done:    make(chan struct{}),
	}
	go c.readLoop(r)
	return c
}

// Spawn starts cmd, wires its stdio to a new Client, and returns it. cmd's
// Stdin and Stdout are overwritten; set cmd.Stderr beforehand to capture the
// plugin's diagnostic output. The caller must eventually call Close.
func Spawn(cmd *exec.Cmd) (*Client, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start plugin: %w", err)
	}
	c := NewClient(stdout, stdin)
	c.cmd = cmd
	return c, nil
}

// readLoop decodes response Messages until the stream ends, handing each to
// the channel registered under its ID. When the stream closes, every pending
// call is failed so no caller blocks forever on a dead plugin.
func (c *Client) readLoop(r io.Reader) {
	defer close(c.done)
	dec := json.NewDecoder(r)
	for {
		var msg Message
		if err := dec.Decode(&msg); err != nil {
			c.failAll(err)
			return
		}
		c.mu.Lock()
		ch, ok := c.pending[msg.ID]
		if ok {
			delete(c.pending, msg.ID)
		}
		c.mu.Unlock()
		if ok {
			m := msg
			ch <- &m
		}
	}
}

// failAll marks the client closed and unblocks every pending call with err.
func (c *Client) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if err == nil || errors.Is(err, io.EOF) {
		err = fmt.Errorf("plugin connection closed")
	}
	c.closerr = err
	for id, ch := range c.pending {
		ch <- &Message{ID: id, Error: &Error{Code: CodeInternal, Message: err.Error()}}
		delete(c.pending, id)
	}
}

// call sends a request and blocks until the matching response arrives, the
// context is canceled, or the connection dies. params is marshaled into the
// request; result (if non-nil) is unmarshaled from a successful response.
func (c *Client) call(ctx context.Context, method string, params, result any) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal %s params: %w", method, err)
		}
		raw = b
	}

	c.mu.Lock()
	if c.closed {
		err := c.closerr
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	ch := make(chan *Message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(&Message{ID: id, Method: method, Params: raw}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("write %s request: %w", method, err)
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case msg := <-ch:
		if msg.Error != nil {
			return msg.Error
		}
		if result != nil && len(msg.Result) > 0 {
			if err := json.Unmarshal(msg.Result, result); err != nil {
				return fmt.Errorf("decode %s result: %w", method, err)
			}
		}
		return nil
	}
}

func (c *Client) write(msg *Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.enc.Encode(msg)
}

// Handshake performs the initial negotiation. Call it once, first.
func (c *Client) Handshake(ctx context.Context) (*HandshakeResult, error) {
	var res HandshakeResult
	if err := c.call(ctx, MethodHandshake, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Configure injects the opaque provider config bag. cfg is marshaled as-is.
func (c *Client) Configure(ctx context.Context, cfg any) error {
	var raw json.RawMessage
	if cfg != nil {
		b, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("marshal configure: %w", err)
		}
		raw = b
	}
	return c.call(ctx, MethodConfigure, ConfigureParams{Config: raw}, nil)
}

// Apply converges a resource, threading prior state/private through.
func (c *Client) Apply(ctx context.Context, p ApplyParams) (*ApplyResult, error) {
	var res ApplyResult
	if err := c.call(ctx, MethodApply, p, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Get returns observed state. A CodeNotFound *Error is returned verbatim so
// the adapter can translate it into providers.NotFoundError.
func (c *Client) Get(ctx context.Context, p GetParams) (*GetResult, error) {
	var res GetResult
	if err := c.call(ctx, MethodGet, p, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// List enumerates observed resources of a kind.
func (c *Client) List(ctx context.Context, kind string) ([]*protocol.Resource, error) {
	var res ListResult
	if err := c.call(ctx, MethodList, ListParams{Kind: kind}, &res); err != nil {
		return nil, err
	}
	return res.Resources, nil
}

// Delete removes a resource (idempotent on the plugin side).
func (c *Client) Delete(ctx context.Context, p DeleteParams) error {
	return c.call(ctx, MethodDelete, p, nil)
}

// Plan expands a composite manifest into child manifests.
func (c *Client) Plan(ctx context.Context, manifest *protocol.Resource) (*PlanResult, error) {
	var res PlanResult
	if err := c.call(ctx, MethodPlan, PlanParams{Manifest: manifest}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// DryRun previews an Apply.
func (c *Client) DryRun(ctx context.Context, manifest *protocol.Resource) (*DryRunResult, error) {
	var res DryRunResult
	if err := c.call(ctx, MethodDryRun, DryRunParams{Manifest: manifest}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// DoAction invokes a runtime action.
func (c *Client) DoAction(ctx context.Context, p DoActionParams) (*DoActionResult, error) {
	var res DoActionResult
	if err := c.call(ctx, MethodDoAction, p, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// OwnerOf asks the plugin whether it owns a resource.
func (c *Client) OwnerOf(ctx context.Context, p RefParams) (*OwnerOfResult, error) {
	var res OwnerOfResult
	if err := c.call(ctx, MethodOwnerOf, p, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ChildrenOf asks the plugin for the children a resource composes.
func (c *Client) ChildrenOf(ctx context.Context, p RefParams) ([]ResourceRef, error) {
	var res ChildrenOfResult
	if err := c.call(ctx, MethodChildrenOf, p, &res); err != nil {
		return nil, err
	}
	return res.Children, nil
}

// Close asks the plugin to shut down (best-effort), closes stdin, and — for
// spawned clients — waits for the process to exit. Safe to call more than
// once.
func (c *Client) Close(ctx context.Context) error {
	// Best-effort graceful shutdown request; ignore its error since the
	// plugin may exit before replying.
	_ = c.call(ctx, MethodShutdown, nil, nil)

	c.writeMu.Lock()
	err := c.w.Close()
	c.writeMu.Unlock()

	<-c.done
	if c.cmd != nil {
		// A plugin's exit code after a shutdown request varies by
		// implementation; reaping the process is what matters here, so we
		// wait but don't treat a non-zero exit as a Close failure.
		_ = c.cmd.Wait()
	}
	return err
}
