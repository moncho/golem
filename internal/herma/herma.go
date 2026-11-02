// Package herma reads accepted principles and shared memory through the herma
// CLI. Herma stays authoritative; nothing here stores its records.
package herma

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golem/internal/host"
)

const (
	binary    = "herma" // found on PATH
	timeout   = 10 * time.Second
	maxOutput = 1 << 20

	principlesUnavailable = "Could not load accepted Herma principles. Check the Herma connection and agent credentials."
	memoryUnavailable     = "Herma memory is unavailable. A proposed write may need retrying with the same request ID."
	outageWarning         = "Golem: Herma is unavailable; using the last successfully loaded principles."
)

// Runner runs one herma command and returns its stdout.
type Runner func(ctx context.Context, args []string) (string, error)

// Error carries a message safe for callers; Unwrap gives the cause for logs.
type Error struct {
	Message string
	Err     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

// CLI runs the installed herma binary with the allowlisted environment plus
// HERMA_* variables, so model and channel secrets never reach it. Errors name
// the subcommand only, never arguments or output.
func CLI(url, identity string) Runner {
	return func(ctx context.Context, args []string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		full := arguments(url, args)
		name := "herma"
		if len(args) > 0 {
			name = "herma " + args[0]
		}
		cmd := exec.CommandContext(ctx, binary, full...)
		cmd.Env = environment(identity)
		var stderr limitedBuffer
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		if err := cmd.Start(); err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		out, readErr := io.ReadAll(io.LimitReader(stdout, maxOutput+1))
		if len(out) > maxOutput {
			cancel() // stop a process still writing into the pipe
			readErr = errors.New("output exceeds 1 MiB")
		}
		_, _ = io.Copy(io.Discard, stdout)
		if err := cmd.Wait(); err != nil {
			if reason := stderr.firstLine(); reason != "" {
				return "", fmt.Errorf("%s: %w: %s", name, err, reason)
			}
			return "", fmt.Errorf("%s: %w", name, err)
		}
		if readErr != nil {
			return "", fmt.Errorf("%s: %w", name, readErr)
		}
		return string(out), nil
	}
}

// environment is the allowlist plus HERMA_* and XDG_CONFIG_HOME from the
// process; a configured identity replaces any ambient HERMA_IDENTITY.
func environment(identity string) []string {
	env := host.Env()
	for _, kv := range os.Environ() {
		if identity != "" && strings.HasPrefix(kv, "HERMA_IDENTITY=") {
			continue
		}
		if strings.HasPrefix(kv, "HERMA_") || strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			env = append(env, kv)
		}
	}
	if identity != "" {
		env = append(env, "HERMA_IDENTITY="+identity)
	}
	return env
}

// limitedBuffer keeps the first 4 KiB of herma's stderr: its error messages
// explain a failed call and never carry tokens.
type limitedBuffer struct{ b []byte }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := 4096 - len(l.b); room > 0 {
		l.b = append(l.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (l *limitedBuffer) firstLine() string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(l.b)), "\n")
	return strings.TrimPrefix(line, "herma: ")
}

// Principles caches the accepted global and project principles for ttl. After
// one successful read it keeps serving the last complete read through an outage.
type Principles struct {
	run     Runner
	project string
	ttl     time.Duration
	now     func() time.Time
	warn    func(string)

	mu      sync.Mutex
	current string
	loaded  bool
	readAt  time.Time
	warned  bool
}

// LoadPrinciples requires one successful read.
func LoadPrinciples(ctx context.Context, run Runner, project string, ttl time.Duration, now func() time.Time, warn func(string)) (*Principles, error) {
	p := &Principles{run: run, project: project, ttl: ttl, now: now, warn: warn}
	if _, err := p.Current(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// Current returns the principles, refreshing them once the cache is ttl old.
func (p *Principles) Current(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded && p.now().Sub(p.readAt) < p.ttl {
		return p.current, nil
	}
	p.readAt = p.now()
	text, err := p.read(ctx)
	if err != nil {
		if !p.loaded {
			return "", &Error{Message: principlesUnavailable, Err: err}
		}
		if !p.warned {
			p.warn(outageWarning)
		}
		p.warned = true
		return p.current, nil
	}
	p.current, p.loaded, p.warned = text, true, false
	return text, nil
}

func (p *Principles) read(ctx context.Context) (string, error) {
	global, err := p.run(ctx, []string{"principles"})
	if err != nil {
		return "", err
	}
	parts := []string{strings.TrimSpace(global)}
	if p.project != "" {
		scoped, err := p.run(ctx, []string{"principles", "--project", p.project})
		if err != nil {
			return "", err
		}
		parts = append(parts, strings.TrimSpace(scoped))
	}
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "\n\n"), nil
}

// Memory recalls, reads and proposes Herma records. Proposals are reviewable
// knowledge only; the request ID makes a retried proposal idempotent.
type Memory struct {
	run   Runner
	scope []string
}

func NewMemory(run Runner, project string) *Memory {
	m := &Memory{run: run}
	if project != "" {
		m.scope = []string{"--project", project}
	}
	return m
}

func (m *Memory) call(ctx context.Context, args ...string) (string, error) {
	out, err := m.run(ctx, args)
	if err != nil {
		return "", &Error{Message: memoryUnavailable, Err: err}
	}
	return out, nil
}

func (m *Memory) Recall(ctx context.Context, query string) (string, error) {
	args := append([]string{"recall", query}, m.scope...)
	return m.call(ctx, append(args, "--limit", "8", "--max-bytes", "12000")...)
}

func (m *Memory) Get(ctx context.Context, id string) (string, error) {
	return m.call(ctx, "get", id)
}

func (m *Memory) Propose(ctx context.Context, title, body, source, requestID string) (string, error) {
	args := []string{"create", "--kind", "knowledge", "--status", "proposed", "--title", title,
		"--body", body, "--sources", source, "--request-id", requestID}
	return m.call(ctx, append(args, m.scope...)...)
}

// arguments puts the server URL before the subcommand, where herma's global
// flags go.
func arguments(url string, args []string) []string {
	return append([]string{"--url", url}, args...)
}
