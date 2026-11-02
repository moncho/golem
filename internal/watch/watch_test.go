package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golem/internal/console"
	"golem/internal/host"
)

type fakeBackend struct {
	status   string
	inspects atomic.Int32
}

func (f *fakeBackend) Admit(context.Context, host.Input) (string, error) { return "router", nil }
func (f *fakeBackend) Status(context.Context) (json.RawMessage, error) {
	return json.RawMessage(f.status), nil
}
func (f *fakeBackend) Search(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`[]`), nil
}
func (f *fakeBackend) Inspect(_ context.Context, work string) (json.RawMessage, error) {
	f.inspects.Add(1)
	if work != "work_1" {
		return nil, &host.Error{Code: "rejected", Message: "Unknown task."}
	}
	return json.RawMessage(`{"work":{"id":"work_1"},"commands":[{"id":"job_1","work":"work_1","status":"running","spec":{"id":"job_1","command":"go test ./...","directory":".","timeout_seconds":600},"stop":false,"notified":false,"result":null,"createdAt":1700000000000}]}`), nil
}
func (f *fakeBackend) History(context.Context, string, int64, *int64) (json.RawMessage, error) {
	return json.RawMessage(`{"items":[]}`), nil
}
func (f *fakeBackend) Resume(context.Context, string, string, string, string) (json.RawMessage, error) {
	return nil, errors.New("unused")
}

func TestFetchReadsStatusAndInspectsActiveTasks(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "gw")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(state)
	b := &fakeBackend{status: `{"tasks":[{"id":"work_1","title":"Run tests","status":"active","environment":"local","threads":["console:main"],"outcome":"open"},{"id":"work_0","title":"Old","status":"done","environment":"local","threads":["slack:C1:1"],"outcome":"resolved"}],"commands":[{"id":"job_1","work":"work_1","status":"running","stopRequested":false},{"id":"job_0","work":"work_0","status":"done","stopRequested":false}],"queuedReplies":1,"failedReplies":[{"id":"d1"}],"timers":[{"id":"t1"},{"id":"t2"}],"failures":[]}`}
	srv, err := console.Serve(context.Background(), state, b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	snap, err := Fetch(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tasks) != 2 || snap.Tasks[0].Title != "Run tests" || snap.Tasks[1].Outcome != "resolved" {
		t.Fatalf("tasks: %+v", snap.Tasks)
	}
	if snap.QueuedReplies != 1 || snap.FailedReplies != 1 || snap.Timers != 2 {
		t.Fatalf("counts: %+v", snap)
	}
	var running *Command
	for i := range snap.Commands {
		if snap.Commands[i].ID == "job_1" {
			running = &snap.Commands[i]
		}
	}
	if running == nil || running.Line != "go test ./..." || running.Status != "running" || running.CreatedAt.Unix() != 1700000000 {
		t.Fatalf("running command: %+v", running)
	}
	if n := b.inspects.Load(); n != 1 {
		t.Fatalf("inspect called %d times, want 1 (active tasks only)", n)
	}
}

func snapshotRunning() Snapshot {
	return Snapshot{
		Tasks:    []Task{{ID: "work_1", Title: "Run tests", Status: "active", Outcome: "open", Threads: []string{"console:main"}}},
		Commands: []Command{{ID: "job_abcdef_1", Work: "work_1", Status: "running", Line: "go test ./internal/billing/...", CreatedAt: time.Unix(1000, 0)}},
	}
}

func joined(lines []string) string { return plain(strings.Join(lines, "\n")) }

func TestRenderShowsHierarchyAndRunningCommand(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	v.Update(snapshotRunning(), t0)
	lines := v.Render(t0, 100, 24)
	if len(lines) != 24 {
		t.Fatalf("got %d lines, want 24", len(lines))
	}
	text := joined(lines)
	for _, want := range []string{"ROUTER", "WATCHDOG", "Run tests", "go test ./internal/billing/...", "console:main", "00:42", "job_abcdef_1"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in frame:\n%s", want, text)
		}
	}
	for i, line := range lines {
		if w := width(plain(line)); w > 100 {
			t.Errorf("line %d is %d wide: %q", i, w, plain(line))
		}
	}
}

