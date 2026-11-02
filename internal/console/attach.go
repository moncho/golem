package console

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"golem/internal/host"
)

var resumeLine = regexp.MustCompile(`^/resume\s+(\S+)\s+([\s\S]+)$`)

type page struct {
	Items []struct {
		ID   int64  `json:"id"`
		Role string `json:"role"`
		Text string `json:"text"`
	} `json:"items"`
	Before *int64 `json:"before"`
	After  *int64 `json:"after"`
}

type attached struct {
	state       string
	out, errOut io.Writer
	wmu         sync.Mutex

	mu     sync.Mutex // guards the fields below
	thread string
	last   int64 // the after cursor: the newest entry scanned
	primed bool  // last is a real cursor, so polls are ascending
	before int64
}

func (a *attached) printf(w io.Writer, format string, args ...any) {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	fmt.Fprintf(w, format, args...)
}

// history fetches the selected thread. The first fetch of a thread reads the
// newest page and sets both cursors; later fetches ask only for entries after
// the cursor, and older pages walk back with the stored before cursor.
func (a *attached) history(older bool) error {
	a.mu.Lock()
	selected, before, last, primed := a.thread, a.before, a.last, a.primed
	a.mu.Unlock()
	if older && before == 0 {
		a.printf(a.out, "No earlier history.\n")
		return nil
	}
	req := Request{Op: "history", Thread: selected}
	if older {
		req.Before = before
	} else if primed {
		req.After = &last
	}
	raw, err := Call(a.state, req)
	if err != nil {
		return err
	}
	var p page
	if err := json.Unmarshal(raw, &p); err != nil {
		return errors.New("Invalid controller response.")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.thread != selected {
		return nil
	}
	if req.After == nil {
		a.before = 0
		if p.Before != nil {
			a.before = *p.Before
		}
	}
	newest := a.last
	for _, it := range p.Items {
		if older || it.ID > a.last {
			a.printf(a.out, "[%s] %s: %s\n", selected, it.Role, it.Text)
		}
		newest = max(newest, it.ID)
	}
	if !older {
		if p.After != nil {
			newest = max(newest, *p.After)
		}
		a.last, a.primed = newest, true
	}
	return nil
}

func (a *attached) show(req Request) error {
	raw, err := Call(a.state, req)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		buf.Reset()
		buf.Write(raw)
	}
	a.printf(a.out, "%s\n", buf.String())
	return nil
}

func (a *attached) report(err error) {
	var he *host.Error
	if errors.As(err, &he) {
		a.printf(a.errOut, "%s\n", he.Message)
		return
	}
	a.printf(a.errOut, "%s\n", err.Error())
}

// Attach is the console client. Leaving it never stops the controller.
func Attach(ctx context.Context, state string, in io.Reader, out, errOut io.Writer, poll time.Duration) error {
	a := &attached{state: state, out: out, errOut: errOut, thread: "main"}
	if _, err := Call(state, Request{Op: "status"}); err != nil {
		return err
	}
	if err := a.history(false); err != nil {
		return err
	}
	a.printf(out, "Attached. /tasks QUERY, /inspect ID, /resume ID instruction, /thread NAME, /history, /status, /retry. /detach or /quit leaves work running.\n")

	pctx, stop := context.WithCancel(ctx)
	var pollDone sync.WaitGroup
	pollDone.Add(1)
	go func() {
		defer pollDone.Done()
		t := time.NewTicker(poll)
		defer t.Stop()
		disconnected := false
		for {
			select {
			case <-pctx.Done():
				return
			case <-t.C:
			}
			if err := a.history(false); err != nil {
				if !disconnected {
					a.printf(errOut, "Controller disconnected; waiting to reconnect.\n")
				}
				disconnected = true
			} else {
				if disconnected {
					a.printf(out, "Controller reconnected.\n")
				}
				disconnected = false
			}
		}
	}()
	defer func() { stop(); pollDone.Wait() }()

	lines := make(chan string)
	go func() {
		defer close(lines)
		r := bufio.NewReader(in)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				select {
				case lines <- strings.TrimRight(line, "\r\n"):
				case <-pctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	var pending *Request
	for {
		var line string
		var ok bool
		select {
		case <-ctx.Done():
			return nil
		case line, ok = <-lines:
			if !ok {
				return nil
			}
		}
		if t := strings.TrimSpace(line); t == "/quit" || t == "/detach" {
			return nil
		}
		if err := a.dispatch(line, &pending); err != nil {
			a.report(err)
		}
	}
}

func (a *attached) dispatch(line string, pending **Request) error {
	switch {
	case strings.HasPrefix(line, "/thread "):
		name := strings.TrimSpace(line[len("/thread "):])
		if name == "" {
			name = "main"
		}
		a.mu.Lock()
		a.thread, a.last, a.primed, a.before = name, 0, false, 0
		a.mu.Unlock()
		return a.history(false)
	case line == "/history":
		return a.history(true)
	case line == "/status":
		return a.show(Request{Op: "status"})
	case line == "/tasks" || strings.HasPrefix(line, "/tasks "):
		text := ""
		if len(line) > 7 {
			text = strings.TrimSpace(line[7:])
		}
		return a.show(Request{Op: "search", Text: text})
	case strings.HasPrefix(line, "/inspect "):
		return a.show(Request{Op: "inspect", Work: strings.TrimSpace(line[len("/inspect "):])})
	case line == "/retry":
		if *pending == nil {
			return nil
		}
		return a.send(pending)
	case strings.TrimSpace(line) == "":
		return nil
	}
	if *pending != nil {
		a.printf(a.errOut, "An input has an uncertain acknowledgement. Use /retry before sending another.\n")
		return nil
	}
	a.mu.Lock()
	thread := a.thread
	a.mu.Unlock()
	if strings.HasPrefix(line, "/resume ") {
		m := resumeLine.FindStringSubmatch(line)
		if m == nil {
			a.printf(a.errOut, "Use /resume ID instruction.\n")
			return nil
		}
		*pending = &Request{Op: "resume", Key: newKey(), Work: m[1], Text: m[2], Thread: thread}
	} else {
		*pending = &Request{Op: "input", Key: newKey(), Text: line, Thread: thread}
	}
	return a.send(pending)
}

// send keeps the request pending unless the controller answered definitively
// (success, or a rejection that a retry would repeat).
func (a *attached) send(pending **Request) error {
	_, err := Call(a.state, **pending)
	var he *host.Error
	if err == nil || errors.As(err, &he) {
		*pending = nil
	}
	return err
}
