package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golem/internal/host"
)

// unitSpec is everything a service manager needs to keep `golem serve` running.
type unitSpec struct {
	Label, Binary, State, Workspace, EnvFile, Proactive string
	Retain                                              time.Duration
	Slack, Herma                                        bool
	Path, Log                                           string
}

// serveArgs is the serve command line the unit runs, every path absolute.
func serveArgs(s unitSpec) []string {
	args := []string{"serve", "--state", s.State, "--workspace", s.Workspace, "--env-file", s.EnvFile, "--proactive", s.Proactive, "--retain", s.Retain.String()}
	if s.Slack {
		args = append(args, "--slack")
	}
	if s.Herma {
		args = append(args, "--herma")
	}
	return args
}

// launchdPlist is a user agent that launchd keeps alive and throttles.
func launchdPlist(s unitSpec) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "  <key>Label</key>\n  <string>%s</string>\n", html.EscapeString(s.Label))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range append([]string{s.Binary}, serveArgs(s)...) {
		fmt.Fprintf(&b, "    <string>%s</string>\n", html.EscapeString(arg))
	}
	b.WriteString("  </array>\n")
	fmt.Fprintf(&b, "  <key>EnvironmentVariables</key>\n  <dict>\n    <key>PATH</key>\n    <string>%s</string>\n  </dict>\n", html.EscapeString(s.Path))
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n  <key>KeepAlive</key>\n  <true/>\n  <key>ThrottleInterval</key>\n  <integer>30</integer>\n")
	fmt.Fprintf(&b, "  <key>StandardOutPath</key>\n  <string>%s</string>\n  <key>StandardErrorPath</key>\n  <string>%s</string>\n", html.EscapeString(s.Log), html.EscapeString(s.Log))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// systemdUnit is a user service that restarts on failure and stops retrying
