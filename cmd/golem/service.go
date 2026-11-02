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
	"strings"
	"sync"
	"time"

	"golem/internal/config"
	"golem/internal/console"
	"golem/internal/executor"
	"golem/internal/herma"
	"golem/internal/host"
	"golem/internal/slack"
)

const (
	initTimeout  = 15 * time.Second
	closeTimeout = 10 * time.Second
	lockWait     = 10 * time.Second
)

// hostCallTimeout bounds every Go-to-host call; a variable only so tests can
// shorten it.
var hostCallTimeout = 15 * time.Second

type serviceOptions struct {
	State, Workspace, EnvFile string
	// Values, when non-nil, replaces loading EnvFile and the environment.
	Values    map[string]string
	Slack     bool
	Herma     bool
	Proactive string
	Retain    time.Duration
	// Send delivers a Router message; nil prints console threads to stdout.
	Send func(thread host.Thread, text string) error
}

// errSlackStart reports that Slack could not be started with the given settings.
var errSlackStart = errors.New("Slack could not start. Check tokens and scopes.")

// errHostExited reports that the host ended while the service was running.
type errHostExited struct{ code int }

func (e errHostExited) Error() string { return fmt.Sprintf("host exited with code %d", e.code) }

// service owns the state lock, the execution endpoint, the host process and
// the controller socket.
type service struct {
	ctx     context.Context
	base    context.Context // outlives ctx so closing can still talk to the host
	cancel  context.CancelFunc
	logger  *slog.Logger
	opts    serviceOptions
	init    host.Init
	router  *executor.Router
	local   *executor.Local
	lock    *os.File
	script  string
	send    func(thread host.Thread, text string) error
	slack   *slack.Client
	server  *console.Server
	exited  chan struct{}
	exitErr error
	once    sync.Once

	// principles and memory are nil unless Herma is enabled.
	slackUsers   []string
	slackRefusal string
	principles   *herma.Principles
	memory       *herma.Memory

	mu     sync.Mutex
	proc   *host.Process
	closed bool
}

// lockedWriter serialises writes from Router delivery and the CLI.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

func startService(ctx context.Context, opts serviceOptions, stdout io.Writer, logger *slog.Logger) (_ *service, err error) {
	if opts.Retain <= 0 {
		return nil, errors.New("--retain must be a positive duration.")
	}
	values := opts.Values
	if values == nil {
		if values, err = config.Load(opts.EnvFile, os.Environ()); err != nil {
			return nil, err
		}
	}
	settings, err := config.Build(values, opts.Slack, opts.Herma, opts.Proactive)
	if err != nil {
		return nil, err
	}
	state, err := filepath.Abs(opts.State)
	if err != nil {
		return nil, err
	}

	base, cancel := context.WithCancel(context.Background())
	s := &service{ctx: ctx, base: base, cancel: cancel, logger: logger, opts: opts, send: opts.Send, exited: make(chan struct{})}
	if s.send == nil {
		s.send = consoleSend(stdout)
	}
	if settings.Slack != nil {
		s.slack = slack.New(settings.Slack.BotToken, settings.Slack.AppToken)
		s.slackUsers, s.slackRefusal = settings.Users, settings.Slack.Refusal
		s.slack.Logger = logger
	}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	// The state lock comes first: it guards everything under the state
	// directory, including the execution endpoint's records.
	if s.lock, err = host.Lock(state, lockWait); err != nil {
		return nil, err
	}
	if s.local, err = executor.Open(opts.Workspace, filepath.Join(state, "execution")); err != nil {
		return nil, err
	}
	go s.local.RunSweeper(base, opts.Retain, time.Hour)
	backends := map[string]executor.Backend{"local": s.local}
	if settings.Node != nil {
		backends["remote"] = executor.NewRemote(settings.Node.URL, settings.Node.Token)
	}
	s.router = executor.NewRouter(backends)
	environments := make([]string, 0, len(backends))
	for name := range backends {
		environments = append(environments, name)
	}
	slices.Sort(environments)
	s.init = host.Init{
		State: state, Model: settings.Model, Proactive: settings.Proactive,
		Channels: nonNil(settings.Channels), Users: nonNil(settings.Users), Bots: nonNil(settings.Bots),
		Environments: environments, Principles: settings.Herma, Memory: settings.Herma,
	}
	project := settings.HermaProject
	if settings.Herma {
		if project == "" {
			if project, err = herma.DiscoverProject(opts.Workspace); err != nil {
				return nil, err
			}
		}
		warn := func(message string) { logger.Warn(message) }
		if s.principles, err = herma.LoadPrinciples(base, herma.CLI(settings.HermaURL, settings.HermaIdentity), project, 5*time.Second, time.Now, warn); err != nil {
			logger.Error("Herma principles could not be loaded", "err", errors.Unwrap(err))
			return nil, err
		}
		s.memory = herma.NewMemory(herma.CLI(settings.HermaURL, settings.HermaIdentity), project)
	}
	logger.Info("Golem configured", startupAttrs(settings, opts, state, environments, project)...)
	if s.script, err = host.Extract(state); err != nil {
		return nil, err
	}
	if err = s.spawn(base); err != nil {
		return nil, err
	}
	if s.server, err = console.Serve(base, opts.State, s, logger); err != nil {
		return nil, err
	}
	if s.slack != nil {
		go s.runSlack()
	}
	return s, nil
}

