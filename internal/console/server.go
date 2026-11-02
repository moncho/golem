// Package console serves the local controller socket and the attach client.
// The endpoint lives in the private state directory and is mode 0600; the
// application behind the Backend remains the only owner of messages and tasks.
package console

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"golem/internal/host"
)

const (
	genericMessage = "Request could not be completed. Check task references, input and controller status."
	maxRequest     = 1 << 20
	maxPath        = 100
	maxThread      = 200
	serverDeadline = 30 * time.Second
)

// ErrPathTooLong is returned when the socket path would not fit a local socket.
var ErrPathTooLong = errors.New("The state path is too long for a local socket. Choose a shorter --state directory.")

// Backend is what the controller socket forwards to.
type Backend interface {
	Admit(ctx context.Context, in host.Input) (string, error)
	Status(ctx context.Context) (json.RawMessage, error)
	Search(ctx context.Context, query string) (json.RawMessage, error)
	Inspect(ctx context.Context, work string) (json.RawMessage, error)
	History(ctx context.Context, thread string, before int64, after *int64) (json.RawMessage, error)
	Resume(ctx context.Context, work, text, thread, key string) (json.RawMessage, error)
}

// Request is one line sent to the controller.
type Request struct {
	Op     string `json:"op"`
	Key    string `json:"key,omitempty"`
	Text   string `json:"text,omitempty"`
	Work   string `json:"work,omitempty"`
	Thread string `json:"thread,omitempty"`
	Before int64  `json:"before,omitempty"`
	// After is a pointer so that after 0 (an ascending poll from the start)
	// differs from no position (the newest page).
	After *int64 `json:"after,omitempty"`
}

// Response is the single line answered for a request.
type Response struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *host.Error     `json:"error,omitempty"`
}

// SocketPath is the controller socket inside the state directory.
func SocketPath(state string) string { return filepath.Join(state, "controller.sock") }

// Server answers controller requests on the local socket.
type Server struct {
	path    string
	ln      net.Listener
	backend Backend
	logger  *slog.Logger
	ctx     context.Context
	cancel  context.CancelFunc

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	wg     sync.WaitGroup
	closed bool
}

// Serve starts listening. The caller must hold the state lock: a stale socket
// file is removed before listening.
func Serve(ctx context.Context, state string, backend Backend, logger *slog.Logger) (*Server, error) {
	path := SocketPath(state)
	if len(path) > maxPath {
		return nil, ErrPathTooLong
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		logger.Error("socket chmod failed", "err", err)
		ln.Close()
		os.Remove(path)
		return nil, errors.New("Could not secure the controller socket.")
	}
	cctx, cancel := context.WithCancel(ctx)
	s := &Server{path: path, ln: ln, backend: backend, logger: logger, ctx: cctx, cancel: cancel, conns: map[net.Conn]struct{}{}}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Close stops listening, drops open connections and removes the socket file.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.cancel()
	err := s.ln.Close()
	s.wg.Wait()
	os.Remove(s.path)
	return err
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer s.wg.Done()
	defer func() {
		c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	c.SetDeadline(time.Now().Add(serverDeadline))
	line, err := bufio.NewReader(io.LimitReader(c, maxRequest+1)).ReadBytes('\n')
	if err != nil || len(line) > maxRequest {
		return
	}
	resp := s.answer(line)
	out, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("response not encodable", "err", err)
		out = []byte(`{"ok":false,"error":{"code":"request_failed","message":"` + genericMessage + `"}}`)
	}
	c.Write(append(out, '\n'))
}

func failed() Response {
	return Response{Error: &host.Error{Code: "request_failed", Message: genericMessage}}
}

func (s *Server) answer(line []byte) Response {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.logger.Warn("malformed request", "size", len(line), "err", err)
		return failed()
	}
	thread := req.Thread
	if thread == "" {
		thread = "main"
	}
	if utf8.RuneCountInString(thread) > maxThread {
		s.logger.Warn("request thread too long", "op", req.Op)
		return failed()
	}
	var (
		result json.RawMessage
		err    error
	)
	switch req.Op {
	case "status":
		result, err = s.backend.Status(s.ctx)
	case "search":
		result, err = s.backend.Search(s.ctx, req.Text)
	case "inspect":
		result, err = s.backend.Inspect(s.ctx, req.Work)
	case "history":
		result, err = s.backend.History(s.ctx, "console:"+thread, req.Before, req.After)
	case "resume":
		if req.Key == "" {
			s.logger.Warn("request missing key", "op", req.Op)
			return failed()
		}
		// The host namespaces the key.
		result, err = s.backend.Resume(s.ctx, req.Work, req.Text, "console:"+thread, req.Key)
	case "input":
		if req.Key == "" || !hasText(line) {
			s.logger.Warn("request missing key or text", "op", req.Op)
			return failed()
		}
		var id string
		id, err = s.backend.Admit(s.ctx, host.Input{
			Key:      "console-input:" + req.Key,
			Text:     req.Text,
			Thread:   host.Thread{ID: "console:" + thread, Source: "console"},
			Sender:   "owner",
			Explicit: true,
		})
		if err == nil {
			result, err = json.Marshal(id)
		}
	default:
		s.logger.Warn("unknown op", "op", req.Op)
		return failed()
	}
	if err != nil {
		var he *host.Error
		if errors.As(err, &he) && he.Code == "rejected" {
			return Response{Error: he}
		}
		s.logger.Error("request failed", "op", req.Op, "err", err)
		return failed()
	}
	if len(result) == 0 {
		result = json.RawMessage("null")
	}
	return Response{OK: true, Result: result}
}

// hasText reports whether the request carries a string "text" field, as the
// protocol requires for input (an empty string is allowed, a missing one not).
func hasText(line []byte) bool {
	var probe struct {
		Text *string `json:"text"`
	}
	return json.Unmarshal(line, &probe) == nil && probe.Text != nil
}
