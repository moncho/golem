package console

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golem/internal/host"
)

type call struct {
	Method string
	Input  host.Input
	Text   string
	Thread string
	Before int64
	After  *int64
	Key    string
}

type fakeBackend struct {
	mu      sync.Mutex
	calls   []call
	err     error
	history func(thread string, before int64, after *int64) json.RawMessage
}

func (f *fakeBackend) record(c call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	return f.err
}

func (f *fakeBackend) last() call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func (f *fakeBackend) Admit(_ context.Context, in host.Input) (string, error) {
	return "admitted", f.record(call{Method: "admit", Input: in})
}
func (f *fakeBackend) Status(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), f.record(call{Method: "status"})
}
func (f *fakeBackend) Search(_ context.Context, q string) (json.RawMessage, error) {
	return json.RawMessage(`[]`), f.record(call{Method: "search", Text: q})
}
func (f *fakeBackend) Inspect(_ context.Context, w string) (json.RawMessage, error) {
	return json.RawMessage(`{}`), f.record(call{Method: "inspect", Text: w})
}
func (f *fakeBackend) History(_ context.Context, thread string, before int64, after *int64) (json.RawMessage, error) {
	if err := f.record(call{Method: "history", Thread: thread, Before: before, After: after}); err != nil {
		return nil, err
	}
	if f.history != nil {
		return f.history(thread, before, after), nil
	}
	return json.RawMessage(`{"items":[],"before":null}`), nil
}
func (f *fakeBackend) Resume(_ context.Context, work, text, thread, key string) (json.RawMessage, error) {
	return json.RawMessage(`{"resumed":true}`), f.record(call{Method: "resume", Text: text, Thread: thread, Key: key, Input: host.Input{Key: work}})
}