// slackStatus mirrors a lifecycle event of the application as the status of
// its Slack agent session.
func (s *service) slackStatus(raw json.RawMessage) {
	if s.slack == nil {
		return
	}
	var p struct {
		Message string
		Fields  map[string]any
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	fields := map[string]string{}
	for k, v := range p.Fields {
		if str, ok := v.(string); ok {
			fields[k] = str
		}
	}
	if thread, status, ok := slack.SessionStatus(p.Message, fields); ok {
		go s.slack.SetStatus(s.base, thread, status)
	}
}

// runSlack feeds Slack events to the application until the service closes.
// Run fails only when the bot identity cannot be established, which ends the
// service.
func (s *service) runSlack() {
	s.slack.OnStop = func(ctx context.Context, stop slack.Stop) error {
		return s.call(ctx, "stop", map[string]string{"thread": stop.Thread, "channel": stop.Channel, "sender": stop.User}, nil)
	}
	err := s.slack.Run(s.base, func(ctx context.Context, in host.Input) error {
		// An ignored or duplicate answer is still a successful admission. The
		// application decides who is heard; a person it ignored who addressed
		// the bot directly gets the adapter's fixed refusal.
		result, err := s.Admit(ctx, in)
		if err == nil && result == "ignored" {
			s.slack.Refuse(ctx, in, s.slackUsers, s.slackRefusal)
		}
		return err
	})
	if err != nil && s.base.Err() == nil {
		s.logger.Error("Slack could not start", "err", err)
		s.once.Do(func() {
			s.exitErr = errSlackStart
			close(s.exited)
		})
	}
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func consoleSend(stdout io.Writer) func(host.Thread, string) error {
	return func(thread host.Thread, text string) error {
		switch thread.Source {
		case "console":
			_, err := fmt.Fprintf(stdout, "Router [%s]: %s\n", strings.TrimPrefix(thread.ID, "console:"), text)
			return err
		case "slack":
			return &host.Error{Code: "undeliverable", Message: "Slack is disabled."}
		default:
			return &host.Error{Code: "undeliverable", Message: "Channel is not available."}
		}
	}
}

// spawn starts a host, registers the callbacks and waits up to initTimeout,
// within ctx, for init's answer: the host refuses anything else until init
// completes.
func (s *service) spawn(ctx context.Context) error {
	p, err := host.Spawn(s.base, s.script, []string{"serve"}, []*os.File{s.lock}, s.logger)
	if err != nil {
		return err
	}
	s.handle(p.Peer)
	go p.Peer.Run(s.base)
	ctx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	if err := p.Peer.Call(ctx, "init", s.init, nil); err != nil {
		p.Close(closeTimeout)
		var he *host.Error
		if errors.As(err, &he) {
			return errors.New(he.Message)
		}
		s.logger.Error("host init failed", "err", err)
		return errors.New("The application host did not start.")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		p.Close(closeTimeout)
		return errors.New("service closed")
	}
	s.proc = p
	s.mu.Unlock()
	go s.watch(p)
	return nil
}

// watch reports the host's exit unless it was closed or replaced on purpose.
func (s *service) watch(p *host.Process) {
	<-p.Done()
	s.mu.Lock()
	current := s.proc == p && !s.closed
	s.mu.Unlock()
	if current {
		s.once.Do(func() {
			s.exitErr = errHostExited{p.ExitCode()}
			close(s.exited)
		})
	}
}

var (
	errExecFailed = &host.Error{Code: "execution_failed", Message: "Execution request could not be completed."}
	errExpired    = &host.Error{Code: "expired", Message: "Output no longer retained."}
	errNoEnv      = &host.Error{Code: "unavailable", Message: "Execution environment unavailable."}
	errInternal   = &host.Error{Code: "internal", Message: "Request could not be completed."}
)

func (s *service) handle(peer *host.Peer) {
	type execParams struct {
		Environment string        `json:"environment"`
		Spec        executor.Spec `json:"spec"`
		ID          string        `json:"id"`
		Offset      int64         `json:"offset"`
		Limit       int           `json:"limit"`
	}
	execute := func(method string, run func(ctx context.Context, b executor.Backend, p execParams) (any, error)) {
		peer.Handle(method, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p execParams
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, errExecFailed
			}
			b, ok := s.router.Backend(p.Environment)
			if !ok {
				s.logger.Warn("unknown environment", "method", method)
				return nil, errExecFailed
			}
			res, err := run(ctx, b, p)
			switch {
			case err == nil:
				return res, nil
			case errors.Is(err, executor.ErrOutputExpired):
				return nil, errExpired
			case errors.Is(err, executor.ErrUnavailable):
				return nil, errNoEnv
			default:
				s.logger.Warn("execution request failed", "method", method, "environment", p.Environment, "err", err)
				return nil, errExecFailed
			}
		})
	}
	execute("exec.start", func(ctx context.Context, b executor.Backend, p execParams) (any, error) {
		return b.Start(ctx, p.Spec)
	})
	execute("exec.poll", func(ctx context.Context, b executor.Backend, p execParams) (any, error) {
		return b.Poll(ctx, p.ID)
	})
	execute("exec.stop", func(ctx context.Context, b executor.Backend, p execParams) (any, error) {
		return map[string]bool{"stopped": true}, b.Stop(ctx, p.ID)
	})
	execute("exec.output", func(ctx context.Context, b executor.Backend, p execParams) (any, error) {
		return b.Output(ctx, p.ID, p.Offset, p.Limit)
	})
	peer.Handle("send", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Thread host.Thread `json:"thread"`
			Text   string      `json:"text"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, errInternal
		}
		if p.Thread.Source == "slack" && s.slack != nil {
			switch err := s.slack.Send(ctx, p.Thread, p.Text); {
			case err == nil:
			case errors.Is(err, slack.ErrUndeliverable):
				return nil, &host.Error{Code: "undeliverable", Message: "Slack rejected the message."}
			default:
				s.logger.Warn("Slack delivery failed", "err", err)
				return nil, errInternal
			}
			return map[string]bool{"ok": true}, nil
		}
		if err := s.send(p.Thread, p.Text); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
	peer.Handle("log", func(_ context.Context, raw json.RawMessage) (any, error) {
		if err := hostLog(s.logger, raw); err != nil {
			return nil, errInternal
		}
		s.slackStatus(raw)
		return nil, nil
	})
	s.handleHerma(peer)
}

// handleHerma answers the host's principles and memory calls. Only method
// names and Go errors are logged, never Herma content.
func (s *service) handleHerma(peer *host.Peer) {
	type params struct {
		Query     string `json:"query"`
		ID        string `json:"id"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		Source    string `json:"source"`
		RequestID string `json:"requestId"`
	}
	answer := func(method string, run func(ctx context.Context, p params) (string, error)) {
		peer.Handle(method, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p params
			if s.principles == nil || json.Unmarshal(raw, &p) != nil {
				return nil, errInternal
			}
			out, err := run(ctx, p)
			if err != nil {
				s.logger.Warn("Herma request failed", "method", method, "err", errors.Unwrap(err))
				return nil, &host.Error{Code: "internal", Message: err.Error()}
			}
			return out, nil
		})
	}
	answer("principles", func(ctx context.Context, _ params) (string, error) { return s.principles.Current(ctx) })
	answer("memory.recall", func(ctx context.Context, p params) (string, error) { return s.memory.Recall(ctx, p.Query) })
	answer("memory.get", func(ctx context.Context, p params) (string, error) { return s.memory.Get(ctx, p.ID) })
	answer("memory.propose", func(ctx context.Context, p params) (string, error) {
		return s.memory.Propose(ctx, p.Title, p.Body, p.Source, p.RequestID)
	})
}

