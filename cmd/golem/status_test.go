package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golem/internal/console"
)

func TestStatusPrintsTasksAndExecutionSummary(t *testing.T) {
	bin := golemBinary(t)
	dir := shortDir(t)
	state, workspace := filepath.Join(dir, "st"), filepath.Join(dir, "ws")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	s := startServe(t, bin, state, workspace)
	s.waitOut(t, "service ready", 30*time.Second)
	if _, err := console.Call(state, console.Request{Op: "input", Text: "Do the independent task", Thread: "other"}); err != nil {
		t.Fatal(err)
	}
	s.waitOut(t, "Router [other]: independent result", 60*time.Second)
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if !s.exited(20 * time.Second) {
		t.Fatal("serve did not stop")
	}

	cmd := exec.Command(bin, "status", "--state", state)
	cmd.Env = fauxEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("status: %v\nstderr:\n%s", err, stderr.String())
	}
	var out struct {
		Tasks     []struct{ Status string } `json:"tasks"`
		Failures  []any                     `json:"failures"`
		Execution map[string]int            `json:"execution"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("%v\n%s", err, stdout.String())
	}
	if out.Failures == nil || len(out.Failures) != 0 {
		t.Fatalf("failures %v\n%s", out.Failures, stdout.String())
	}
	if len(out.Tasks) != 1 || out.Execution["done"] != 1 {
		t.Fatalf("status\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "\n  \"execution\"") {
		t.Fatalf("not indented\n%s", stdout.String())
	}
}

func TestStatusFailsWithoutState(t *testing.T) {
	bin := golemBinary(t)
	state := filepath.Join(shortDir(t), "missing")
	cmd := exec.Command(bin, "status", "--state", state)
	cmd.Env = fauxEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("err %v", err)
	}
	if strings.TrimSpace(stderr.String()) != "No Pi state exists at the selected directory." {
		t.Fatalf("stderr %q", stderr.String())
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("status created the state directory: %v", err)
	}
}