func TestRenderAnimatesBetweenTicks(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	v.Update(snapshotRunning(), t0)
	a := joined(v.Render(t0, 100, 24))
	b := joined(v.Render(t0.Add(100*time.Millisecond), 100, 24))
	if a == b {
		t.Fatal("two frames 100 ms apart are identical; nothing animates")
	}
}

func TestRenderPulsesANewTaskThenSettles(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	v.Update(Snapshot{}, t0.Add(-time.Second)) // the watcher was already running
	v.Update(snapshotRunning(), t0)
	if !strings.Contains(joined(v.Render(t0.Add(300*time.Millisecond), 100, 24)), pulseGlyph) {
		t.Fatal("no pulse travelling to the new worker")
	}
	if strings.Contains(joined(v.Render(t0.Add(5*time.Second), 100, 24)), pulseGlyph) {
		t.Fatal("pulse still shown five seconds later")
	}
}

func TestRenderFadesFinishedTasks(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	v.Update(snapshotRunning(), t0)
	done := snapshotRunning()
	done.Tasks[0].Status, done.Tasks[0].Outcome = "done", "resolved"
	done.Commands[0].Status, done.Commands[0].Finished, done.Commands[0].ExitCode = "done", true, 0
	t1 := t0.Add(2 * time.Second)
	v.Update(done, t1)
	if text := joined(v.Render(t1.Add(5*time.Second), 100, 24)); !strings.Contains(text, "Run tests") || !strings.Contains(text, "exit 0") {
		t.Fatalf("finished task should still show with its exit code for a while:\n%s", text)
	}
	v.Update(done, t1.Add(11*time.Second))
	if text := joined(v.Render(t1.Add(11*time.Second), 100, 24)); strings.Contains(text, "┌ work_1") || !strings.Contains(text, "recently finished") {
		t.Fatalf("finished task should have left the tree for the recent list:\n%s", text)
	}
}