func (s *service) peer() (*host.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil {
		return nil, host.ErrClosed
	}
	return s.proc.Peer, nil
}

func (s *service) call(ctx context.Context, method string, params, result any) error {
	p, err := s.peer()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, hostCallTimeout)
	defer cancel()
	return p.Call(ctx, method, params, result)
}

func (s *service) Admit(ctx context.Context, in host.Input) (string, error) {
	var out string
	err := s.call(ctx, "admit", in, &out)
	return out, err
}

func (s *service) Status(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call(ctx, "status", struct{}{}, &out)
	return out, err
}

func (s *service) Search(ctx context.Context, query string) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call(ctx, "search", map[string]string{"query": query}, &out)
	return out, err
}

func (s *service) Inspect(ctx context.Context, work string) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call(ctx, "inspect", map[string]string{"work": work}, &out)
	return out, err
}

func (s *service) History(ctx context.Context, thread string, before int64, after *int64) (json.RawMessage, error) {
	// The host rejects null positions, so an absent position is left out;
	// after 0 is a real position and is sent.
	params := struct {
		Thread string `json:"thread"`
		Before int64  `json:"before,omitempty"`
		After  *int64 `json:"after,omitempty"`
	}{thread, before, after}
	var out json.RawMessage
	err := s.call(ctx, "history", params, &out)
	return out, err
}

