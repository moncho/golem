package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"golem/internal/config"
	"golem/internal/host"
)

func TestPrettyHandlerReadsLikeALog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newPrettyHandler(&buf, false))
	at := time.Date(2026, 11, 8, 2, 10, 27, 0, time.Local)
	rec := slog.NewRecord(at, slog.LevelInfo, "Golem configured", 0)
	rec.AddAttrs(slog.String("model", "anthropic/claude-sonnet-5-5 (key set)"), slog.String("proactive", "off"), slog.String("workspace", "/w"), slog.String("state", "/s"), slog.String("slack", "off"), slog.String("herma", "off"))
	if err := logger.Handler().Handle(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	logger.Warn("Slack disconnected; reconnecting")
	logger.Error("host exited", "code", 1)
	logger.Info("Herma project", "id", "rec_1")
	logger.Info("command finished", "work", "work_1", "job", "job_1", "status", "done", "exit", 0)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[0] != "02:10:27 Golem configured" {
		t.Fatalf("first line %q", lines[0])
	}
	for i, want := range []string{"model      anthropic/claude-sonnet-5-5 (key set)", "proactive  off", "workspace  /w", "state      /s", "slack      off", "herma      off"} {
		if got := strings.TrimLeft(lines[1+i], " "); got != want || !strings.HasPrefix(lines[1+i], "         ") {
			t.Fatalf("block line %d = %q, want indented %q", i, lines[1+i], want)
		}
	}
	if !strings.Contains(out, " WARN  Slack disconnected; reconnecting") || !strings.Contains(out, " ERROR host exited  code=1") {
		t.Fatalf("levels and short attrs:\n%s", out)
	}
	if !strings.Contains(out, " command finished  work=work_1 job=job_1 status=done exit=0") {
		t.Fatalf("up to five attrs stay inline:\n%s", out)
	}
	if !strings.Contains(out, " Herma project  id=rec_1") || strings.Contains(out, "INFO") {
		t.Fatalf("info records carry no level tag and inline short attrs:\n%s", out)
	}
}

func TestStartupAttrsAreComposedForPeople(t *testing.T) {
	settings := config.Settings{
		Model:     host.Model{Provider: "anthropic", ModelID: "claude-sonnet-5-5", RouterModelID: "claude-haiku-5-5", APIKey: "k"},
		Proactive: "off", Channels: []string{"C1", "C2"}, Users: []string{"U1"},
		Slack: &config.SlackSettings{BotToken: "b", AppToken: "a"},
		Node:  &config.NodeSettings{URL: "http://node.example:7373", Token: "t"},
		Herma: true, HermaURL: "https://herma.example", HermaIdentity: "golem-agent",
	}
	attrs := startupAttrs(settings, serviceOptions{Workspace: "/work", Retain: 336 * time.Hour}, "/state", []string{"local", "remote"}, "rec_p")
	got := map[string]string{}
	for i := 0; i+1 < len(attrs); i += 2 {
		got[attrs[i].(string)] = attrs[i+1].(string)
	}
	for key, want := range map[string]string{
		"model":     "anthropic/claude-sonnet-5-5, router claude-haiku-5-5 (key set)",
		"execution": "local, remote http://node.example:7373; logs kept 14d",
		"slack":     "on: 2 channels, 1 user",
		"herma":     "https://herma.example as golem-agent, project rec_p",
		"workspace": "/work", "state": "/state", "proactive": "off",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	plain := startupAttrs(config.Settings{Model: host.Model{Provider: "openai", ModelID: "gpt-4.1-mini"}, Proactive: "off"}, serviceOptions{Workspace: ".", Retain: 36 * time.Hour}, "/s", []string{"local"}, "")
	got = map[string]string{}
	for i := 0; i+1 < len(plain); i += 2 {
		got[plain[i].(string)] = plain[i+1].(string)
	}
	open := startupAttrs(config.Settings{Model: host.Model{Provider: "openai", ModelID: "m"}, Proactive: "off", Users: []string{"U1"}, Slack: &config.SlackSettings{}}, serviceOptions{Retain: time.Hour}, "/s", []string{"local"}, "")
	for i := 0; i+1 < len(open); i += 2 {
		if open[i] == "slack" && open[i+1] != "on: any channel the bot is in, 1 user" {
			t.Errorf("slack = %q", open[i+1])
		}
	}
	for key, want := range map[string]string{"model": "openai/gpt-4.1-mini (key missing)", "execution": "local; logs kept 36h", "slack": "off", "herma": "off"} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
}

func TestHostLogKeepsFieldOrderAndLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newPrettyHandler(&buf, false))
	if err := hostLog(logger, []byte(`{"level":"info","message":"command finished","fields":{"work":"work_371","job":"job_1","status":"done","exit":0,"ms":32000}}`)); err != nil {
		t.Fatal(err)
	}
	if err := hostLog(logger, []byte(`{"level":"warn","message":"reply failed","fields":{"thread":"slack:C1:1","attempts":10}}`)); err != nil {
		t.Fatal(err)
	}
	if err := hostLog(logger, []byte(`{"level":"error","message":"Golem runtime: task_interrupted"}`)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, " command finished  work=work_371 job=job_1 status=done exit=0 ms=32000") {
		t.Fatalf("fields must keep the host's order, inline:\n%s", out)
	}
	if !strings.Contains(out, " WARN  reply failed  thread=slack:C1:1 attempts=10") || !strings.Contains(out, " ERROR Golem runtime: task_interrupted") {
		t.Fatalf("levels:\n%s", out)
	}
	if hostLog(logger, []byte(`{not json`)) == nil {
		t.Fatal("malformed log params must be an error")
	}
}