func TestRenderShowsWhoHoldsTheBallWhenWaiting(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	v.Update(Snapshot{Tasks: []Task{{ID: "work_2", Title: "Fix add", Status: "waiting", Outcome: "needs_decision", NextActor: "user", NextAction: "Choose the scope", Threads: []string{"slack:C0EXAMPLE01:1794100238.413969"}}}}, t0)
	text := joined(v.Render(t0, 100, 24))
	for _, want := range []string{"needs_decision", "user", "Choose the scope", "slack:C0EXAMPLE01"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
}

func TestRenderIdleStaysAlive(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	v.Update(Snapshot{Threads: []string{"console:main"}}, t0)
	text := joined(v.Render(t0, 80, 20))
	if !strings.Contains(text, "ROUTER") || !strings.Contains(text, "nothing running") {
		t.Fatalf("idle frame:\n%s", text)
	}
}

func TestRenderFitsNarrowTerminals(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	s := snapshotRunning()
	s.Commands[0].Line = strings.Repeat("very-long-command ", 20)
	for i := 2; i <= 6; i++ {
		s.Tasks = append(s.Tasks, Task{ID: "work_" + string(rune('0'+i)), Title: "Task " + string(rune('0'+i)), Status: "active", Outcome: "open", Threads: []string{"console:main"}})
	}
	v.Update(s, t0)
	for _, size := range [][2]int{{60, 16}, {120, 30}, {40, 10}} {
		lines := v.Render(t0, size[0], size[1])
		if len(lines) != size[1] {
			t.Errorf("%v: %d lines", size, len(lines))
		}
		for i, line := range lines {
			if w := width(plain(line)); w > size[0] {
				t.Errorf("%v line %d is %d wide", size, i, w)
			}
		}
	}
}

func TestTasksAlreadyFinishedAtFirstSightAreNotShown(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	v.Update(Snapshot{Tasks: []Task{
		{ID: "work_old", Title: "Long ago", Status: "done", Outcome: "resolved", Summary: "It was fine."},
		{ID: "work_now", Title: "Current", Status: "active", Outcome: "open"},
	}}, t0)
	text := joined(v.Render(t0, 100, 24))
	if strings.Contains(text, "┌ work_old") || !strings.Contains(text, "┌ work_now  Current") {
		t.Fatalf("first frame should box only live work:\n%s", text)
	}
	later := Snapshot{Tasks: []Task{{ID: "work_now", Title: "Current", Status: "done", Outcome: "resolved", Summary: "Tests pass on the branch."}}}
	v.Update(later, t0.Add(time.Second))
	if text := joined(v.Render(t0.Add(2*time.Second), 100, 24)); !strings.Contains(text, "Tests pass on the branch.") {
		t.Fatalf("a task that finishes while watched shows its summary:\n%s", text)
	}
}

func TestRenderKeepsControlCharactersOutOfTheBox(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	s := snapshotRunning()
	s.Commands[0].Line = "printf 'started\n'; sleep 60;\tls"
	v.Update(s, t0)
	lines := v.Render(t0, 100, 24)
	if len(lines) != 24 {
		t.Fatalf("a newline in a command line broke the frame into %d lines", len(lines))
	}
	for _, line := range lines {
		if strings.ContainsAny(plain(line), "\n\t\r") {
			t.Fatalf("control character reached the frame: %q", plain(line))
		}
	}
	if !strings.Contains(joined(lines), "printf 'started⏎'; sleep 60; ls") {
		t.Fatalf("command line not shown with a visible newline mark:\n%s", joined(lines))
	}
}

func TestFirstFrameHasNoPulsesAndShowsRecentlyFinishedWork(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	v.Update(Snapshot{Tasks: []Task{
		{ID: "work_a", Title: "Already running", Status: "active", Outcome: "open"},
		{ID: "work_b", Title: "Just finished", Status: "done", Outcome: "resolved", Summary: "All good.", EndedAt: t0.Add(-3 * time.Second)},
		{ID: "work_c", Title: "Long ago", Status: "done", Outcome: "resolved", EndedAt: t0.Add(-time.Hour)},
	}}, t0)
	text := joined(v.Render(t0, 100, 30))
	if strings.Contains(text, pulseGlyph) || strings.Contains(text, "▲") {
		t.Fatalf("a first frame must not pulse for work it is seeing for the first time:\n%s", text)
	}
	if !strings.Contains(text, "┌ work_a  Already running") || !strings.Contains(text, "┌ work_b  Just finished") || strings.Contains(text, "┌ work_c") {
		t.Fatalf("first frame should box live work and work finished within the fade window, and list older work:\n%s", text)
	}
	v.Update(v.snap, t0.Add(8*time.Second))
	if text := joined(v.Render(t0.Add(8*time.Second), 100, 30)); strings.Contains(text, "┌ work_b") || !strings.Contains(text, "recently finished") {
		t.Fatalf("a task that ended 11 s ago should have left the tree for the recent list:\n%s", text)
	}
}

func TestPipelineChevronsAreEvenlySpaced(t *testing.T) {
	bar := []rune(plain(pipeline(27, 0)))
	var heads []int
	for i, r := range bar {
		if r == '▸' {
			heads = append(heads, i)
		}
	}
	if len(heads) < 3 {
		t.Fatalf("too few chevrons in %q", string(bar))
	}
	gap := heads[1] - heads[0]
	for i := 2; i < len(heads); i++ {
		if d := heads[i] - heads[i-1]; d != gap {
			t.Fatalf("uneven chevron spacing %v in %q", heads, string(bar))
		}
	}
}

func TestRouterFlashesWhenAWorkerReports(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	v.Update(snapshotRunning(), t0)
	done := snapshotRunning()
	done.Tasks[0].Status, done.Tasks[0].Outcome = "done", "resolved"
	t1 := t0.Add(3 * time.Second)
	v.Update(done, t1)
	if text := joined(v.Render(t1.Add(200*time.Millisecond), 100, 24)); !strings.Contains(text, "↤ report") {
		t.Fatalf("router should flash the incoming report:\n%s", text)
	}
	if text := joined(v.Render(t1.Add(4*time.Second), 100, 24)); strings.Contains(text, "↤ report") {
		t.Fatalf("report flash should settle:\n%s", text)
	}
}

func TestTitlesAndSummariesAreSanitised(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	v.Update(Snapshot{}, t0.Add(-time.Second))
	v.Update(Snapshot{Tasks: []Task{
		{ID: "work_t", Title: "Fix\nthe build", Status: "waiting", Outcome: "needs_decision", NextActor: "user", NextAction: "Pick\tone"},
	}}, t0)
	lines := v.Render(t0, 100, 24)
	if len(lines) != 24 {
		t.Fatalf("frame broke into %d lines", len(lines))
	}
	text := joined(lines)
	if !strings.Contains(text, "Fix⏎the build") || !strings.Contains(text, "Pick one") {
		t.Fatalf("title and action should be sanitised:\n%s", text)
	}
}

func TestBoxesGrowIntoWideTerminals(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	action := "Confirm that `Design Decision.md` is the artifact you meant before I summarise it"
	v.Update(Snapshot{Tasks: []Task{{ID: "work_370", Title: "Read the design note", Status: "waiting", Outcome: "waiting_reply", NextActor: "user", NextAction: action, Threads: []string{"slack:C0EXAMPLE01:1"}}}}, t0)
	text := joined(v.Render(t0, 190, 20))
	for _, part := range []string{"Confirm that `Design Decision.md` is the artifact", "summarise it"} {
		if !strings.Contains(text, part) {
			t.Fatalf("a lone box on a wide terminal should show the whole next action (missing %q):\n%s", part, text)
		}
	}
	if text := joined(v.Render(t0, 40, 20)); !strings.Contains(text, "…") {
		t.Fatalf("a narrower terminal still truncates:\n%s", text)
	}
	two := Snapshot{Tasks: []Task{
		{ID: "work_1", Title: "One", Status: "active", Outcome: "open"},
		{ID: "work_2", Title: "Two", Status: "active", Outcome: "open"},
	}}
	v2 := NewView(t0)
	v2.Update(two, t0)
	lines := v2.Render(t0, 190, 20)
	if text := joined(lines); !strings.Contains(text, "┬") {
		t.Fatalf("two boxes should share one row on a wide terminal:\n%s", text)
	}
	for i, line := range lines {
		if w := width(plain(line)); w > 190 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
}

func TestWaitingActionWrapsAcrossBothLines(t *testing.T) {
	t0 := time.Unix(1042, 0)
	v := NewView(t0)
	action := "Confirm that `Design Decision.md` in Downloads is the artifact you meant. If not, tell me which file to read instead and I will summarise that one."
	v.Update(Snapshot{Tasks: []Task{{ID: "work_370", Title: "Read artifact", Status: "waiting", Outcome: "waiting_reply", NextActor: "user", NextAction: action}}}, t0)
	text := joined(v.Render(t0, 190, 20))
	for _, want := range []string{"⧖ user: Confirm that", "tell me which file to read instead", "summarise that one."} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q; the action should wrap onto the second line:\n%s", want, text)
		}
	}
}

