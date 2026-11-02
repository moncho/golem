package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"golem/internal/host"
)

// runDemo drives the scripted scenario against a real service: Pi recovery
// across a host restart, concurrent work, a direct followup and command
// cancellation. It needs no network or API key.
func runDemo(ctx context.Context, stdout, stderr io.Writer) int {
	if err := demo(ctx, &lockedWriter{w: stdout}, stderr); err != nil {
		fmt.Fprintln(stderr, "Demo failed:", err)
		return 1
	}
	return 0
}

type demoCommand struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type demoStatus struct {
	Tasks    []struct{ Status string } `json:"tasks"`
	Commands []demoCommand             `json:"commands"`
}

func demo(ctx context.Context, stdout, stderr io.Writer) error {
	dir, err := os.MkdirTemp("", "golem-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	workspace := filepath.Join(dir, "ws")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		return err
	}
	var mu sync.Mutex
	var sent []string
	send := func(_ host.Thread, text string) error {
		mu.Lock()
		sent = append(sent, text)
		mu.Unlock()
		fmt.Fprintf(stdout, "Router: %s\n", text)
		return nil
	}
	delivered := func(text string) func() (bool, error) {
		return func() (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return slices.Contains(sent, text), nil
		}
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	fmt.Fprintln(stdout, "Demo: scripted model decisions, real Pi Durable and local commands; no network or API key.")
	s, err := startService(ctx, serviceOptions{
		State: filepath.Join(dir, "st"), Workspace: workspace,
		Values:    map[string]string{"GOLEM_PROVIDER": "faux", "GOLEM_MODEL": "faux-1"},
		Proactive: "off", Retain: time.Hour, Send: send,
	}, stdout, logger)
	if err != nil {
		return err
	}
	defer s.Close()

	input := func(key, text, thread string) host.Input {
		return host.Input{Key: key, Text: text, Thread: host.Thread{ID: "console:" + thread, Source: "console"}, Sender: "owner", Explicit: true}
	}
	status := func() (demoStatus, error) {
		var st demoStatus
		raw, err := s.Status(ctx)
		if err == nil {
			err = json.Unmarshal(raw, &st)
		}
		return st, err
	}
	jobRunning := func(id string) error {
		job, err := s.local.Poll(ctx, id)
		if err != nil {
			return err
		}
		if job.Status != "running" {
			return fmt.Errorf("the original command is %s, not running", job.Status)
		}
		return nil
	}

	if _, err := s.Admit(ctx, input("initial", "Start the long task", "main")); err != nil {
		return err
	}
	var original string
	if err := until(ctx, "a command is running", func() (bool, error) {
		st, err := status()
		for _, c := range st.Commands {
			if c.Status == "running" {
				original = c.ID
				return true, nil
			}
		}
		return false, err
	}); err != nil {
		return err
	}
	if err := s.RestartHost(ctx); err != nil {
		return err
	}
	if err := jobRunning(original); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Demo: reopened Pi storage while the execution endpoint kept the command running.")
	if _, err := s.Admit(ctx, input("independent", "Do the independent task", "other")); err != nil {
		return err
	}
	if err := until(ctx, "the Router delivers the independent result", delivered("independent result")); err != nil {
		return err
	}
	if err := jobRunning(original); err != nil {
		return err
	}
	if got, err := s.Admit(ctx, input("followup", "change plan", "main")); err != nil || got != "subscribed" {
		return fmt.Errorf("the followup was admitted as %q (%v), not subscribed", got, err)
	}
	fmt.Fprintln(stdout, "Demo: sent 'change plan' directly to the subscribed Worker while its old command was running.")
	if err := until(ctx, "the Router delivers the updated result", delivered("updated result")); err != nil {
		return err
	}
	st, err := status()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(st.Commands, func(c demoCommand) bool { return c.ID == original })
	if i < 0 || st.Commands[i].Status != "cancelled" {
		return errors.New("the original command was not cancelled")
	}
	if len(st.Commands) != 3 {
		return fmt.Errorf("%d commands ran, not 3", len(st.Commands))
	}
	done := 0
	for _, task := range st.Tasks {
		if task.Status == "done" {
			done++
		}
	}
	if done != 2 {
		return fmt.Errorf("%d tasks are done, not 2", done)
	}
	if got, err := s.Admit(ctx, input("followup", "change plan", "main")); err != nil || got != "duplicate" {
		return fmt.Errorf("the repeated followup was admitted as %q (%v), not duplicate", got, err)
	}
	fmt.Fprintln(stdout, "PASS: Pi resumed stored work; an independent task finished; the followup stopped the old command; the Router delivered verified replacement output.")
	return nil
}

// until polls every 25 ms for up to 10 s.
func until(ctx context.Context, condition string, done func() (bool, error)) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		ok, err := done()
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("%s did not happen: %w", condition, err)
			}
			return fmt.Errorf("%s did not happen within 10s", condition)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
