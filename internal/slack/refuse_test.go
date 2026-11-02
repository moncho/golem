package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golem/internal/host"
)

func TestRefuseAnswersAStrangerOncePerHour(t *testing.T) {
	var posts []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		if r.URL.Path == "/chat.postMessage" {
			posts = append(posts, body)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	c := New("bot", "app")
	c.API = srv.URL
	now := time.Unix(1000000, 0)
	c.now = func() time.Time { return now }
	allowed := []string{"U1"}
	in := func(sender string, explicit, bot bool) host.Input {
		return host.Input{Key: "slack:C1:5", Text: "do things", Sender: sender, Explicit: explicit, Bot: bot,
			Thread: host.Thread{ID: "slack:C1:5", Source: "slack", Channel: "C1", ReplyTo: "5"}}
	}
	ctx := context.Background()
	if !c.Refuse(ctx, in("U9", true, false), allowed, "Not for you.") {
		t.Fatal("a stranger who addresses the bot is refused")
	}
	if len(posts) != 1 || posts[0]["channel"] != "C1" || posts[0]["thread_ts"] != "5" || posts[0]["text"] != "Not for you." {
		t.Fatalf("post: %v", posts)
	}
	// The same person again, and Slack's duplicate delivery of a mention, stay silent.
	if c.Refuse(ctx, in("U9", true, false), allowed, "Not for you.") {
		t.Fatal("one refusal per person per hour")
	}
	for _, quiet := range []host.Input{in("U1", true, false), in("U8", false, false), in("B1", true, true)} {
		if c.Refuse(ctx, quiet, allowed, "Not for you.") {
			t.Fatalf("no refusal for %+v", quiet)
		}
	}
	now = now.Add(61 * time.Minute)
	if !c.Refuse(ctx, in("U9", true, false), allowed, "Not for you.") || len(posts) != 2 {
		t.Fatalf("after an hour the stranger is told again; posts=%d", len(posts))
	}
	if c.Refuse(ctx, in("U7", true, false), allowed, "") {
		t.Fatal("an empty text turns the refusal off")
	}
}