func TestWidthCountsWideRunesAsTwoCells(t *testing.T) {
	if width("⏳") != 2 || width("日本") != 4 || width("◉ ROUTER") != 8 || width("⣾═▸") != 3 {
		t.Fatalf("cell counting: ⏳=%d 日本=%d ◉ ROUTER=%d ⣾═▸=%d", width("⏳"), width("日本"), width("◉ ROUTER"), width("⣾═▸"))
	}
	if got := plain(fit("⏳x", 4)); width(got) != 4 {
		t.Fatalf("fit must pad by cells, got %q (%d cells)", got, width(got))
	}
	if got := plain(clip("日本語", 3)); got != "日" {
		t.Fatalf("clip must not split a wide rune: %q", got)
	}
}

func TestFramesUseOnlySingleCellGlyphs(t *testing.T) {
	t0 := time.Unix(1042, 0)
	snaps := []Snapshot{
		snapshotRunning(),
		{Tasks: []Task{{ID: "w", Title: "Waiting", Status: "waiting", Outcome: "needs_decision", NextActor: "user", NextAction: "Decide"}}},
		{Tasks: []Task{{ID: "d", Title: "Done", Status: "done", Outcome: "resolved", Summary: "ok", EndedAt: t0}}, QueuedReplies: 1, FailedReplies: 1, Timers: 2},
		{Threads: []string{"console:main"}},
	}
	for _, s := range snaps {
		v := NewView(t0)
		v.Update(Snapshot{}, t0.Add(-time.Second))
		v.Update(s, t0)
		for _, ms := range []int64{0, 100, 700, 1300} {
			for _, line := range v.Render(t0.Add(time.Duration(ms)*time.Millisecond), 120, 30) {
				for _, r := range plain(line) {
					if wide(r) {
						t.Fatalf("wide glyph %q (U+%04X) in frame line %q", string(r), r, plain(line))
					}
				}
			}
		}
	}
}

