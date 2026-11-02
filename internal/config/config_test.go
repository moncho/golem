package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadKeepsQuotedValues(t *testing.T) {
	p := write(t, "GOLEM_MODEL=\"a=b#c\"\r\nexport OPENAI_API_KEY='k'\r\nIGNORED=x")
	v, err := Load(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v["GOLEM_MODEL"] != "a=b#c" || v["OPENAI_API_KEY"] != "k" {
		t.Fatalf("values: %v", v)
	}
	if _, ok := v["IGNORED"]; ok {
		t.Fatal("IGNORED loaded")
	}
}

func TestEnvironmentWinsOverFile(t *testing.T) {
	p := write(t, "GOLEM_MODEL=file\n")
	v, err := Load(p, []string{"GOLEM_MODEL=env", "OTHER=x"})
	if err != nil {
		t.Fatal(err)
	}
	if v["GOLEM_MODEL"] != "env" || len(v) != 1 {
		t.Fatalf("values: %v", v)
	}
}

func TestLoadMissingFileAndReadError(t *testing.T) {
	v, err := Load(filepath.Join(t.TempDir(), "none"), nil)
	if err != nil || len(v) != 0 {
		t.Fatalf("missing: %v %v", v, err)
	}
	if _, err := Load(t.TempDir(), nil); err == nil || err.Error() != "Could not read configuration." {
		t.Fatalf("directory: %v", err)
	}
}

func TestBuildDefaultsAndMessages(t *testing.T) {
	for _, c := range []struct {
		name      string
		values    map[string]string
		slack     bool
		proactive string
		want      string
		check     func(*testing.T, Settings)
	}{
		{name: "missing model", values: map[string]string{}, proactive: "off",
			want: "Set GOLEM_MODEL and GOLEM_PROVIDER (openai, anthropic, deepseek or compatible). Use golem demo without a model."},
		{name: "faux", values: map[string]string{"GOLEM_PROVIDER": "faux"}, proactive: "off",
			check: func(t *testing.T, s Settings) {
				if s.Model.Provider != "faux" || s.Model.ModelID != "faux-1" {
					t.Fatalf("%+v", s.Model)
				}
			}},
		{name: "base url", values: map[string]string{"GOLEM_MODEL": "m", "GOLEM_BASE_URL": "http://x", "GOLEM_API_KEY": "g", "OPENAI_API_KEY": "o"}, proactive: "off",
			check: func(t *testing.T, s Settings) {
				if s.Model.Provider != "compatible" || s.Model.APIKey != "g" || s.Model.BaseURL != "http://x" {
					t.Fatalf("%+v", s.Model)
				}
			}},
		{name: "provider key", values: map[string]string{"GOLEM_MODEL": "m", "GOLEM_PROVIDER": "anthropic", "ANTHROPIC_API_KEY": "a", "GOLEM_ROUTER_MODEL": "r"}, proactive: "act",
			check: func(t *testing.T, s Settings) {
				if s.Model.APIKey != "a" || s.Model.RouterModelID != "r" || s.Proactive != "act" {
					t.Fatalf("%+v", s)
				}
			}},
		{name: "slack without users", values: map[string]string{"GOLEM_MODEL": "m", "SLACK_BOT_TOKEN": "b", "SLACK_APP_TOKEN": "a", "GOLEM_SLACK_CHANNELS": "c"}, slack: true, proactive: "off",
			want: "Slack requires both tokens and GOLEM_SLACK_USERS."},
		{name: "slack without channels", values: map[string]string{"GOLEM_MODEL": "m", "SLACK_BOT_TOKEN": "b", "SLACK_APP_TOKEN": "a", "GOLEM_SLACK_USERS": "u"}, slack: true, proactive: "off",
			check: func(t *testing.T, s Settings) {
				if s.Slack == nil || len(s.Channels) != 0 || len(s.Users) != 1 {
					t.Fatalf("%+v", s)
				}
			}},
		{name: "slack ok", values: map[string]string{"GOLEM_MODEL": "m", "SLACK_BOT_TOKEN": "b", "SLACK_APP_TOKEN": "a", "GOLEM_SLACK_USERS": "u, v", "GOLEM_SLACK_CHANNELS": "c"}, slack: true, proactive: "off",
			check: func(t *testing.T, s Settings) {
				if s.Slack == nil || s.Slack.BotToken != "b" || len(s.Users) != 2 || s.Users[1] != "v" {
					t.Fatalf("%+v", s)
				}
			}},
		{name: "node", values: map[string]string{"GOLEM_MODEL": "m", "GOLEM_NODE_URL": "http://n"}, proactive: "off",
			want: "A remote execution endpoint requires GOLEM_NODE_TOKEN."},
		{name: "proactive", values: map[string]string{"GOLEM_MODEL": "m"}, proactive: "sometimes",
			want: "Proactive policy must be off, observe or act."},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := Build(c.values, c.slack, false, c.proactive)
			if c.want != "" {
				if err == nil || err.Error() != c.want {
					t.Fatalf("error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, s)
		})
	}
}

func TestHermaEnabledByURL(t *testing.T) {
	base := map[string]string{"GOLEM_PROVIDER": "faux"}
	s, err := Build(base, false, false, "off")
	if err != nil || s.Herma || s.HermaURL != "" {
		t.Fatalf("herma off without a url: %+v %v", s.Herma, err)
	}
	withURL := map[string]string{"GOLEM_PROVIDER": "faux", "GOLEM_HERMA_URL": "https://herma.example", "GOLEM_HERMA_PROJECT": "rec_p"}
	s, err = Build(withURL, false, false, "off")
	if err != nil || !s.Herma || s.HermaURL != "https://herma.example" || s.HermaProject != "rec_p" {
		t.Fatalf("herma on from the url: %+v %v", s, err)
	}
	_, err = Build(base, false, true, "off")
	if err == nil || err.Error() != "Set GOLEM_HERMA_URL to use Herma." {
		t.Fatalf("--herma without a url: %v", err)
	}
}

func TestHermaIdentityFromValues(t *testing.T) {
	s, err := Build(map[string]string{"GOLEM_PROVIDER": "faux", "GOLEM_HERMA_URL": "https://herma.example", "GOLEM_HERMA_IDENTITY": "golem-agent"}, false, false, "off")
	if err != nil || s.HermaIdentity != "golem-agent" {
		t.Fatalf("identity: %q %v", s.HermaIdentity, err)
	}
}

func TestSlackRefusalText(t *testing.T) {
	base := map[string]string{"GOLEM_PROVIDER": "faux", "SLACK_BOT_TOKEN": "b", "SLACK_APP_TOKEN": "a", "GOLEM_SLACK_USERS": "u"}
	s, err := Build(base, true, false, "off")
	if err != nil || s.Slack.Refusal != "I only take requests from people on my owner's list. Ask them to add you." {
		t.Fatalf("default refusal: %q %v", s.Slack.Refusal, err)
	}
	base["GOLEM_SLACK_REFUSAL"] = "Nice try."
	if s, _ = Build(base, true, false, "off"); s.Slack.Refusal != "Nice try." {
		t.Fatalf("override: %q", s.Slack.Refusal)
	}
	base["GOLEM_SLACK_REFUSAL"] = "off"
	if s, _ = Build(base, true, false, "off"); s.Slack.Refusal != "" {
		t.Fatalf("off: %q", s.Slack.Refusal)
	}
}
