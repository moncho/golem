package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"golem/internal/watch"
)

// runWatch shows the running service as an animated hierarchy. Without a
// terminal on both ends it prints one frame and exits, so scripts and tests
// can read it.
func runWatch(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("watch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	state := flags.String("state", ".golem-pi", "Private state directory of the service to watch")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	snap, err := watch.Fetch(*state)
	if err != nil {
		fmt.Fprintln(stderr, "golem watch:", err)
		return 1
	}
	now := time.Now()
	view := watch.NewView(now)
	view.Update(snap, now)
	out, outIsFile := stdout.(*os.File)
	in, inIsFile := stdin.(*os.File)
	if !outIsFile || !inIsFile || !watch.IsTerminal(out) || !watch.IsTerminal(in) {
		for _, line := range view.Render(now, 100, 30) {
			fmt.Fprintln(stdout, line)
		}
		return 0
	}
	if err := watch.Run(ctx, in, out, *state, view); err != nil {
		fmt.Fprintln(stderr, "golem watch:", err)
		return 1
	}
	return 0
}
