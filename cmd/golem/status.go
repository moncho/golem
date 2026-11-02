package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golem/internal/executor"
	"golem/internal/host"
)

// runStatus prints the host's read-only status of the Pi state merged with the
// execution endpoint's job counts.
func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	state := flags.String("state", ".golem-pi", "Private state directory")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	out, err := readStatus(ctx, *state)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\n", out)
	return 0
}

func readStatus(ctx context.Context, state string) ([]byte, error) {
	failed := errors.New("Pi state could not be read.")
	state, err := filepath.Abs(state)
	if err != nil {
		return nil, failed
	}
	// The bundle goes to a temporary directory so a status read never creates
	// or writes the state directory.
	tmp, err := os.MkdirTemp("", "golem-status-")
	if err != nil {
		return nil, failed
	}
	defer os.RemoveAll(tmp)
	script, err := host.Extract(tmp)
	if err != nil {
		return nil, failed
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, errors.New("node not found on PATH.")
	}
	cmd := exec.CommandContext(ctx, node, script, "status", "--state", state)
	cmd.Env = host.Env()
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, failed
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &merged); err != nil {
		return nil, failed
	}
	summary, err := executor.Summary(filepath.Join(state, "execution"))
	if err != nil {
		return nil, errors.New("Execution state could not be read.")
	}
	if merged["execution"], err = json.Marshal(summary); err != nil {
		return nil, failed
	}
	return json.MarshalIndent(merged, "", "  ")
}
