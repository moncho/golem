package slack

import (
	"encoding/json"
	"testing"
)

func payload(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	base := map[string]any{"type": "message", "user": "U1", "channel": "C1", "ts": "12", "thread_ts": "10", "text": "<@SELF> help"}
	for k, v := range fields {
		if v == nil {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	b, err := json.Marshal(map[string]any{"event": base})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseNormalizesMentionsAndRejectsEditsSelfAndBotAuthority(t *testing.T) {
	ordinary, ok := Parse(payload(t, nil), "SELF")
	if !ok {
		t.Fatal("ordinary message rejected")
	}
	mention, ok := Parse(payload(t, map[string]any{"type": "app_mention"}), "SELF")
	if !ok || mention.Key != ordinary.Key || ordinary.Key != "slack:C1:12" {
		t.Fatalf("keys differ: %q %q", ordinary.Key, mention.Key)
	}
	if ordinary.Thread.ID != "slack:C1:10" || ordinary.Thread.ReplyTo != "10" || ordinary.Thread.Source != "slack" || ordinary.Thread.Channel != "C1" || !ordinary.Explicit || ordinary.Sender != "U1" || ordinary.Bot {
		t.Fatalf("unexpected input %+v", ordinary)
	}
	for name, fields := range map[string]map[string]any{
		"self":   {"user": "SELF"},
		"edit":   {"subtype": "message_changed"},
		"anon":   {"user": nil},
		"nochan": {"channel": nil},
		"notext": {"text": nil},
		"other":  {"type": "reaction_added"},
		"nots":   {"ts": nil},
	} {
		if _, ok := Parse(payload(t, fields), "SELF"); ok {
			t.Errorf("%s accepted", name)
		}
	}
	bot, ok := Parse(payload(t, map[string]any{"bot_id": "B1"}), "SELF")
	if !ok || bot.Explicit || !bot.Bot || bot.Sender != "B1" {
		t.Fatalf("bot message %+v", bot)
	}
	im, _ := Parse(payload(t, map[string]any{"text": "hi", "channel_type": "im"}), "SELF")
	plain, _ := Parse(payload(t, map[string]any{"text": "hi"}), "SELF")
	if !im.Explicit || plain.Explicit {
		t.Fatal("explicit rules")
	}
	if _, ok := Parse(json.RawMessage(`not json`), "SELF"); ok {
		t.Fatal("garbage accepted")
	}
}
