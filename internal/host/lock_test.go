package host

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestLockIsInheritedByChild(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH")
	}
	dir := t.TempDir()
	f, err := Lock(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := Spawn(context.Background(), "testdata/fake-host.mjs", nil, []*os.File{f}, logger)
	if err != nil {
		t.Fatal(err)
	}
	go p.Peer.Run(context.Background())
	defer p.Close(5 * time.Second)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(dir, 300*time.Millisecond); err == nil || !strings.Contains(err.Error(), "in use by another golem") {
		t.Fatalf("second lock: %v", err)
	}
	if err := p.Close(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	g, err := Lock(dir, time.Second)
	if err != nil {
		t.Fatalf("lock after child exit: %v", err)
	}
	g.Close()
}
