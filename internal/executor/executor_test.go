package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func localForTest(t *testing.T) *Local {
	t.Helper()
	root := t.TempDir()
	l, err := Open(root, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}
func awaitJob(t *testing.T, b Backend, id string) Job {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		j, err := b.Poll(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Finished() {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return Job{}
}

func TestAsyncCancellationAndEnvironment(t *testing.T) {
	l := localForTest(t)
	ctx := context.Background()
	t.Setenv("GOLEM_API_KEY", "never-pass-this-value")
	start := time.Now()
	j, err := l.Start(ctx, Spec{ID: "long", Command: "printf '%s' \"${GOLEM_API_KEY-unset}\"; sleep 30; touch should-not-exist", TimeoutSeconds: 40})
	if err != nil || j.Status != "running" {
		t.Fatalf("start: %+v %v", j, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("start blocked on command")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		out, _ := l.Output(ctx, "long", 0, 100)
		if out.Output == "unset" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := l.Stop(ctx, "long"); err != nil {
		t.Fatal(err)
	}
	if got := awaitJob(t, l, "long"); got.Status != "cancelled" {
		t.Fatal(got)
	}
	out, err := l.Output(ctx, "long", 0, 100)
	if err != nil || out.Output != "unset" {
		t.Fatalf("environment leaked or missing output: %q %v", out.Output, err)
	}
	if _, err := os.Stat(filepath.Join(l.root, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("cancelled command continued")
	}
}
func TestIdempotentStartAndCancellationBeforeStart(t *testing.T) {
	l := localForTest(t)
	ctx := context.Background()
	spec := Spec{ID: "once", Command: "printf x >> effects", TimeoutSeconds: 5}
	if _, err := l.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	awaitJob(t, l, "once")
	if _, err := l.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(l.root, "effects"))
	if string(b) != "x" {
		t.Fatalf("replayed command: %q", b)
	}
	if err := l.Stop(ctx, "delayed"); err != nil {
		t.Fatal(err)
	}
	j, err := l.Start(ctx, Spec{ID: "delayed", Command: "touch wrong", TimeoutSeconds: 5})
	if err != nil || j.Status != "cancelled" {
		t.Fatalf("late start survived cancellation: %+v %v", j, err)
	}
	if _, err := os.Stat(filepath.Join(l.root, "wrong")); !os.IsNotExist(err) {
		t.Fatal("late command ran")
	}
}
func TestRestartNeverReplaysUncertainCommand(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(filepath.Join(state, "uncertain.json"), []byte(`{"spec":{"id":"uncertain","command":"touch wrong","timeout_seconds":5},"status":"running"}`)); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir, state)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	j, err := l.Start(context.Background(), Spec{ID: "uncertain", Command: "touch wrong", TimeoutSeconds: 5})
	if err != nil || j.Status != "interrupted" {
		t.Fatalf("uncertain command replayed: %+v %v", j, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wrong")); !os.IsNotExist(err) {
		t.Fatal("replayed uncertain effects")
	}
	if _, err := Open(dir, state); err == nil {
		t.Fatal("allowed two endpoints to own one directory")
	}
}

func TestRemoteAuthenticationAndLostStartResponse(t *testing.T) {
	l := localForTest(t)
	handler := Handler(l, "test-token")
	spec := Spec{ID: "remote", Command: "printf x >> effects", TimeoutSeconds: 5}
	body, _ := json.Marshal(spec)
	// Discard the first acknowledgement after execution, then repeat the same request.
	for range 2 {
		req := httptest.NewRequest("PUT", "/jobs/remote", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("start: %d", w.Code)
		}
	}
	awaitJob(t, l, "remote")
	b, _ := os.ReadFile(filepath.Join(l.root, "effects"))
	if string(b) != "x" {
		t.Fatalf("remote retry duplicated effects: %q", b)
	}
	unauth := httptest.NewRecorder()
	handler.ServeHTTP(unauth, httptest.NewRequest("GET", "/jobs/remote", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated read succeeded")
	}
	req := httptest.NewRequest("GET", "/jobs/missing", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	data, _ := io.ReadAll(w.Result().Body)
	if strings.Contains(string(data), l.root) || strings.Contains(string(data), "unknown command") {
		t.Fatal("internal error leaked")
	}
}

func TestOutputReportsBytesConsumed(t *testing.T) {
	l := localForTest(t)
	ctx := context.Background()
	if _, err := l.Start(ctx, Spec{ID: "bytes", Command: "printf 'éé'", TimeoutSeconds: 5}); err != nil {
		t.Fatal(err)
	}
	awaitJob(t, l, "bytes")
	chunk, err := l.Output(ctx, "bytes", 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Bytes != 3 || chunk.Output != "é\xc3" {
		t.Fatalf("chunk: %+v", chunk)
	}
	rest, err := l.Output(ctx, "bytes", int64(chunk.Bytes), 100)
	if err != nil || rest.Bytes != 1 {
		t.Fatalf("rest: %+v %v", rest, err)
	}
}

func TestSweepRemovesOldLogsAndKeepsRecords(t *testing.T) {
	l := localForTest(t)
	ctx := context.Background()
	for _, id := range []string{"old", "fresh", "legacy"} {
		if _, err := l.Start(ctx, Spec{ID: id, Command: "echo " + id, TimeoutSeconds: 5}); err != nil {
			t.Fatal(err)
		}
		awaitJob(t, l, id)
	}
	if _, err := l.Start(ctx, Spec{ID: "busy", Command: "sleep 30", TimeoutSeconds: 40}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	l.mu.Lock()
	for id, age := range map[string]time.Duration{"old": 15 * 24 * time.Hour, "fresh": 24 * time.Hour} {
		j := l.jobs[id]
		j.EndedAt = now.Add(-age).UnixMilli()
		l.jobs[id] = j
		if err := l.save(j); err != nil {
			t.Fatal(err)
		}
	}
	j := l.jobs["legacy"]
	j.EndedAt = 0
	l.jobs["legacy"] = j
	if err := l.save(j); err != nil {
		t.Fatal(err)
	}
	l.mu.Unlock()
	past := now.Add(-15 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(l.state, "legacy.log"), past, past); err != nil {
		t.Fatal(err)
	}
	n, err := l.Sweep(now, 14*24*time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	for _, id := range []string{"old", "legacy"} {
		if _, err := os.Stat(filepath.Join(l.state, id+".log")); !os.IsNotExist(err) {
			t.Fatalf("%s log kept", id)
		}
		if _, err := os.Stat(filepath.Join(l.state, id+".json")); err != nil {
			t.Fatalf("%s record: %v", id, err)
		}
		if j, err := l.Poll(ctx, id); err != nil || !j.Finished() {
			t.Fatalf("poll %s: %+v %v", id, j, err)
		}
		if _, err := l.Output(ctx, id, 0, 10); !errors.Is(err, ErrOutputExpired) {
			t.Fatalf("output %s: %v", id, err)
		}
	}
	for _, id := range []string{"fresh", "busy"} {
		if _, err := os.Stat(filepath.Join(l.state, id+".log")); err != nil {
			t.Fatalf("%s log removed", id)
		}
	}
	if _, err := l.Output(ctx, "nope", 0, 10); err == nil || errors.Is(err, ErrOutputExpired) {
		t.Fatalf("unknown job: %v", err)
	}
	if err := l.Stop(ctx, "busy"); err != nil {
		t.Fatal(err)
	}
	if j, _ := l.Poll(ctx, "busy"); j.EndedAt == 0 {
		t.Fatal("stopped job has no EndedAt")
	}
}

func TestSummaryCountsByStatus(t *testing.T) {
	l := localForTest(t)
	ctx := context.Background()
	_, _ = l.Start(ctx, Spec{ID: "a", Command: "true", TimeoutSeconds: 5})
	_, _ = l.Start(ctx, Spec{ID: "b", Command: "false", TimeoutSeconds: 5})
	awaitJob(t, l, "a")
	awaitJob(t, l, "b")
	got, err := Summary(l.state)
	if err != nil || got["done"] != 1 || got["failed"] != 1 || len(got) != 2 {
		t.Fatalf("summary: %v %v", got, err)
	}
}

// TestCrashHelper is the endpoint process that TestOpenKillsCommandsOfACrashedEndpoint
// kills: it starts a long command and then waits to be killed.
func TestCrashHelper(t *testing.T) {
	dir := os.Getenv("GOLEM_EXECUTOR_CRASH_HELPER")
	if dir == "" {
		t.Skip("helper process only")
	}
	l, err := Open(dir, filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start(context.Background(), Spec{ID: "long", Command: "sleep 60", TimeoutSeconds: 600}); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("started\n")
	select {}
}

// A crash of the endpoint leaves its commands running; the next Open kills
// their process groups before marking them interrupted.
func TestOpenKillsCommandsOfACrashedEndpoint(t *testing.T) {
	dir := t.TempDir()
	helper := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
	helper.Env = append(os.Environ(), "GOLEM_EXECUTOR_CRASH_HELPER="+dir)
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "started\n" {
		helper.Process.Kill()
		t.Fatalf("helper: %q %v", line, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "state", "long.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Pgid      int   `json:"pgid"`
		StartedAt int64 `json:"started_at"`
	}
	if err := json.Unmarshal(b, &rec); err != nil || rec.Pgid <= 1 || rec.StartedAt == 0 {
		helper.Process.Kill()
		t.Fatalf("record has no process group: %s %v", b, err)
	}
	defer syscall.Kill(-rec.Pgid, syscall.SIGKILL)
	helper.Process.Kill()
	helper.Wait()
	if err := syscall.Kill(-rec.Pgid, 0); err != nil {
		t.Fatalf("command did not outlive its endpoint: %v", err)
	}

	l, err := Open(dir, filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	j, err := l.Poll(context.Background(), "long")
	if err != nil || j.Status != "interrupted" || !strings.Contains(j.Note, "Inspect the workspace") {
		t.Fatalf("%+v %v", j, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(-rec.Pgid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatal("process group still running after Open")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A record from before the current boot, or when the boot time is unknown,
// cannot be trusted to name a live orphan: its group is left alone.
func TestOpenSparesGroupsNotFromThisBoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		boot func() (time.Time, error)
	}{
		{"started before boot", func() (time.Time, error) { return time.Now(), nil }},
		{"boot time unknown", func() (time.Time, error) { return time.Time{}, errors.New("no boot time") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := bootTime
			bootTime = tc.boot
			defer func() { bootTime = old }()
			bystander := exec.Command("sleep", "60")
			bystander.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := bystander.Start(); err != nil {
				t.Fatal(err)
			}
			pgid := bystander.Process.Pid
			defer func() { syscall.Kill(-pgid, syscall.SIGKILL); bystander.Wait() }()
			dir := t.TempDir()
			state := filepath.Join(dir, "state")
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			rec := fmt.Sprintf(`{"spec":{"id":"old","command":"sleep 60","timeout_seconds":600},"status":"running","exit_code":-1,"pgid":%d,"started_at":%d}`,
				pgid, time.Now().Add(-time.Hour).UnixMilli())
			if err := AtomicWrite(filepath.Join(state, "old.json"), []byte(rec)); err != nil {
				t.Fatal(err)
			}
			l, err := Open(dir, state)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			if j, _ := l.Poll(context.Background(), "old"); j.Status != "interrupted" {
				t.Fatalf("%+v", j)
			}
			if err := syscall.Kill(-pgid, 0); err != nil {
				t.Fatalf("an unrelated group was killed: %v", err)
			}
		})
	}
}
