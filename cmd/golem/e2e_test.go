package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golem/internal/console"
)

func socketStatus(t *testing.T, state string) demoStatus {
	t.Helper()
	raw, err := console.Call(state, console.Request{Op: "status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var st demoStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("status %s: %v", raw, err)
	}
	return st
}

func socketInput(t *testing.T, state, text, thread string) string {
	t.Helper()
	raw, err := console.Call(state, console.Request{Op: "input", Text: text, Thread: thread})
	if err != nil {
		t.Fatalf("input %q: %v", text, err)
	}
	var admitted string
	if err := json.Unmarshal(raw, &admitted); err != nil {
		t.Fatalf("input %q answered %s: %v", text, raw, err)
	}
	return admitted
}

// children lists the direct child processes of pid.
func children(t *testing.T, pid int) []int {
	t.Helper()
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		t.Fatalf("pgrep: %v", err)
	}
	var pids []int
	for _, field := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(field); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestServeSurvivesParentKillAndReplacement(t *testing.T) {
	bin := golemBinary(t)
	dir := shortDir(t)
	state, workspace := filepath.Join(dir, "st"), filepath.Join(dir, "ws")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}

	a := startServe(t, bin, state, workspace)
	a.waitOut(t, "service ready", 30*time.Second)
	if got := socketInput(t, state, "Start the long task", "main"); got != "router" {
		t.Fatalf("long task admitted as %q", got)
	}
	var original string
	deadline := time.Now().Add(30 * time.Second)
	for original == "" {
		for _, c := range socketStatus(t, state).Commands {
			if c.Status == "running" {
				original = c.ID
			}
		}
		if original == "" && time.Now().After(deadline) {
			t.Fatalf("no running command within 30s\nstderr:\n%s", a.errOut)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A's children are its host (in A's process group) and the long command,
	// which leads its own group. The command outlives A's SIGKILL until the
	// replacement's start kills it; the test also kills its group when it
	// ends, in case it fails before then.
	var hostPID int
	for _, pid := range children(t, a.cmd.Process.Pid) {
		pgid, err := syscall.Getpgid(pid)
		if err != nil {
			continue
		}
		if pgid == pid {
			t.Cleanup(func() { syscall.Kill(-pgid, syscall.SIGKILL) })
		} else {
			hostPID = pid
		}
	}
	if hostPID == 0 {
		t.Fatal("serve has no host process")
	}
	t.Cleanup(func() {
		if alive(hostPID) {
			syscall.Kill(hostPID, syscall.SIGKILL)
		}
	})

	b := startServe(t, bin, state, workspace)
	if !b.exited(15 * time.Second) {
		t.Fatal("a second serve kept running while the first held the state")
	}
	if b.exitCode != 1 || !strings.Contains(b.errOut.String(), "in use by another golem") {
		t.Fatalf("second serve exit %d\nstderr:\n%s", b.exitCode, b.errOut)
	}

	attach := exec.Command(bin, "attach", "--state", state)
	attach.Env = fauxEnv()
	attach.Stdin = strings.NewReader("/detach\n")
	var attachOut, attachErr bytes.Buffer
	attach.Stdout, attach.Stderr = &attachOut, &attachErr
	if err := attach.Run(); err != nil {
		t.Fatalf("attach: %v\nstdout:\n%s\nstderr:\n%s", err, attachOut.String(), attachErr.String())
	}
	if a.exited(500 * time.Millisecond) {
		t.Fatalf("serve exited after detach with %d\nstderr:\n%s", a.exitCode, a.errOut)
	}

	if err := a.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	c := startServe(t, bin, state, workspace)
	if !a.exited(5 * time.Second) {
		t.Fatal("killed serve did not exit")
	}
	c.waitOut(t, "service ready", 30*time.Second)
	// C holds the lock, so A's host, which shared it, has exited; it may
	// briefly remain an unreaped zombie, which kill(pid, 0) still finds.
	gone := time.Now().Add(2 * time.Second)
	for alive(hostPID) && time.Now().Before(gone) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(hostPID) {
		t.Fatalf("replacement became ready while the old host %d was alive", hostPID)
	}

	if got := socketInput(t, state, "Do the independent task", "other"); got != "router" {
		t.Fatalf("independent task admitted as %q", got)
	}
	c.waitOut(t, "Router [other]: independent result", 30*time.Second)
	if got := socketInput(t, state, "change plan", "main"); got != "subscribed" {
		t.Fatalf("followup admitted as %q", got)
	}
	c.waitOut(t, "Router [main]: updated result", 30*time.Second)

	st := socketStatus(t, state)
	i := slices.IndexFunc(st.Commands, func(cmd demoCommand) bool { return cmd.ID == original })
	if i < 0 {
		t.Fatalf("original command %s is missing from status: %+v", original, st.Commands)
	}
	if s := st.Commands[i].Status; s != "interrupted" && s != "cancelled" {
		t.Fatalf("original command is %s", s)
	}

	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if !c.exited(20 * time.Second) {
		t.Fatal("replacement did not stop after SIGTERM")
	}
	if c.exitCode != 0 {
		t.Fatalf("replacement exit %d\nstderr:\n%s", c.exitCode, c.errOut)
	}
}
