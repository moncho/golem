// Golem is one binary with subcommands; the executor owns commands and output.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

const usage = "usage: golem [serve|attach|watch|status|demo|service|executor] [flags]"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// No subcommand, or flags first, means serve.
	name := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	switch name {
	case "executor":
		if err := runExecutor(args, stdin, stdout); err != nil {
			fmt.Fprintln(stderr, "execution endpoint stopped:", err)
			return 1
		}
		return 0
	case "serve", "attach", "watch", "status", "demo", "service":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		switch name {
		case "serve":
			return runServe(ctx, args, stdout, stderr)
		case "attach":
			return runAttach(ctx, args, stdin, stdout, stderr)
		case "watch":
			return runWatch(ctx, args, stdin, stdout, stderr)
		case "service":
			return runService(ctx, args, stdout, stderr)
		case "status":
			return runStatus(ctx, args, stdout, stderr)
		}
		if len(args) > 0 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		return runDemo(ctx, stdout, stderr)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
}
