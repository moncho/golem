package main

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golem/internal/console"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func waitUntil(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A Worker that asks for the remote environment runs its command on an
// independent `golem executor --listen`, not in the serving process.
func TestServeRoutesCommandsToARemoteExecutor(t *testing.T) {
	bin := golemBinary(t)
	state, workspace := shortDir(t), shortDir(t)
	remoteState, remoteWorkspace := shortDir(t), shortDir(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	executor := exec.Command(bin, "executor", "--listen", addr, "--workspace", remoteWorkspace, "--state", remoteState)
	executor.Env = append(fauxEnv(), "GOLEM_NODE_TOKEN=test-remote-token")
	var executorOut lockedBuffer
	executor.Stdout, executor.Stderr = &executorOut, &executorOut
	if err := executor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { executor.Process.Kill(); executor.Wait() })
	waitUntil(t, "the remote executor to listen", 10*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err == nil
	})

	serve := exec.Command(bin, "serve", "--state", state, "--workspace", workspace, "--env-file", "/dev/null")
	serve.Env = append(fauxEnv(), "GOLEM_NODE_URL=http://"+addr, "GOLEM_NODE_TOKEN=test-remote-token")
	var out, errOut lockedBuffer
	serve.Stdout, serve.Stderr = &out, &errOut
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serve.Process.Signal(os.Interrupt); serve.Wait() })
	waitUntil(t, "service ready", 30*time.Second, func() bool { return strings.Contains(out.String(), "service ready") })
	if !strings.Contains(errOut.String(), "local, remote http://"+addr) {
		t.Fatalf("configured line should name the remote environment:\n%s", errOut.String())
	}

	if _, err := console.Call(state, console.Request{Op: "input", Key: "r1", Text: "Do the independent task remotely", Thread: "other"}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the Router's reply", 30*time.Second, func() bool { return strings.Contains(out.String(), "Router [other]: independent result") })

	remoteJobs, _ := filepath.Glob(filepath.Join(remoteState, "*.json"))
	if len(remoteJobs) != 1 {
		t.Fatalf("expected one job record at the remote executor, found %v", remoteJobs)
	}
	record, _ := os.ReadFile(remoteJobs[0])
	if !strings.Contains(string(record), `"status":"done"`) {
		t.Fatalf("remote job record: %s", record)
	}
	localJobs, _ := filepath.Glob(filepath.Join(state, "execution", "*.json"))
	if len(localJobs) != 0 {
		t.Fatalf("the local executor must not have run anything: %v", localJobs)
	}
}