func TestIdleScreenListsRecentlyFinishedWork(t *testing.T) {
	t0 := time.Unix(1000000, 0)
	v := NewView(t0)
	v.Update(Snapshot{Threads: []string{"console:main"}, Tasks: []Task{
		{ID: "work_370", Title: "Read the design note", Status: "done", Outcome: "resolved", Threads: []string{"slack:C0EXAMPLE01:1"}, EndedAt: t0.Add(-3 * time.Minute)},
		{ID: "work_178", Title: "Review PR 1234", Status: "done", Outcome: "resolved", Threads: []string{"slack:C0EXAMPLE01:2"}, EndedAt: t0.Add(-26 * time.Hour)},
		{ID: "work_32", Title: "Inspect project", Status: "cancelled", Outcome: "open", Threads: []string{"console:main"}, EndedAt: t0.Add(-9 * 24 * time.Hour)},
	}}, t0)
	text := joined(v.Render(t0, 120, 24))
	for _, want := range []string{"nothing running", "recently finished", "work_370  Read the design note", "resolved · 3m ago · slack:C0EXAMPLE01", "work_178", "1d ago", "work_32", "cancelled", "9d ago"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	// The newest comes first.
	if strings.Index(text, "work_370") > strings.Index(text, "work_178") {
		t.Fatalf("recent work should be newest first:\n%s", text)
	}
}

func TestBusyScreenKeepsAShortRecentList(t *testing.T) {
	t0 := time.Unix(1000000, 0)
	v := NewView(t0)
	s := snapshotRunning()
	for i := range 5 {
		s.Tasks = append(s.Tasks, Task{ID: fmt.Sprintf("work_old%d", i), Title: "Old", Status: "done", Outcome: "resolved", EndedAt: t0.Add(-time.Duration(i+1) * time.Hour)})
	}
	v.Update(s, t0)
	text := joined(v.Render(t0, 120, 30))
	if !strings.Contains(text, "Run tests") || !strings.Contains(text, "recently finished") {
		t.Fatalf("busy frame should show the tree and a recent list:\n%s", text)
	}
	if strings.Count(text, "work_old") != 3 {
		t.Fatalf("a busy screen lists at most three recent tasks:\n%s", text)
	}
}

func TestWaitingBoxShowsHowLongAndRemoteEnvironment(t *testing.T) {
	t0 := time.Unix(1000000, 0)
	v := NewView(t0)
	v.Update(Snapshot{}, t0.Add(-time.Second))
	v.Update(Snapshot{Tasks: []Task{
		{ID: "ask-the-user-7", Title: "Ask the user", Status: "waiting", Outcome: "needs_decision", NextActor: "user", NextAction: "Pick a scope", WaitingAt: t0.Add(-14 * time.Minute), Threads: []string{"console:main"}, Environment: "remote"},
	}}, t0)
	text := joined(v.Render(t0, 120, 24))
	for _, want := range []string{"⧖ user · 14m: Pick a scope", "console:main · needs_decision · remote"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	v2 := NewView(t0)
	v2.Update(snapshotRunning(), t0)
	if text := joined(v2.Render(t0, 120, 24)); strings.Contains(text, "· local") {
		t.Fatalf("the default environment is not worth a tag:\n%s", text)
	}
}

func TestBoxShowsCostWhenKnown(t *testing.T) {
	t0 := time.Unix(1000000, 0)
	v := NewView(t0)
	s := snapshotRunning()
	s.Tasks[0].Cost = 0.1234
	v.Update(s, t0)
	if text := joined(v.Render(t0, 120, 24)); !strings.Contains(text, "console:main · open · $0.12") {
		t.Fatalf("cost should show on the status line:\n%s", text)
	}
}
