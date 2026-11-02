package console

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golem/internal/host"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAttachDetachLeavesServerRunning(t *testing.T) {
	b := &fakeBackend{}
	state, _ := start(t, b)
	var out, errOut syncBuffer
	if err := Attach(context.Background(), state, strings.NewReader("/detach\n"), &out, &errOut, time.Hour); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Attached. /tasks QUERY, /inspect ID, /resume ID instruction, /thread NAME, /history, /status, /retry. /detach or /quit leaves work running.") {
		t.Fatalf("banner missing: %q", out.String())
	}
	if _, err := Call(state, Request{Op: "status"}); err != nil {
		t.Fatalf("server stopped: %v", err)
	}
}

// attachUntil runs Attach until out contains want, then detaches.
func attachUntil(t *testing.T, b *fakeBackend, want string) string {
	t.Helper()
	state, _ := start(t, b)
	pr, pw := io.Pipe()
	var out, errOut syncBuffer
	done := make(chan error, 1)
	go func() { done <- Attach(context.Background(), state, pr, &out, &errOut, 10*time.Millisecond) }()
	waitFor(t, want, func() bool { return strings.Contains(out.String(), want) })
	time.Sleep(60 * time.Millisecond)
	pw.Write([]byte("/quit\n"))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// TestAttachPollsWithAfter: the poll continues from the cursor the host
// returns, not from the last item shown, so entries of other threads are not
// scanned again and items past them are reached.
func TestAttachPollsWithAfter(t *testing.T) {
	b := &fakeBackend{}
	b.history = func(thread string, before int64, after *int64) json.RawMessage {
		switch {
		case after == nil:
			return json.RawMessage(`{"items":[{"id":1,"role":"user","text":"one","at":1}],"before":null,"after":1}`)
		case *after == 1:
			return json.RawMessage(`{"items":[{"id":2,"role":"router","text":"two","at":2}],"after":50}`)
		case *after == 50:
			return json.RawMessage(`{"items":[{"id":60,"role":"router","text":"three","at":3}],"after":60}`)
		}
		return json.RawMessage(`{"items":[],"after":` + strconv.FormatInt(*after, 10) + `}`)
	}
	out := attachUntil(t, b, "[main] router: three")
	if strings.Count(out, "two") != 1 || strings.Count(out, "three") != 1 || !strings.Contains(out, "[main] user: one") {
		t.Fatalf("%q", out)
	}
}

// TestAttachFollowsEmptyCursor: a window with no items of the thread still
// moves the cursor, so a reply after more than one window is shown.
func TestAttachFollowsEmptyCursor(t *testing.T) {
	b := &fakeBackend{}
	b.history = func(thread string, before int64, after *int64) json.RawMessage {
		switch {
		case after == nil:
			return json.RawMessage(`{"items":[],"before":null,"after":0}`)
		case *after == 205:
			return json.RawMessage(`{"items":[{"id":300,"role":"router","text":"late reply","at":3}],"after":300}`)
		case *after == 300:
			return json.RawMessage(`{"items":[],"after":300}`)
		}
		return json.RawMessage(`{"items":[],"after":205}`)
	}
	out := attachUntil(t, b, "[main] router: late reply")
	if strings.Count(out, "late reply") != 1 {
		t.Fatalf("%q", out)
	}
}

// TestAttachFirstPollOfEmptyThreadIsAscending: after the newest page, every
// poll is an ascending after call, even before the thread has any item.
func TestAttachFirstPollOfEmptyThreadIsAscending(t *testing.T) {
	b := &fakeBackend{}
	b.history = func(thread string, before int64, after *int64) json.RawMessage {
		if after == nil {
			return json.RawMessage(`{"items":[],"before":null,"after":0}`)
		}
		return json.RawMessage(`{"items":[],"after":0}`)
	}
	state, _ := start(t, b)
	pr, pw := io.Pipe()
	var out, errOut syncBuffer
	done := make(chan error, 1)
	go func() { done <- Attach(context.Background(), state, pr, &out, &errOut, 10*time.Millisecond) }()
	histories := func() []call {
		b.mu.Lock()
		defer b.mu.Unlock()
		var h []call
		for _, c := range b.calls {
			if c.Method == "history" {
				h = append(h, c)
			}
		}
		return h
	}
	waitFor(t, "two history calls", func() bool { return len(histories()) >= 2 })
	pw.Write([]byte("/quit\n"))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h := histories()
	if h[0].After != nil {
		t.Fatalf("first fetch is not the newest page: %+v", h[0])
	}
	if h[1].After == nil || *h[1].After != 0 || h[1].Before != 0 {
		t.Fatalf("first poll is not ascending from 0: %+v", h[1])
	}
}

func TestAttachPrintsRejectedMessage(t *testing.T) {
	rb := &resumeRejects{fakeBackend: &fakeBackend{}}
	state, _ := start(t, rb)
	var out, errOut syncBuffer
	if err := Attach(context.Background(), state, strings.NewReader("/resume w1 go\n/retry\nhello\n/detach\n"), &out, &errOut, time.Hour); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "Unknown task or thread.") || strings.Contains(errOut.String(), genericMessage) {
		t.Fatalf("errOut %q", errOut.String())
	}
	if strings.Contains(errOut.String(), "uncertain acknowledgement") {
		t.Fatalf("pending not cleared: %q", errOut.String())
	}
	rb.mu.Lock()
	defer rb.mu.Unlock()
	var resumes, admits int
	for _, c := range rb.calls {
		switch c.Method {
		case "resume":
			resumes++
		case "admit":
			admits++
		}
	}
	if resumes != 1 || admits != 1 {
		t.Fatalf("resumes %d admits %d", resumes, admits)
	}
}

type resumeRejects struct{ *fakeBackend }

func (r *resumeRejects) Resume(context.Context, string, string, string, string) (json.RawMessage, error) {
	r.record(call{Method: "resume"})
	return nil, &host.Error{Code: "rejected", Message: "Unknown task or thread."}
}

func TestAttachBlocksInputUntilRetry(t *testing.T) {
	fb := &flaky{fakeBackend: &fakeBackend{}}
	state, _ := start(t, fb)
	var out, errOut syncBuffer
	in := strings.NewReader("hello\nagain\n/retry\nagain\n/detach\n")
	if err := Attach(context.Background(), state, in, &out, &errOut, time.Hour); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "An input has an uncertain acknowledgement. Use /retry before sending another.") {
		t.Fatalf("errOut %q", errOut.String())
	}
	fb.mu.Lock()
	defer fb.mu.Unlock()
	var admits []call
	for _, c := range fb.calls {
		if c.Method == "admit" {
			admits = append(admits, c)
		}
	}
	if len(admits) != 3 { // hello (fails), retry (ok), again (ok)
		t.Fatalf("admits %d", len(admits))
	}
	if admits[0].Input.Key != admits[1].Input.Key || admits[0].Input.Text != "hello" || admits[1].Input.Text != "hello" {
		t.Fatalf("retry did not reuse the key: %+v %+v", admits[0].Input, admits[1].Input)
	}
	if admits[2].Input.Key == admits[0].Input.Key || admits[2].Input.Text != "again" {
		t.Fatalf("%+v", admits[2].Input)
	}
}

type flaky struct {
	*fakeBackend
	n int
}

func (f *flaky) Admit(ctx context.Context, in host.Input) (string, error) {
	f.mu.Lock()
	f.n++
	n := f.n
	f.mu.Unlock()
	if n == 1 {
		f.record(call{Method: "admit", Input: in})
		return "", io.ErrUnexpectedEOF
	}
	return f.fakeBackend.Admit(ctx, in)
}
