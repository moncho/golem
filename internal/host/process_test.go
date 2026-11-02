package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"slices"
	"sync"
	"testing"
	"time"
)

func spawnFake(t *testing.T) (*Process, <-chan error) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := Spawn(context.Background(), "testdata/fake-host.mjs", nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	run := make(chan error, 1)
	go func() { run <- p.Peer.Run(context.Background()) }()
	t.Cleanup(func() { p.Close(5 * time.Second) })
	return p, run
}

func callCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestCallsResolveOutOfOrder(t *testing.T) {
	p, _ := spawnFake(t)
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	for _, c := range []struct {
		name  string
		delay int
	}{{"slow", 50}, {"fast", 0}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := map[string]any{"name": c.name, "delayMs": c.delay}
			var out map[string]any
			if err := p.Peer.Call(callCtx(t), "echo", in, &out); err != nil {
				t.Error(err)
				return
			}
			if out["name"] != c.name {
				t.Errorf("got %v for %s", out, c.name)
			}
			mu.Lock()
			order = append(order, c.name)
			mu.Unlock()
		}()
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	if !slices.Equal(order, []string{"fast", "slow"}) {
		t.Fatalf("order %v", order)
	}
}

func TestErrorResponseIsTyped(t *testing.T) {
	p, _ := spawnFake(t)
	err := p.Peer.Call(callCtx(t), "fail", nil, nil)
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr.Code != "rejected" || hostErr.Message != "nope" {
		t.Fatalf("got %v", err)
	}
}

func TestHostToGoHandlerAnswers(t *testing.T) {
	p, _ := spawnFake(t)
	var got json.RawMessage
	p.Peer.Handle("log", func(ctx context.Context, params json.RawMessage) (any, error) {
		got = params
		return map[string]bool{"ok": true}, nil
	})
	var out map[string]bool
	if err := p.Peer.Call(callCtx(t), "callback", nil, &out); err != nil {
		t.Fatal(err)
	}
	if !out["ok"] {
		t.Fatalf("result %v", out)
	}
	var in map[string]string
	if err := json.Unmarshal(got, &in); err != nil || in["level"] != "info" || in["message"] != "hi" {
		t.Fatalf("params %s", got)
	}
}

func TestPeerIgnoresNonJSONLines(t *testing.T) {
	p, _ := spawnFake(t)
	if err := p.Peer.Call(callCtx(t), "stray", nil, nil); err != nil {
		t.Fatal(err)
	}
	var out map[string]int
	if err := p.Peer.Call(callCtx(t), "echo", map[string]int{"n": 7}, &out); err != nil || out["n"] != 7 {
		t.Fatalf("%v %v", out, err)
	}
}

func TestCloseEndsHostOnEOF(t *testing.T) {
	p, _ := spawnFake(t)
	if err := p.Close(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if p.ExitCode() != 0 {
		t.Fatalf("exit code %d", p.ExitCode())
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("Done is not closed")
	}
}

func TestSpawnEnvIsAllowlisted(t *testing.T) {
	t.Setenv("GOLEM_SECRET_TEST", "x")
	p, _ := spawnFake(t)
	var keys []string
	if err := p.Peer.Call(callCtx(t), "env", nil, &keys); err != nil {
		t.Fatal(err)
	}
	allowed := []string{"PATH", "HOME", "USER", "SHELL", "TMPDIR", "LANG", "LC_ALL", "TERM"}
	for _, k := range keys {
		if k == "__CF_USER_TEXT_ENCODING" { // added by macOS on exec
			continue
		}
		if !slices.Contains(allowed, k) {
			t.Errorf("unexpected variable %s", k)
		}
	}
	if !slices.Contains(keys, "PATH") {
		t.Errorf("PATH missing from %v", keys)
	}
}

func TestCallAfterCloseReturnsErrClosed(t *testing.T) {
	p, run := spawnFake(t)
	if err := p.Close(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := <-run; err != nil {
		t.Fatalf("Run after Close: %v", err)
	}
	if err := p.Peer.Call(callCtx(t), "echo", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
}

func TestReplyBeforeExitIsDelivered(t *testing.T) {
	p, run := spawnFake(t)
	var out string
	if err := p.Peer.Call(callCtx(t), "bye", nil, &out); err != nil || len(out) != 8_000_000 {
		t.Fatalf("got %q, %v", out, err)
	}
	<-p.Done()
	if err := <-run; err != nil {
		t.Fatalf("Run after host exit: %v", err)
	}
}
