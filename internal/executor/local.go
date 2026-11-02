// Package executor runs commands without owning agent sessions or model keys.
package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

type Spec struct {
	ID             string `json:"id"`
	Command        string `json:"command"`
	Directory      string `json:"directory"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}
type Job struct {
	Spec      Spec   `json:"spec"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	Note      string `json:"note,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// EndedAt is when the job reached a finished status, in Unix milliseconds.
	EndedAt int64 `json:"ended_at,omitempty"`
	// Pgid is the process group of a running command, kept in the record so
	// that the next Open can stop commands a crashed endpoint left behind. It
	// never leaves the endpoint.
	Pgid int `json:"pgid,omitempty"`
	// StartedAt is when the process started, in Unix milliseconds. A record
	// started before the current boot cannot name a live orphan.
	StartedAt int64 `json:"started_at,omitempty"`
}

// bootTime is when the system booted; a variable so tests can fake it.
var bootTime = systemBootTime

// public is the job as answered to callers, without endpoint internals.
func (j Job) public() Job {
	j.Pgid, j.StartedAt = 0, 0
	return j
}

// Chunk is a slice of command output. Bytes counts the log bytes read; a split
// multibyte character becomes U+FFFD on the wire, so callers advance by Bytes.
type Chunk struct {
	Output string `json:"output"`
	Bytes  int    `json:"bytes"`
}

func (j Job) Finished() bool {
	return j.Status == "done" || j.Status == "failed" || j.Status == "cancelled" || j.Status == "interrupted"
}

type Backend interface {
	Start(context.Context, Spec) (Job, error)
	Poll(context.Context, string) (Job, error)
	Stop(context.Context, string) error
	Output(context.Context, string, int64, int) (Chunk, error)
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
var ErrUnavailable = errors.New("execution environment unavailable")

// ErrOutputExpired reports that a job's record exists but its log was swept.
var ErrOutputExpired = errors.New("output no longer retained")

type process struct {
	cmd    *exec.Cmd
	done   chan struct{}
	cancel context.CancelFunc
}
type Local struct {
	mu          sync.Mutex
	root, state string
	jobs        map[string]Job
	active      map[string]*process
	lock        *os.File
	closed      bool
}

func Open(root, state string) (*Local, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(state, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("execution directory is already in use")
	}
	var (
		boot     time.Time
		bootErr  error
		bootRead bool
	)
	l := &Local{root: root, state: state, jobs: map[string]Job{}, active: map[string]*process{}, lock: f}
	files, err := filepath.Glob(filepath.Join(state, "*.json"))
	if err != nil {
		l.Close()
		return nil, err
	}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			l.Close()
			return nil, err
		}
		var j Job
		if json.Unmarshal(b, &j) != nil || !validID.MatchString(j.Spec.ID) {
			l.Close()
			return nil, errors.New("invalid execution state")
		}
		if !j.Finished() {
			// A crashed endpoint leaves its commands running in their own
			// groups; stop them before reporting them interrupted.
			// Only a group started since this boot can be one; without the
			// boot time the group is left alone.
			if j.Pgid > 1 && j.StartedAt != 0 {
				if !bootRead {
					boot, bootErr = bootTime()
					bootRead = true
					if bootErr != nil {
						slog.Warn("boot time unknown; commands left by a previous endpoint are not stopped", "err", bootErr)
					}
				}
				if bootErr == nil && time.UnixMilli(j.StartedAt).After(boot) {
					if err := syscall.Kill(-j.Pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
						slog.Warn("could not stop a command left by a previous endpoint", "err", err)
					}
				}
			}
			j.Pgid, j.StartedAt = 0, 0
			j.Status = "interrupted"
			j.EndedAt = time.Now().UnixMilli()
			j.Note = "Execution environment restarted. Inspect the workspace before retrying; the command may have had effects."
			if err := l.save(j); err != nil {
				l.Close()
				return nil, err
			}
		}
		l.jobs[j.Spec.ID] = j
	}
	return l, nil
}

func (l *Local) save(j Job) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(l.state, j.Spec.ID+".json"), b)
}

// AtomicWrite commits a private snapshot before acknowledging a state change.
func AtomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (l *Local) Start(ctx context.Context, s Spec) (Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Job{}, ErrUnavailable
	}
	if !validID.MatchString(s.ID) || s.Command == "" || len(s.Command) > 1<<20 || s.TimeoutSeconds < 1 || s.TimeoutSeconds > 86400 {
		return Job{}, errors.New("invalid command request")
	}
	if j, ok := l.jobs[s.ID]; ok {
		if j.Status == "cancelled" && j.Spec.Command == "" {
			return j.public(), nil
		}
		if j.Spec != s {
			return Job{}, errors.New("command ID already used")
		}
		return j.public(), nil
	}
	if len(l.active) >= 8 {
		return Job{}, ErrUnavailable
	}
	if filepath.IsAbs(s.Directory) {
		return Job{}, errors.New("directory must be relative to the execution root")
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(l.root, s.Directory))
	if err != nil {
		return Job{}, errors.New("execution directory does not exist")
	}
	rel, err := filepath.Rel(l.root, dir)
	if err != nil || rel == ".." || len(rel) > 3 && rel[:3] == "../" {
		return Job{}, errors.New("directory is outside the execution root")
	}
	j := Job{Spec: s, Status: "starting", ExitCode: -1}
	if err := l.save(j); err != nil {
		return Job{}, err
	}
	l.jobs[s.ID] = j
	out, err := os.OpenFile(filepath.Join(l.state, s.ID+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return l.failStart(j)
	}
	w := &limitedWriter{file: out, left: 8 << 20}
	// The lifetime belongs to this execution endpoint, not to an HTTP request.
	runCtx, cancel := context.WithTimeout(context.Background(), time.Duration(s.TimeoutSeconds)*time.Second)
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", s.Command)
	cmd.Dir = dir
	cmd.Stdout = w
	cmd.Stderr = w
	for _, key := range []string{"PATH", "HOME", "USER", "SHELL", "TMPDIR", "LANG", "LC_ALL", "TERM"} {
		if v, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		out.Close()
		return l.failStart(j)
	}
	p := &process{cmd: cmd, done: make(chan struct{}), cancel: cancel}
	l.active[s.ID] = p
	j.Status = "running"
	j.Pgid, j.StartedAt = cmd.Process.Pid, time.Now().UnixMilli()
	l.jobs[s.ID] = j
	if err := l.save(j); err != nil {
		cancel() /* completion records the stopped process */
	}
	go func() {
		err := cmd.Wait()
		timedOut := runCtx.Err() == context.DeadlineExceeded
		// Do not leave background children after their owning command exits.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cancel()
		_ = out.Sync()
		_ = out.Close()
		l.mu.Lock()
		defer l.mu.Unlock()
		defer close(p.done)
		current := l.jobs[s.ID]
		current.ExitCode = cmd.ProcessState.ExitCode()
		current.Truncated = w.truncated
		current.EndedAt = time.Now().UnixMilli()
		current.Pgid, current.StartedAt = 0, 0
		if current.Status != "cancelled" {
			current.Status = "done"
			if err != nil {
				current.Status = "failed"
			}
			if timedOut {
				current.Note = "Command reached its time limit."
			}
		}
		if err := l.save(current); err != nil {
			current.Status = "interrupted"
			current.Note = "Could not persist command completion."
		}
		l.jobs[s.ID] = current
		delete(l.active, s.ID)
	}()
	return j.public(), nil
}
func (l *Local) failStart(j Job) (Job, error) {
	j.Status = "failed"
	j.Note = "Could not start command."
	l.jobs[j.Spec.ID] = j
	return j, l.save(j)
}
func (l *Local) Poll(ctx context.Context, id string) (Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, ok := l.jobs[id]
	if !ok {
		return Job{}, errors.New("unknown command")
	}
	return j.public(), nil
}
func (l *Local) Stop(ctx context.Context, id string) error {
	if !validID.MatchString(id) {
		return errors.New("invalid command ID")
	}
	l.mu.Lock()
	p := l.active[id]
	if p == nil {
		// Remember cancellation even if a delayed Start request has not arrived yet.
		if _, exists := l.jobs[id]; !exists {
			j := Job{Spec: Spec{ID: id}, Status: "cancelled", ExitCode: -1, EndedAt: time.Now().UnixMilli()}
			if err := l.save(j); err != nil {
				l.mu.Unlock()
				return err
			}
			l.jobs[id] = j
		}
		l.mu.Unlock()
		return nil
	}
	j := l.jobs[id]
	j.Status = "cancelled"
	l.jobs[id] = j
	p.cancel()
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return nil
	}
}
func (l *Local) Output(ctx context.Context, id string, offset int64, limit int) (Chunk, error) {
	if !validID.MatchString(id) || offset < 0 || limit < 1 || limit > 32768 {
		return Chunk{}, errors.New("invalid output range")
	}
	f, err := os.Open(filepath.Join(l.state, id+".log"))
	if err != nil {
		l.mu.Lock()
		_, known := l.jobs[id]
		l.mu.Unlock()
		if known && errors.Is(err, os.ErrNotExist) {
			return Chunk{}, ErrOutputExpired
		}
		return Chunk{}, errors.New("command output unavailable")
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return Chunk{}, err
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)))
	return Chunk{Output: string(b), Bytes: len(b)}, err
}
func (l *Local) Close() error {
	l.mu.Lock()
	l.closed = true
	var pending []*process
	for id, p := range l.active {
		j := l.jobs[id]
		j.Status = "cancelled"
		l.jobs[id] = j
		p.cancel()
		pending = append(pending, p)
	}
	l.mu.Unlock()
	for _, p := range pending {
		<-p.done
	}
	return l.lock.Close()
}

type limitedWriter struct {
	mu        sync.Mutex
	file      *os.File
	left      int
	truncated bool
}

func (w *limitedWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(b)
	keep := min(n, w.left)
	if keep < n {
		w.truncated = true
	}
	if _, err := w.file.Write(b[:keep]); err != nil {
		return 0, fmt.Errorf("write command output: %w", err)
	}
	w.left -= keep
	return n, nil
}

// Sweep removes the logs of jobs that finished more than retain before now.
// Job records stay, so Poll keeps answering after the output is gone.
func (l *Local) Sweep(now time.Time, retain time.Duration) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	removed := 0
	var failure error
	for id, j := range l.jobs {
		if !j.Finished() {
			continue
		}
		path := filepath.Join(l.state, id+".log")
		ended := time.UnixMilli(j.EndedAt)
		if j.EndedAt == 0 {
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			ended = info.ModTime()
		}
		if now.Sub(ended) <= retain {
			continue
		}
		switch err := os.Remove(path); {
		case err == nil:
			removed++
		case !errors.Is(err, os.ErrNotExist):
			failure = errors.Join(failure, err)
		}
	}
	return removed, failure
}

// RunSweeper sweeps at start and then every interval until ctx ends.
func (l *Local) RunSweeper(ctx context.Context, retain, every time.Duration) {
	sweep := func() {
		if _, err := l.Sweep(time.Now(), retain); err != nil {
			slog.Warn("log retention sweep failed")
		}
	}
	sweep()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}

// Summary counts jobs by status from the records alone, without taking the
// execution lock, so it can run beside a serving process.
func Summary(state string) (map[string]int, error) {
	files, err := filepath.Glob(filepath.Join(state, "*.json"))
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		var j Job
		if json.Unmarshal(b, &j) != nil {
			return nil, errors.New("invalid execution state")
		}
		counts[j.Status]++
	}
	return counts, nil
}
