package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"golem/internal/console"
	"golem/internal/host"
)

type watchBackend struct{}

func (watchBackend) Admit(context.Context, host.Input) (string, error) { return "router", nil }
func (watchBackend) Status(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"tasks":[{"id":"work_9","title":"Lint the tree","status":"active","environment":"local","threads":["console:main"],"outcome":"open"}],"commands":[{"id":"job_9","work":"work_9","status":"running","stopRequested":false}],"queuedReplies":0,"failedReplies":[],"timers":[],"failures":[]}`), nil
}
func (watchBackend) Search(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`[]`), nil
}
func (watchBackend) Inspect(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"work":{"id":"work_9"},"commands":[{"id":"job_9","work":"work_9","status":"running","spec":{"id":"job_9","command":"golangci-lint run ./...","directory":".","timeout_seconds":600},"stop":false,"notified":false,"result":null,"createdAt":1700000000000}]}`), nil
}
func (watchBackend) History(context.Context, string, int64, *int64) (json.RawMessage, error) {
	return json.RawMessage(`{"items":[]}`), nil
}
func (watchBackend) Resume(context.Context, string, string, string, string) (json.RawMessage, error) {
	return nil, errors.New("unused")
}

func TestWatchRendersOneFrameWithoutATerminal(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "gw")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(state)
	srv, err := console.Serve(context.Background(), state, watchBackend{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	var out, errOut bytes.Buffer
	if code := run([]string{"watch", "--state", state}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"ROUTER", "WATCHDOG", "Lint the tree", "golangci-lint run ./..."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b[?1049h") {
		t.Fatal("alternate screen must not be used when stdout is not a terminal")
	}
}

func TestWatchReportsAnUnreachableController(t *testing.T) {
	state := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"watch", "--state", state}, strings.NewReader(""), &out, &errOut); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "Controller unavailable") {
		t.Fatalf("stderr: %s", errOut.String())
	}
}
