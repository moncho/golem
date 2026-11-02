// Package slack receives Slack events over Socket Mode and posts replies.
package slack

import (
	"encoding/json"
	"strings"

	"golem/internal/host"
)

type event struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	User        string `json:"user"`
	BotID       string `json:"bot_id"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	Text        string `json:"text"`
}

// Parse turns an events_api payload into an input. It ignores edits, the
// bot's own messages and events without an author or text; a message and its
// app_mention share one key so they are admitted once.
func Parse(payload json.RawMessage, botUser string) (host.Input, bool) {
	var p struct {
		Event *event `json:"event"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Event == nil {
		return host.Input{}, false
	}
	e := p.Event
	if (e.Type != "message" && e.Type != "app_mention") || (e.Subtype != "" && e.Subtype != "bot_message") ||
		e.User == botUser || (e.User == "" && e.BotID == "") || e.Channel == "" || e.TS == "" || e.Text == "" {
		return host.Input{}, false
	}
	root := e.ThreadTS
	if root == "" {
		root = e.TS
	}
	sender := e.User
	if e.BotID != "" {
		sender = e.BotID
	}
	bot := e.BotID != ""
	return host.Input{
		Key:    "slack:" + e.Channel + ":" + e.TS,
		Thread: host.Thread{ID: "slack:" + e.Channel + ":" + root, Source: "slack", Channel: e.Channel, ReplyTo: root},
		Text:   e.Text, Sender: sender, Bot: bot,
		Explicit: !bot && (e.Type == "app_mention" || e.ChannelType == "im" || strings.Contains(e.Text, "<@"+botUser+">")),
	}, true
}
