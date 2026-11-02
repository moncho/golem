package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
)

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var opts serviceOptions
	flags.StringVar(&opts.State, "state", ".golem-pi", "Private state directory")
	flags.StringVar(&opts.Workspace, "workspace", ".", "Execution root for local commands")
	flags.StringVar(&opts.EnvFile, "env-file", ".env", "Configuration file")
	flags.BoolVar(&opts.Slack, "slack", false, "Receive and reply on Slack")
	flags.BoolVar(&opts.Herma, "herma", false, "Use Herma principles and memory (GOLEM_HERMA_URL enables it by itself)")
	flags.StringVar(&opts.Proactive, "proactive", "off", "Proactive policy: off, observe or act")
	flags.DurationVar(&opts.Retain, "retain", 336*time.Hour, "How long finished command output is kept")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	logger := newLogger(stderr)
	out := &lockedWriter{w: stdout}
	s, err := startService(ctx, opts, out, logger)
	if err != nil {
		fmt.Fprintln(stderr, "golem serve:", err)
		return 1
	}
	fmt.Fprintln(out, "Golem service ready. Use golem attach with the same --state directory.")
	err = s.Wait()
	if cerr := s.Close(); cerr != nil {
		logger.Warn("service closed with an error", "err", cerr)
	}
	var exited errHostExited
	if errors.As(err, &exited) {
		logger.Error("host exited", "code", exited.code)
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "golem serve:", err)
		return 1
	}
	return 0
}
