package host

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

type Process struct {
	Peer *Peer

	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{}
	once  sync.Once

	waitErr  error
	exitCode int
}

// Spawn runs `node script args...`. The host ends when its stdin closes (see
// Close); ctx is deliberately not tied to the child so a dead context cannot
// kill a host mid-write.
func Spawn(ctx context.Context, script string, args []string, extra []*os.File, logger *slog.Logger) (*Process, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, fmt.Errorf("node not found: %w", err)
	}
	cmd := exec.Command(node, append([]string{script}, args...)...)
	cmd.Env = Env()
	cmd.ExtraFiles = extra
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Own the stdout pipe: Wait closes pipes made by StdoutPipe, which would
	// cut off a reply the reader has not consumed yet.
	stdout, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	err = cmd.Start()
	pw.Close() // the child holds its own copy
	if err != nil {
		stdout.Close()
		return nil, err
	}
	p := &Process{Peer: NewPeer(&closeAtEnd{stdout}, stdin, logger), cmd: cmd, stdin: stdin, done: make(chan struct{})}
	errsDone := make(chan struct{})
	go func() {
		defer close(errsDone)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			logger.Debug("host", "stderr", sc.Text())
		}
		_, _ = io.Copy(io.Discard, stderr)
	}()
	go func() {
		<-errsDone // Wait closes the pipes, so drain stderr first
		p.waitErr = cmd.Wait()
		p.exitCode = cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	return p, nil
}

// closeAtEnd closes the file once the reader reaches end of file or fails.
type closeAtEnd struct{ f *os.File }

func (c *closeAtEnd) Read(b []byte) (int, error) {
	n, err := c.f.Read(b)
	if err != nil {
		c.f.Close()
	}
	return n, err
}

func (p *Process) Done() <-chan struct{} { return p.done }

// ExitCode is valid once Done is closed; it is -1 before.
func (p *Process) ExitCode() int {
	select {
	case <-p.done:
		return p.exitCode
	default:
		return -1
	}
}

// Close closes the host's stdin, waits up to timeout for it to exit, then
// kills it. It returns the exit error, if any.
func (p *Process) Close(timeout time.Duration) error {
	p.once.Do(func() { _ = p.stdin.Close() })
	select {
	case <-p.done:
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
	return p.waitErr
}
