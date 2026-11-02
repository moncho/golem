// Package watch draws the running service as an animated hierarchy in a
// terminal: the Router, its Workers with their commands, and the Watchdog. It
// only reads the controller socket; nothing here changes Golem's state.
package watch

import (
	"encoding/json"
	"slices"
	"time"

	"golem/internal/console"
)

// Task is one Worker as the status call reports it.
type Task struct {
	ID, Title, Status, Outcome, NextActor, NextAction, Summary string
	Threads                                                    []string
	// Environment is where its commands run; empty or "local" is the default.
	Environment string
	// EndedAt and WaitingAt are zero when unknown or not applicable.
	EndedAt, WaitingAt time.Time
	// Cost is the conversation's spend in dollars so far, 0 when unknown.
	Cost float64
}

// Command is one command of a Worker. Line and CreatedAt come from inspect
// and are empty for tasks that are no longer active.
type Command struct {
	ID, Work, Status, Line string
	CreatedAt              time.Time
	ExitCode               int
	Finished               bool
}

// Snapshot is what the watcher knows about the service at one moment.
type Snapshot struct {
	Tasks                                []Task
	Commands                             []Command
	Threads                              []string
	QueuedReplies, FailedReplies, Timers int
}

type statusReply struct {
	Tasks []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Outcome     string   `json:"outcome"`
		NextActor   string   `json:"nextActor"`
		NextAction  string   `json:"nextAction"`
		Summary     string   `json:"summary"`
		EndedAt     int64    `json:"endedAt"`
		WaitingAt   int64    `json:"waitingAt"`
		Environment string   `json:"environment"`
		Cost        float64  `json:"cost"`
		Threads     []string `json:"threads"`
	} `json:"tasks"`
	Commands []struct {
		ID     string `json:"id"`
		Work   string `json:"work"`
		Status string `json:"status"`
	} `json:"commands"`
	QueuedReplies int               `json:"queuedReplies"`
	FailedReplies []json.RawMessage `json:"failedReplies"`
	Timers        []json.RawMessage `json:"timers"`
}

type inspectReply struct {
	Commands []struct {
		ID     string `json:"id"`
		Work   string `json:"work"`
		Status string `json:"status"`
		Spec   struct {
			Command string `json:"command"`
		} `json:"spec"`
		Result *struct {
			ExitCode int `json:"exit_code"`
		} `json:"result"`
		CreatedAt int64 `json:"createdAt"`
	} `json:"commands"`
}

func finished(status string) bool {
	return status == "done" || status == "failed" || status == "cancelled" || status == "interrupted"
}

func active(status string) bool { return status == "active" || status == "waiting" }

// Fetch reads the service through its socket: one status call, then one
// inspect per active task for the command lines.
func Fetch(state string) (Snapshot, error) {
	raw, err := console.Call(state, console.Request{Op: "status"})
	if err != nil {
		return Snapshot{}, err
	}
	var status statusReply
	if err := json.Unmarshal(raw, &status); err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{QueuedReplies: status.QueuedReplies, FailedReplies: len(status.FailedReplies), Timers: len(status.Timers)}
	threads := map[string]bool{}
	for _, t := range status.Tasks {
		snap.Tasks = append(snap.Tasks, Task{ID: t.ID, Title: t.Title, Status: t.Status, Outcome: t.Outcome, NextActor: t.NextActor, NextAction: t.NextAction, Summary: t.Summary, Threads: t.Threads, EndedAt: ended(t.EndedAt), WaitingAt: ended(t.WaitingAt), Environment: t.Environment, Cost: t.Cost})
		for _, thread := range t.Threads {
			threads[thread] = true
		}
	}
	commands := map[string]Command{}
	for _, c := range status.Commands {
		commands[c.ID] = Command{ID: c.ID, Work: c.Work, Status: c.Status, Finished: finished(c.Status)}
	}
	for _, t := range status.Tasks {
		if !active(t.Status) {
			continue
		}
		raw, err := console.Call(state, console.Request{Op: "inspect", Work: t.ID})
		if err != nil {
			continue // the task may have finished between the two calls
		}
		var inspect inspectReply
		if json.Unmarshal(raw, &inspect) != nil {
			continue
		}
		for _, c := range inspect.Commands {
			cmd := Command{ID: c.ID, Work: c.Work, Status: c.Status, Line: c.Spec.Command, CreatedAt: time.UnixMilli(c.CreatedAt), Finished: finished(c.Status)}
			if c.Result != nil {
				cmd.ExitCode = c.Result.ExitCode
			}
			commands[c.ID] = cmd
		}
	}
	for _, c := range commands {
		snap.Commands = append(snap.Commands, c)
	}
	slices.SortFunc(snap.Commands, func(a, b Command) int { return a.CreatedAt.Compare(b.CreatedAt) })
	for thread := range threads {
		snap.Threads = append(snap.Threads, thread)
	}
	slices.Sort(snap.Threads)
	return snap, nil
}

func ended(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
