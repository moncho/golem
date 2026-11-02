package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golem/internal/host"
)

// ErrUndeliverable marks a Slack answer that no retry can fix.
var ErrUndeliverable = errors.New("slack rejected the message")

var errRequest = errors.New("Slack request failed. Check credentials, scopes and connectivity.")

// permanent lists Slack error codes that a retry cannot fix.
var permanent = map[string]bool{
	"channel_not_found": true, "not_in_channel": true, "is_archived": true, "msg_too_long": true,
	"restricted_action": true, "cannot_dm_bot": true, "user_not_found": true,
}

// Client talks to Slack with a bot token (Web API) and an app token (Socket Mode).
type Client struct {
	// API is the Web API base URL; tests point it at a local server.
	API string
	// Logger receives reconnect notices; it never sees tokens or message text.
	Logger *slog.Logger
	// OnStop receives a session's stop button; an error leaves the event
	// unacknowledged so Slack redelivers it.
	OnStop func(context.Context, Stop) error

	statusOff atomic.Bool

	now       func() time.Time
	refusedMu sync.Mutex
	refused   map[string]time.Time

	botToken, appToken string
	http               *http.Client
}

func New(botToken, appToken string) *Client {
	return &Client{API: "https://slack.com/api", Logger: slog.Default(), now: time.Now, botToken: botToken, appToken: appToken,
		http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) call(ctx context.Context, method, token string, body any) (map[string]any, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, errRequest
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.API+"/"+method, bytes.NewReader(b))
	if err != nil {
		return nil, errRequest
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errRequest
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errRequest
	}
	var value map[string]any
	if json.NewDecoder(resp.Body).Decode(&value) != nil {
		return nil, errRequest
	}
	if ok, _ := value["ok"].(bool); ok {
		return value, nil
	}
	if code, _ := value["error"].(string); permanent[code] {
		return nil, ErrUndeliverable
	}
	return nil, errRequest
}

// Send posts text into the thread. Permanent rejections return ErrUndeliverable.
func (c *Client) Send(ctx context.Context, thread host.Thread, text string) error {
	_, err := c.call(ctx, "chat.postMessage", c.botToken, map[string]any{
		"channel": thread.Channel, "thread_ts": thread.ReplyTo, "text": text, "unfurl_links": false, "unfurl_media": false,
	})
	return err
}
