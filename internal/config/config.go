// Package config loads and validates owner configuration. It never logs values.
package config

import (
	"errors"
	"os"
	"regexp"
	"strings"

	"golem/internal/host"
)

var Allowed = regexp.MustCompile(`^(GOLEM_[A-Z_]+|SLACK_BOT_TOKEN|SLACK_APP_TOKEN|OPENAI_API_KEY|ANTHROPIC_API_KEY|DEEPSEEK_API_KEY)$`)

var line = regexp.MustCompile(`^\s*(?:export\s+)?([A-Z_]+)\s*=\s*(.*?)\s*$`)

// Load reads the allowed keys from file, then lets environ (KEY=VALUE) win.
// A missing file is fine.
func Load(file string, environ []string) (map[string]string, error) {
	values := map[string]string{}
	b, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("Could not read configuration.")
	}
	for _, l := range strings.Split(string(b), "\n") {
		m := line.FindStringSubmatch(strings.TrimSuffix(l, "\r"))
		if m == nil || !Allowed.MatchString(m[1]) {
			continue
		}
		v := m[2]
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		values[m[1]] = v
	}
	for _, e := range environ {
		k, v, ok := strings.Cut(e, "=")
		if ok && Allowed.MatchString(k) {
			values[k] = v
		}
	}
	return values, nil
}

// CSV splits a comma separated list, trimming blanks.
func CSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Refusal is the fixed reply to people outside the allowlist; empty is off.
type SlackSettings struct{ BotToken, AppToken, Refusal string }
type NodeSettings struct{ URL, Token string }

type Settings struct {
	Model                 host.Model
	Proactive             string
	Channels, Users, Bots []string
	Slack                 *SlackSettings
	Node                  *NodeSettings
	// Herma is on when GOLEM_HERMA_URL is set or --herma asks for it.
	Herma         bool
	HermaURL      string
	HermaIdentity string
	HermaProject  string
}

// Build validates the values and maps them onto the application's settings.
func Build(values map[string]string, slack, herma bool, proactive string) (Settings, error) {
	var s Settings
	switch proactive {
	case "off", "observe", "act":
	default:
		return s, errors.New("Proactive policy must be off, observe or act.")
	}
	provider := values["GOLEM_PROVIDER"]
	if provider == "" {
		provider = "openai"
		if values["GOLEM_BASE_URL"] != "" {
			provider = "compatible"
		}
	}
	model := values["GOLEM_MODEL"]
	if model == "" && provider == "faux" {
		model = "faux-1"
	}
	if model == "" {
		return s, errors.New("Set GOLEM_MODEL and GOLEM_PROVIDER (openai, anthropic, deepseek or compatible). Use golem demo without a model.")
	}
	key := values["GOLEM_API_KEY"]
	if key == "" {
		key = values[strings.ToUpper(provider)+"_API_KEY"]
	}
	s.Model = host.Model{Provider: provider, ModelID: model, RouterModelID: values["GOLEM_ROUTER_MODEL"], BaseURL: values["GOLEM_BASE_URL"], APIKey: key}
	s.Proactive = proactive
	s.Channels, s.Users, s.Bots = CSV(values["GOLEM_SLACK_CHANNELS"]), CSV(values["GOLEM_SLACK_USERS"]), CSV(values["GOLEM_SLACK_BOTS"])
	if slack {
		if values["SLACK_BOT_TOKEN"] == "" || values["SLACK_APP_TOKEN"] == "" || len(s.Users) == 0 {
			return s, errors.New("Slack requires both tokens and GOLEM_SLACK_USERS.")
		}
		refusal := values["GOLEM_SLACK_REFUSAL"]
		switch refusal {
		case "":
			refusal = "I only take requests from people on my owner's list. Ask them to add you."
		case "off":
			refusal = ""
		}
		s.Slack = &SlackSettings{BotToken: values["SLACK_BOT_TOKEN"], AppToken: values["SLACK_APP_TOKEN"], Refusal: refusal}
	}
	if url := values["GOLEM_NODE_URL"]; url != "" {
		if values["GOLEM_NODE_TOKEN"] == "" {
			return s, errors.New("A remote execution endpoint requires GOLEM_NODE_TOKEN.")
		}
		s.Node = &NodeSettings{URL: url, Token: values["GOLEM_NODE_TOKEN"]}
	}
	s.HermaURL = values["GOLEM_HERMA_URL"]
	if herma && s.HermaURL == "" {
		return s, errors.New("Set GOLEM_HERMA_URL to use Herma.")
	}
	if s.HermaURL != "" {
		s.Herma = true
		s.HermaIdentity = values["GOLEM_HERMA_IDENTITY"]
		s.HermaProject = values["GOLEM_HERMA_PROJECT"]
	}
	return s, nil
}
