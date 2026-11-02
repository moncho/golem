// Package host speaks JSON Lines with the TypeScript host process.
package host

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
)

// Error is an error answered by the far side of the connection.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

var ErrClosed = errors.New("host connection closed")

type Handler func(ctx context.Context, params json.RawMessage) (any, error)

type message struct {
	ID     string          `json:"id"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type reply struct {
	result json.RawMessage
	err    error
}

type Peer struct {
	in     io.Reader
	out    io.Writer
	logger *slog.Logger

	writeMu sync.Mutex

	mu       sync.Mutex
	handlers map[string]Handler
	pending  map[string]chan reply
	closed   bool
}

func NewPeer(in io.Reader, out io.Writer, logger *slog.Logger) *Peer {
	return &Peer{in: in, out: out, logger: logger, handlers: map[string]Handler{}, pending: map[string]chan reply{}}
}

func (p *Peer) Handle(method string, h Handler) {
	p.mu.Lock()
	p.handlers[method] = h
	p.mu.Unlock()
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (p *Peer) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err = p.out.Write(b)
	return err
}

func (p *Peer) Call(ctx context.Context, method string, params any, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := newID()
	ch := make(chan reply, 1)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrClosed
	}
	p.pending[id] = ch
	p.mu.Unlock()
	forget := func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}
	p.logger.Debug("call", "method", method, "id", id, "bytes", len(raw))
	if err := p.send(message{ID: id, Method: method, Params: raw}); err != nil {
		forget()
		p.logger.Error("write failed", "method", method, "id", id, "err", err)
		return ErrClosed
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if result == nil || len(r.result) == 0 {
			return nil
		}
		return json.Unmarshal(r.result, result)
	case <-ctx.Done():
		forget()
		return ctx.Err()
	}
}

// Run reads lines until end of file or until ctx is done. It does not wait
// for handlers that are still running.
func (p *Peer) Run(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- p.read(ctx) }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	p.mu.Lock()
	p.closed = true
	for id, ch := range p.pending {
		ch <- reply{err: ErrClosed}
		delete(p.pending, id)
	}
	p.mu.Unlock()
	return err
}

func (p *Peer) read(ctx context.Context) error {
	r := bufio.NewReader(p.in)
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			p.receive(ctx, line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (p *Peer) receive(ctx context.Context, line []byte) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil || m.ID == "" {
		p.logger.Debug("ignored line", "bytes", len(line))
		return
	}
	if m.Method != "" {
		go p.serve(ctx, m)
		return
	}
	p.mu.Lock()
	ch, ok := p.pending[m.ID]
	delete(p.pending, m.ID)
	p.mu.Unlock()
	if !ok {
		p.logger.Debug("unmatched response", "id", m.ID)
		return
	}
	if m.Error != nil {
		ch <- reply{err: m.Error}
		return
	}
	ch <- reply{result: m.Result}
}

func (p *Peer) serve(ctx context.Context, m message) {
	p.mu.Lock()
	h := p.handlers[m.Method]
	p.mu.Unlock()
	p.logger.Debug("request", "method", m.Method, "id", m.ID, "bytes", len(m.Params))
	var out message
	out.ID = m.ID
	if h == nil {
		out.Error = &Error{Code: "invalid", Message: "Unknown method."}
	} else if res, err := h(ctx, m.Params); err != nil {
		var he *Error
		if errors.As(err, &he) {
			out.Error = he
		} else {
			p.logger.Error("handler failed", "method", m.Method, "id", m.ID, "err", err)
			out.Error = &Error{Code: "internal", Message: "Request could not be completed."}
		}
	} else if b, err := json.Marshal(res); err != nil {
		p.logger.Error("reply not encodable", "method", m.Method, "id", m.ID, "err", err)
		out.Error = &Error{Code: "internal", Message: "Request could not be completed."}
	} else {
		out.Result = b
	}
	if out.Error == nil && len(out.Result) == 0 {
		out.Result = json.RawMessage("null")
	}
	if err := p.send(out); err != nil {
		p.logger.Error("write failed", "method", m.Method, "id", m.ID, "err", err)
	}
}
