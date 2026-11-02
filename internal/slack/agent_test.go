package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"golem/internal/host"
)

func TestParseStop(t *testing.T) {
	stop, ok := ParseStop(json.RawMessage(`{"event":{"type":"agent_session_stopped","channel":"C1","thread_ts":"10.5","user":"U1"}}`))
	if !ok || stop.Thread != "slack:C1:10.5" || stop.Channel != "C1" || stop.User != "U1" {
		t.Fatalf("stop: %+v %v", stop, ok)
	}
	if _, ok := ParseStop(json.RawMessage(`{"event":{"type":"message","channel":"C1","ts":"1"}}`)); ok {
		t.Fatal("a message is not a stop")
	}
	if _, ok := ParseStop(json.RawMessage(`{"event":{"type":"agent_session_stopped","channel":"C1"}}`)); ok {
		t.Fatal("a stop without a thread is ignored")
	}
}

func TestSetStatusPostsTheSessionStatusAndGivesUpWhenUnavailable(t *testing.T) {
	var bodies []map[string]any
	answer := `{"ok":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agents.sessions.setStatus" || r.Header.Get("Authorization") != "Bearer bot" {
			t.Errorf("unexpected call %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		bodies = append(bodies, body)
		_, _ = io.WriteString(w, answer)
	}))
	defer srv.Close()
	c := New("bot", "app")
	c.API = srv.URL
	thread := host.Thread{ID: "slack:C1:10.5", Source: "slack", Channel: "C1", ReplyTo: "10.5"}
	c.SetStatus(context.Background(), thread, "processing")
	if len(bodies) != 1 || bodies[0]["channel_id"] != "C1" || bodies[0]["thread_ts"] != "10.5" || bodies[0]["status"] != "processing" {
		t.Fatalf("body: %v", bodies)
	}
	answer = `{"ok":false,"error":"feature_disabled"}`
	c.SetStatus(context.Background(), thread, "active")
	c.SetStatus(context.Background(), thread, "active")
	if len(bodies) != 2 {
		t.Fatalf("after a refusal the client stops calling; got %d calls", len(bodies))
	}
}

func TestSessionStatusFollowsTheLifecycle(t *testing.T) {
	for _, c := range []struct {
		name   string
		fields map[string]string
		thread string
		status string
	}{
		{"worker started", map[string]string{"thread": "slack:C1:10"}, "slack:C1:10", "processing"},
		{"worker resumed", map[string]string{"thread": "slack:C1:10"}, "slack:C1:10", "processing"},
		{"command started", map[string]string{"thread": "slack:C1:10"}, "slack:C1:10", "processing"},
		{"message admitted", map[string]string{"thread": "slack:C1:10", "route": "subscribed"}, "slack:C1:10", "processing"},
		{"message admitted", map[string]string{"thread": "slack:C1:10", "route": "router"}, "", ""},
		{"worker reported", map[string]string{"thread": "slack:C1:10", "status": "waiting"}, "slack:C1:10", "suspended"},
		{"worker reported", map[string]string{"thread": "slack:C1:10", "status": "done"}, "slack:C1:10", "active"},
		{"worker reported", map[string]string{"thread": "slack:C1:10", "status": "progress"}, "", ""},
		{"worker cancelled", map[string]string{"thread": "slack:C1:10"}, "slack:C1:10", "active"},
		{"worker started", map[string]string{"thread": "console:main"}, "", ""},
		{"reply delivered", map[string]string{"thread": "slack:C1:10"}, "", ""},
	} {
		thread, status, ok := SessionStatus(c.name, c.fields)
		if ok != (c.status != "") || status != c.status || (ok && (thread.ID != c.thread || thread.Channel != "C1" || thread.ReplyTo != "10")) {
			t.Errorf("%s %v → %+v %q %v", c.name, c.fields, thread, status, ok)
		}
	}
}
