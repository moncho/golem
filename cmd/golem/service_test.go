package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golem/internal/host"
)

// A host that cannot be respawned ends the service instead of leaving it
// running without a host.
func TestRestartHostFailureEndsTheService(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH")
	}
	dir := shortDir(t)
	workspace := filepath.Join(dir, "ws")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := startService(ctx, serviceOptions{State: filepath.Join(dir, "st"), Workspace: workspace,
		Values: map[string]string{"GOLEM_PROVIDER": "faux", "GOLEM_MODEL": "faux-1"}, Retain: time.Hour, Proactive: "off",
		Send: func(host.Thread, string) error { return nil }}, io.Discard, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := os.Chmod(s.script, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(s.script, 0o600)
	if err := s.RestartHost(ctx); err == nil {
		t.Fatal("restart succeeded with an unreadable host")
	}
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait returned no error after a failed restart")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after a failed restart")
	}
}

func TestRestartHostHonoursContext(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH")
	}
	dir := shortDir(t)
	workspace := filepath.Join(dir, "ws")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := startService(context.Background(), serviceOptions{State: filepath.Join(dir, "st"), Workspace: workspace,
		Values: map[string]string{"GOLEM_PROVIDER": "faux", "GOLEM_MODEL": "faux-1"}, Retain: time.Hour, Proactive: "off",
		Send: func(host.Thread, string) error { return nil }}, io.Discard, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.RestartHost(ctx); err == nil {
		t.Fatal("restart ignored a cancelled context")
	}
}

// A host that never answers cannot stall a caller whose context has no
// deadline: every Go-to-host call carries its own timeout.
func TestHostCallTimesOut(t *testing.T) {
	old := hostCallTimeout
	hostCallTimeout = 50 * time.Millisecond
	defer func() { hostCallTimeout = old }()
	s := &service{proc: &host.Process{Peer: host.NewPeer(strings.NewReader(""), io.Discard, slog.New(slog.DiscardHandler))}}
	done := make(chan error, 1)
	go func() { _, err := s.Admit(context.Background(), host.Input{Key: "k"}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call did not time out")
	}
}
