package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"golem/internal/host"
)

// Stop is a person pressing the stop button of an agent session.
type Stop struct {
	Thread, Channel, User string
}

// ParseStop reads an agent_session_stopped event; the thread id has the same
// shape Parse gives messages, so the application finds the Worker by it.
func ParseStop(payload json.RawMessage) (Stop, bool) {
	var p struct {
		Event struct {
			Type     string `json:"type"`
			Channel  string `json:"channel"`
			ThreadTS string `json:"thread_ts"`
			User     string `json:"user"`
		} `json:"event"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Event.Type != "agent_session_stopped" || p.Event.Channel == "" || p.Event.ThreadTS == "" {
		return Stop{}, false
	}
	return Stop{Thread: "slack:" + p.Event.Channel + ":" + p.Event.ThreadTS, Channel: p.Event.Channel, User: p.Event.User}, true
}

// SetStatus shows the session's state in Slack: processing while a Worker
// runs, suspended while it waits on a person, active otherwise. It is best
// effort: a workspace without agent features refuses once and is not asked
// again, and no failure reaches the application.
func (c *Client) SetStatus(ctx context.Context, thread host.Thread, status string) {
	if c.statusOff.Load() {
		return
	}
	body, _ := json.Marshal(map[string]string{"channel_id": thread.Channel, "thread_ts": thread.ReplyTo, "status": status})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.API+"/agents.sessions.setStatus", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.botToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		c.Logger.Debug("Slack session status not sent")
		return
	}
	defer resp.Body.Close()
	var answer struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.NewDecoder(resp.Body).Decode(&answer) != nil || answer.OK {
		return
	}
	switch answer.Error {
	case "feature_disabled", "missing_scope", "unknown_method", "not_allowed_token_type":
		if c.statusOff.CompareAndSwap(false, true) {
			c.Logger.Warn("Slack agent session status is unavailable; continuing without it", "reason", answer.Error)
		}
	default:
		c.Logger.Debug("Slack session status refused", "reason", answer.Error)
	}
}

// SessionStatus maps one lifecycle event of the application to the status its
// Slack thread should show. Events on other channels, and events that change
// nothing, give ok false.
func SessionStatus(name string, fields map[string]string) (thread host.Thread, status string, ok bool) {
	switch name {
	case "worker started", "worker resumed", "command started":
		status = "processing"
	case "message admitted":
		if fields["route"] == "subscribed" {
			status = "processing"
		}
	case "worker reported":
		switch fields["status"] {
		case "waiting":
			status = "suspended"
		case "done":
			status = "active"
		}
	case "worker cancelled":
		status = "active"
	}
	parts := strings.SplitN(fields["thread"], ":", 3)
	if status == "" || len(parts) != 3 || parts[0] != "slack" {
		return host.Thread{}, "", false
	}
	return host.Thread{ID: fields["thread"], Source: "slack", Channel: parts[1], ReplyTo: parts[2]}, status, true
}
