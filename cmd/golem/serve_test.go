package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golem/internal/console"
)

// lines collects a stream so a test can wait for text to appear.
type lines struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lines) collect(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		l.mu.Lock()
		l.buf.WriteString(sc.Text() + "\n")
		l.mu.Unlock()
	}
}

func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func golemBinary(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH")
	}
	bin, err := filepath.Abs(filepath.Join("..", "..", "bin", "golem"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("bin/golem is missing; run make build")
	}
	return bin
}

// shortDir keeps the controller socket path within the local socket limit.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "golem-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func fauxEnv() []string {
	env := []string{"GOLEM_PROVIDER=faux", "GOLEM_MODEL=faux-1"}
	for _, key := range []string{"PATH", "HOME", "USER", "SHELL", "TMPDIR", "LANG", "LC_ALL", "TERM"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

type served struct {
	cmd         *exec.Cmd
	out, errOut *lines
	done        chan struct{}
	exitCode    int
}

func startServe(t *testing.T, bin, state, workspace string) *served {
	t.Helper()
	cmd := exec.Command(bin, "serve", "--state", state, "--workspace", workspace, "--env-file", "/dev/null")
	cmd.Env = fauxEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &served{cmd: cmd, out: &lines{}, errOut: &lines{}, done: make(chan struct{})}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.out.collect(stdout) }()
	go func() { defer wg.Done(); s.errOut.collect(stderr) }()
	go func() {
		wg.Wait()
		cmd.Wait()
		s.exitCode = cmd.ProcessState.ExitCode()
		close(s.done)
	}()
	t.Cleanup(func() {
		select {
		case <-s.done:
		default:
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-s.done:
			case <-time.After(15 * time.Second):
				cmd.Process.Kill()
				<-s.done
			}
		}
	})
	return s
}

// waitOut waits for text on stdout and fails early if serve exits first.
func (s *served) waitOut(t *testing.T, text string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !strings.Contains(s.out.String(), text) {
		select {
		case <-s.done:
			if strings.Contains(s.out.String(), text) {
				return
			}
			t.Fatalf("serve exited with %d before %q\nstdout:\n%s\nstderr:\n%s", s.exitCode, text, s.out, s.errOut)
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q within %s\nstdout:\n%s\nstderr:\n%s", text, within, s.out, s.errOut)
		}
	}
}

func (s *served) exited(within time.Duration) bool {
	select {
	case <-s.done:
		return true
	case <-time.After(within):
		return false
	}
}

func TestServeRunsTheScriptedScenarioThroughTheSocket(t *testing.T) {
	bin := golemBinary(t)
	dir := shortDir(t)
	state, workspace := filepath.Join(dir, "st"), filepath.Join(dir, "ws")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	s := startServe(t, bin, state, workspace)
	s.waitOut(t, "service ready", 30*time.Second)

	raw, err := console.Call(state, console.Request{Op: "input", Text: "Do the independent task", Thread: "other"})
	if err != nil {
		t.Fatalf("input: %v\nstderr:\n%s", err, s.errOut)
	}
	var admitted string
	if err := json.Unmarshal(raw, &admitted); err != nil || admitted != "router" {
		t.Fatalf("admitted %s (%v)", raw, err)
	}
	s.waitOut(t, "Router [other]: independent result", 60*time.Second)

	info, err := os.Stat(console.SocketPath(state))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("socket mode %o", mode)
	}

	attach := exec.Command(bin, "attach", "--state", state)
	attach.Env = fauxEnv()
	attach.Stdin = strings.NewReader("/detach\n")
	var attachOut, attachErr bytes.Buffer
	attach.Stdout, attach.Stderr = &attachOut, &attachErr
	if err := attach.Run(); err != nil {
		t.Fatalf("attach: %v\nstdout:\n%s\nstderr:\n%s", err, attachOut.String(), attachErr.String())
	}
	if !strings.Contains(attachOut.String(), "Attached.") {
		t.Fatalf("attach output:\n%s", attachOut.String())
	}
	if s.exited(500 * time.Millisecond) {
		t.Fatalf("serve exited after detach with %d\nstderr:\n%s", s.exitCode, s.errOut)
	}
	if _, err := console.Call(state, console.Request{Op: "status"}); err != nil {
		t.Fatalf("status after detach: %v", err)
	}

	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if !s.exited(20 * time.Second) {
		t.Fatal("serve did not stop after SIGTERM")
	}
	if s.exitCode != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", s.exitCode, s.errOut)
	}
	if _, err := os.Stat(console.SocketPath(state)); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
}

func TestServeRefusesWhileAnotherHoldsTheState(t *testing.T) {
	bin := golemBinary(t)
	dir := shortDir(t)
	state, workspace := filepath.Join(dir, "st"), filepath.Join(dir, "ws")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	first := startServe(t, bin, state, workspace)
	first.waitOut(t, "service ready", 30*time.Second)

	second := startServe(t, bin, state, workspace)
	if !second.exited(12 * time.Second) {
		t.Fatal("second serve still running")
	}
	if second.exitCode != 1 {
		t.Fatalf("exit %d", second.exitCode)
	}
	if !strings.Contains(second.errOut.String(), "in use by another golem") {
		t.Fatalf("stderr:\n%s", second.errOut)
	}
	if first.exited(0) {
		t.Fatal("first serve stopped")
	}
	if _, err := console.Call(state, console.Request{Op: "status"}); err != nil {
		t.Fatalf("first serve no longer answers: %v", err)
	}
}
