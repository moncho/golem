package main

import (
	"strings"
	"testing"
	"time"
)

func sampleUnit() unitSpec {
	return unitSpec{Label: "local.golem", Binary: "/Users/me/bin/golem", State: "/Users/me/ai/golem/.golem-pi", Workspace: "/opt/code/mono",
		EnvFile: "/Users/me/ai/golem/.env", Proactive: "observe", Retain: 336 * time.Hour, Slack: true, Herma: false,
		Path: "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin", Log: "/Users/me/Library/Logs/golem.log"}
}

func TestServeArgsCarryEveryFlagAsAbsolutePaths(t *testing.T) {
	got := strings.Join(serveArgs(sampleUnit()), " ")
	want := "serve --state /Users/me/ai/golem/.golem-pi --workspace /opt/code/mono --env-file /Users/me/ai/golem/.env --proactive observe --retain 336h0m0s --slack"
	if got != want {
		t.Fatalf("args\n got %q\nwant %q", got, want)
	}
}

func TestLaunchdPlistDeclaresTheAgent(t *testing.T) {
	plist := launchdPlist(sampleUnit())
	for _, want := range []string{
		"<key>Label</key>", "<string>local.golem</string>",
		"<string>/Users/me/bin/golem</string>", "<string>serve</string>", "<string>--state</string>", "<string>/Users/me/ai/golem/.golem-pi</string>", "<string>--slack</string>",
		"<key>EnvironmentVariables</key>", "<key>PATH</key>", "<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>",
		"<key>KeepAlive</key>", "<true/>", "<key>RunAtLoad</key>", "<key>ThrottleInterval</key>", "<integer>30</integer>",
		"<key>StandardOutPath</key>", "<string>/Users/me/Library/Logs/golem.log</string>", "<key>StandardErrorPath</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "--herma") {
		t.Fatal("herma was off")
	}
}

func TestSystemdUnitRestartsAndThrottles(t *testing.T) {
	unit := systemdUnit(sampleUnit())
	for _, want := range []string{
		"[Unit]", "Description=Golem", "StartLimitIntervalSec=300", "StartLimitBurst=5",
		"[Service]", "ExecStart=/Users/me/bin/golem serve --state /Users/me/ai/golem/.golem-pi --workspace /opt/code/mono --env-file /Users/me/ai/golem/.env --proactive observe --retain 336h0m0s --slack",
		"Environment=PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin", "Restart=on-failure", "RestartSec=10",
		"[Install]", "WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestServiceRefusesWhileTheStateIsInUse(t *testing.T) {
	// The check that protects a running `golem serve` from a competing agent.
	if msg := inUseMessage("/tmp/x"); !strings.Contains(msg, "already in use") || !strings.Contains(msg, "/tmp/x") {
		t.Fatalf("message: %q", msg)
	}
}