func (s *service) Resume(ctx context.Context, work, text, thread, key string) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.call(ctx, "resume", map[string]string{"work": work, "text": text, "thread": thread, "key": key}, &out)
	return out, err
}

// stopHost asks the host to close its application, then ends its stdin.
func (s *service) stopHost(ctx context.Context, p *host.Process) error {
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	if err := p.Peer.Call(ctx, "close", struct{}{}, nil); err != nil && !errors.Is(err, host.ErrClosed) {
		s.logger.Warn("host close failed", "err", err)
	}
	return p.Close(closeTimeout)
}

// RestartHost replaces the host process; the executor and socket stay. A
// respawn that fails ends the service: Wait returns the error.
func (s *service) RestartHost(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("service closed")
	}
	old := s.proc
	s.proc = nil
	s.mu.Unlock()
	if old != nil {
		if err := s.stopHost(ctx, old); err != nil {
			s.logger.Warn("host stopped with an error", "err", err)
		}
	}
	if err := s.spawn(ctx); err != nil {
		s.once.Do(func() {
			s.exitErr = err
			close(s.exited)
		})
		return err
	}
	return nil
}

// Wait returns when the host exits on its own (an errHostExited), a restart
// fails (its error) or ctx ends (nil).
func (s *service) Wait() error {
	select {
	case <-s.exited:
		return s.exitErr
	case <-s.ctx.Done():
		return nil
	}
}

// Close stops the socket, the host, the execution endpoint and releases the lock.
func (s *service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	p := s.proc
	s.proc = nil
	s.mu.Unlock()
	var errs []error
	if s.server != nil {
		errs = append(errs, s.server.Close())
	}
	if p != nil {
		errs = append(errs, s.stopHost(s.base, p))
	}
	if s.local != nil {
		errs = append(errs, s.local.Close())
	}
	s.cancel()
	if s.lock != nil {
		errs = append(errs, s.lock.Close())
	}
	return errors.Join(errs...)
}

// startupAttrs describes the resolved configuration for the startup log as a
// few composed facts. It names what is configured and whether secrets are
// present, never their values.
func startupAttrs(settings config.Settings, opts serviceOptions, state string, environments []string, project string) []any {
	model := settings.Model.Provider + "/" + settings.Model.ModelID
	if settings.Model.RouterModelID != "" {
		model += ", router " + settings.Model.RouterModelID
	}
	if settings.Model.BaseURL != "" {
		model += " at " + settings.Model.BaseURL
	}
	if settings.Model.APIKey != "" {
		model += " (key set)"
	} else {
		model += " (key missing)"
	}
	execution := strings.Join(environments, ", ")
	if settings.Node != nil {
		execution = strings.Replace(execution, "remote", "remote "+settings.Node.URL, 1)
	}
	execution += "; logs kept " + days(opts.Retain)
	slackLine := "off"
	if settings.Slack != nil {
		where := "any channel the bot is in"
		if len(settings.Channels) > 0 {
			where = plural(len(settings.Channels), "channel")
		}
		slackLine = fmt.Sprintf("on: %s, %s", where, plural(len(settings.Users), "user"))
		if len(settings.Bots) > 0 {
			slackLine += ", " + plural(len(settings.Bots), "bot")
		}
	}
	hermaLine := "off"
	if settings.Herma {
		hermaLine = settings.HermaURL
		if settings.HermaIdentity != "" {
			hermaLine += " as " + settings.HermaIdentity
		}
		if project != "" {
			hermaLine += ", project " + project
		} else {
			hermaLine += ", no project"
		}
	}
	return []any{
		"model", model, "proactive", settings.Proactive, "workspace", opts.Workspace, "state", state,
		"execution", execution, "slack", slackLine, "herma", hermaLine,
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// days prints a retention as whole days when it is one, else as hours.
func days(d time.Duration) string {
	if d%(24*time.Hour) == 0 && d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	return fmt.Sprintf("%dh", int(d/time.Hour))
}