// a crash loop after five starts in five minutes.
func systemdUnit(s unitSpec) string {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=Golem\nStartLimitIntervalSec=300\nStartLimitBurst=5\n\n[Service]\n")
	fmt.Fprintf(&b, "ExecStart=%s %s\n", s.Binary, strings.Join(serveArgs(s), " "))
	fmt.Fprintf(&b, "Environment=PATH=%s\n", s.Path)
	b.WriteString("Restart=on-failure\nRestartSec=10\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

func inUseMessage(state string) string {
	return fmt.Sprintf("The state directory %s is already in use by a running golem serve. Stop it first, then run golem service install again.", state)
}

// runService installs, removes or reports the user service that keeps
// `golem serve` running: a launchd agent on macOS, a systemd user unit on Linux.
func runService(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall" && args[0] != "status") {
		fmt.Fprintln(stderr, "usage: golem service install|uninstall|status [serve flags] [--label NAME]")
		return 2
	}
	flags := flag.NewFlagSet("service "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	spec := unitSpec{}
	flags.StringVar(&spec.Label, "label", "local.golem", "Service name")
	flags.StringVar(&spec.State, "state", ".golem-pi", "Private state directory")
	flags.StringVar(&spec.Workspace, "workspace", ".", "Execution root for local commands")
	flags.StringVar(&spec.EnvFile, "env-file", ".env", "Configuration file")
	flags.StringVar(&spec.Proactive, "proactive", "off", "Ambient event policy: off, observe or act")
	flags.DurationVar(&spec.Retain, "retain", 336*time.Hour, "How long finished command output is kept")
	flags.BoolVar(&spec.Slack, "slack", false, "Receive and reply on Slack")
	flags.BoolVar(&spec.Herma, "herma", false, "Insist on Herma")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	manager, err := newManager(spec.Label)
	if err != nil {
		fmt.Fprintln(stderr, "golem service:", err)
		return 1
	}
	switch args[0] {
	case "status":
		return manager.status(ctx, stdout, stderr)
	case "uninstall":
		if err := manager.uninstall(ctx); err != nil {
			fmt.Fprintln(stderr, "golem service:", err)
			return 1
		}
		fmt.Fprintln(stdout, "Removed", manager.file)
		return 0
	}
	if err := completeSpec(&spec); err != nil {
		fmt.Fprintln(stderr, "golem service:", err)
		return 1
	}
	spec.Log = manager.logPath
	// A second serve on the same state would only fight the running one.
	lock, err := host.Lock(spec.State, 0)
	if err != nil {
		fmt.Fprintln(stderr, inUseMessage(spec.State))
		return 1
	}
	lock.Close()
	if err := manager.install(ctx, spec); err != nil {
		fmt.Fprintln(stderr, "golem service:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Installed %s\nLog: %s\nWatch it with: %s watch --state %s\n", manager.file, spec.Log, spec.Binary, spec.State)
	return 0
}

// completeSpec resolves the binary and every path to absolutes, so the unit
// works from any directory the manager starts it in.
func completeSpec(spec *unitSpec) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	spec.Binary = exe
	for _, p := range []*string{&spec.State, &spec.Workspace, &spec.EnvFile} {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return err
		}
		*p = abs
	}
	if spec.Path = os.Getenv("PATH"); spec.Path == "" {
		spec.Path = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	}
	return nil
}

type manager struct {
	label, file, logPath string
	goos                 string
}

func newManager(label string) (*manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	switch runtime.GOOS {
	case "darwin":
		return &manager{label: label, goos: "darwin", file: filepath.Join(home, "Library", "LaunchAgents", label+".plist"), logPath: filepath.Join(home, "Library", "Logs", label+".log")}, nil
	case "linux":
		return &manager{label: label, goos: "linux", file: filepath.Join(home, ".config", "systemd", "user", label+".service"), logPath: filepath.Join(home, ".local", "state", "golem", label+".log")}, nil
	}
	return nil, errors.New("golem service supports macOS (launchd) and Linux (systemd --user)")
}

func (m *manager) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func (m *manager) run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (m *manager) install(ctx context.Context, spec unitSpec) error {
	if err := os.MkdirAll(filepath.Dir(m.file), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(spec.Log), 0o755); err != nil {
		return err
	}
	switch m.goos {
	case "darwin":
		if err := os.WriteFile(m.file, []byte(launchdPlist(spec)), 0o644); err != nil {
			return err
		}
		_, _ = m.run(ctx, "launchctl", "bootout", m.domain()+"/"+m.label) // a previous install, if any
		_, err := m.run(ctx, "launchctl", "bootstrap", m.domain(), m.file)
		return err
	default:
		if err := os.WriteFile(m.file, []byte(systemdUnit(spec)), 0o644); err != nil {
			return err
		}
		if _, err := m.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		_, err := m.run(ctx, "systemctl", "--user", "enable", "--now", m.label)
		return err
	}
}

func (m *manager) uninstall(ctx context.Context) error {
	switch m.goos {
	case "darwin":
		_, _ = m.run(ctx, "launchctl", "bootout", m.domain()+"/"+m.label)
	default:
		_, _ = m.run(ctx, "systemctl", "--user", "disable", "--now", m.label)
	}
	if err := os.Remove(m.file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *manager) status(ctx context.Context, stdout, stderr io.Writer) int {
	var out string
	var err error
	switch m.goos {
	case "darwin":
		out, err = m.run(ctx, "launchctl", "print", m.domain()+"/"+m.label)
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				if t := strings.TrimSpace(line); strings.HasPrefix(t, "state = ") || strings.HasPrefix(t, "pid = ") || strings.HasPrefix(t, "last exit code = ") {
					fmt.Fprintln(stdout, t)
				}
			}
			return 0
		}
	default:
		out, err = m.run(ctx, "systemctl", "--user", "status", "--no-pager", m.label)
		if err == nil {
			fmt.Fprint(stdout, out)
			return 0
		}
	}
	fmt.Fprintln(stderr, "golem service: not installed or not running:", err)
	return 1
}
