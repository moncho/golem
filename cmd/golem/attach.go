package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"golem/internal/console"
	"golem/internal/host"
)

// runAttach connects the console to a running service; leaving it never
// stops the service.
func runAttach(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("attach", flag.ContinueOnError)
	flags.SetOutput(stderr)
	state := flags.String("state", ".golem-pi", "State directory of the running service")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err := console.Attach(ctx, *state, stdin, stdout, stderr, time.Second); err != nil {
		var he *host.Error
		if errors.As(err, &he) {
			fmt.Fprintln(stderr, he.Message)
		} else {
			fmt.Fprintln(stderr, err)
		}
		return 1
	}
	return 0
}