func shortState(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func start(t *testing.T, b Backend) (string, *Server) {
	t.Helper()
	state := shortState(t)
	s, err := Serve(context.Background(), state, b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return state, s
}

func raw(t *testing.T, state, line string) Response {
	t.Helper()
	c, err := net.Dial("unix", SocketPath(state))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	var r Response
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("bad response %q: %v", data, err)
	}
	return r
}

func TestServeOwnsPrivateSocket(t *testing.T) {
	state := shortState(t)
	if err := os.WriteFile(SocketPath(state), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Serve(context.Background(), state, &fakeBackend{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(SocketPath(state))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("not a socket: %v", info.Mode())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	s.Close()
	if _, err := os.Stat(SocketPath(state)); !os.IsNotExist(err) {
		t.Fatalf("socket not removed: %v", err)
	}
}

func TestInputMapsToAdmit(t *testing.T) {
	b := &fakeBackend{}
	state, _ := start(t, b)
	if r := raw(t, state, `{"op":"input","key":"k","text":"hi"}`+"\n"); !r.OK {
		t.Fatalf("%+v", r)
	}
	want := host.Input{Key: "console-input:k", Text: "hi", Thread: host.Thread{ID: "console:main", Source: "console"}, Sender: "owner", Explicit: true}
	if got := b.last(); got.Method != "admit" || got.Input != want {
		t.Fatalf("got %+v", got)
	}
	raw(t, state, `{"op":"input","key":"k","text":"hi","thread":"side"}`+"\n")
	if got := b.last().Input.Thread.ID; got != "console:side" {
		t.Fatalf("thread %q", got)
	}
	for _, bad := range []string{`{"op":"input","text":"hi"}`, `{"op":"input","key":"k"}`, `{"op":"resume","text":"x"}`} {
		if r := raw(t, state, bad+"\n"); r.OK || r.Error.Code != "request_failed" {
			t.Fatalf("%s: %+v", bad, r)
		}
	}
	long := `{"op":"status","thread":"` + strings.Repeat("a", 201) + `"}`
	if r := raw(t, state, long+"\n"); r.OK {
		t.Fatal("over-long thread accepted")
	}
}

func TestOtherOpsMapToBackend(t *testing.T) {
	b := &fakeBackend{}
	state, _ := start(t, b)
	raw(t, state, `{"op":"resume","key":"r","work":"w1","text":"go","thread":"side"}`+"\n")
	if c := b.last(); c.Method != "resume" || c.Key != "r" || c.Thread != "console:side" || c.Text != "go" || c.Input.Key != "w1" {
		t.Fatalf("%+v", c)
	}
	r := raw(t, state, `{"op":"history","before":7}`+"\n")
	if c := b.last(); c.Method != "history" || c.Thread != "console:main" || c.Before != 7 || !r.OK {
		t.Fatalf("%+v %+v", c, r)
	}
	raw(t, state, `{"op":"history","after":9}`+"\n")
	if c := b.last(); c.After == nil || *c.After != 9 {
		t.Fatalf("%+v", c)
	}
	r = raw(t, state, `{"op":"status"}`+"\n")
	if string(r.Result) != `{"ok":true}` {
		t.Fatalf("%s", r.Result)
	}
}

func TestRejectedErrorPassesThrough(t *testing.T) {
	b := &fakeBackend{err: &host.Error{Code: "rejected", Message: "Unknown task or thread."}}
	state, _ := start(t, b)
	r := raw(t, state, `{"op":"inspect","work":"zz"}`+"\n")
	if r.OK || r.Error.Code != "rejected" || r.Error.Message != "Unknown task or thread." {
		t.Fatalf("%+v", r)
	}
}

func TestOtherErrorsBecomeGeneric(t *testing.T) {
	b := &fakeBackend{err: errors.New("boom")}
	state, _ := start(t, b)
	r := raw(t, state, `{"op":"status"}`+"\n")
	if r.OK || r.Error.Code != "request_failed" || r.Error.Message != genericMessage || strings.Contains(r.Error.Message, "boom") {
		t.Fatalf("%+v", r)
	}
}

func TestServerRejectsMalformedRequest(t *testing.T) {
	b := &fakeBackend{}
	state, _ := start(t, b)
	r := raw(t, state, "{not json\n")
	if r.OK || r.Error.Code != "request_failed" || r.Error.Message != genericMessage {
		t.Fatalf("%+v", r)
	}
	if r := raw(t, state, `{"op":"nope"}`+"\n"); r.OK || r.Error.Code != "request_failed" {
		t.Fatalf("%+v", r)
	}
	if r := raw(t, state, `{"op":"status"}`+"\n"); !r.OK {
		t.Fatalf("%+v", r)
	}
}

func TestSocketPathTooLong(t *testing.T) {
	state := "/tmp/" + strings.Repeat("d", 100)
	if got := SocketPath(state); got != state+"/controller.sock" {
		t.Fatal(got)
	}
	_, err := Serve(context.Background(), state, &fakeBackend{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, ErrPathTooLong) {
		t.Fatalf("got %v", err)
	}
	if err.Error() != "The state path is too long for a local socket. Choose a shorter --state directory." {
		t.Fatal(err)
	}
}

func TestCallClientErrors(t *testing.T) {
	state := shortState(t)
	if _, err := Call(state, Request{Op: "status"}); err == nil || err.Error() != "Controller unavailable. Start golem serve with the same --state directory." {
		t.Fatalf("got %v", err)
	}
	b := &fakeBackend{err: &host.Error{Code: "rejected", Message: "No."}}
	state, _ = start(t, b)
	_, err := Call(state, Request{Op: "status"})
	var he *host.Error
	if !errors.As(err, &he) || he.Message != "No." {
		t.Fatalf("got %v", err)
	}
	b.mu.Lock()
	b.err = nil
	b.mu.Unlock()
	if _, err := Call(state, Request{Op: "input", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if k := b.last().Input.Key; !strings.HasPrefix(k, "console-input:") || len(k) < 20 {
		t.Fatalf("key %q", k)
	}
}
