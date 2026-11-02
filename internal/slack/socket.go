package slack

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"golem/internal/host"
)

const (
	pingEvery      = 10 * time.Second
	silenceLimit   = 45 * time.Second
	reconnectDelay = 2 * time.Second
	maxPayload     = 1 << 20
	dialTimeout    = 15 * time.Second
)

// Run receives events until ctx ends. It returns an error only when the bot
// identity cannot be established; a lost connection is retried. An envelope
// is acknowledged only after receive returns nil, so Slack redelivers input
// that was not admitted.
func (c *Client) Run(ctx context.Context, receive func(context.Context, host.Input) error) error {
	auth, err := c.call(ctx, "auth.test", c.botToken, struct{}{})
	if err != nil {
		return err
	}
	bot, _ := auth["user_id"].(string)
	if bot == "" {
		return errors.New("Slack bot identity is missing.")
	}
	for ctx.Err() == nil {
		if open, err := c.call(ctx, "apps.connections.open", c.appToken, struct{}{}); err == nil {
			if url, _ := open["url"].(string); url != "" {
				c.socket(ctx, url, bot, receive)
			}
		}
		if ctx.Err() != nil {
			break
		}
		// The dial error can carry the connection URL, so it is not logged.
		c.Logger.Warn("Slack disconnected; reconnecting")
		select {
		case <-ctx.Done():
		case <-time.After(reconnectDelay):
		}
	}
	return nil
}

func (c *Client) socket(ctx context.Context, url, bot string, receive func(context.Context, host.Input) error) {
	dialCtx, cancelDial := context.WithTimeout(ctx, dialTimeout)
	conn, _, err := websocket.Dial(dialCtx, url, nil)
	cancelDial()
	if err != nil {
		return
	}
	defer conn.CloseNow()
	c.Logger.Info("Slack connected")
	conn.SetReadLimit(maxPayload)

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var heard atomic.Int64
	heard.Store(time.Now().UnixNano())
	go func() {
		tick := time.NewTicker(pingEvery)
		defer tick.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-tick.C:
			}
			if time.Since(time.Unix(0, heard.Load())) > silenceLimit {
				conn.CloseNow()
				return
			}
			go func() {
				pctx, stop := context.WithTimeout(cctx, pingEvery)
				defer stop()
				if conn.Ping(pctx) == nil {
					heard.Store(time.Now().UnixNano())
				}
			}()
		}
	}()

	// Envelopes are handled in order by one worker so a slow admission does
	// not stop the reader from answering pings.
	queue := make(chan []byte, 64)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		defer cancel()
		for data := range queue {
			if !c.handle(ctx, conn, data, bot, receive) {
				conn.CloseNow()
				return
			}
		}
	}()
	defer func() { close(queue); <-workerDone }()
	for {
		_, data, err := conn.Read(cctx)
		if err != nil {
			return
		}
		heard.Store(time.Now().UnixNano())
		select {
		case queue <- data:
		case <-cctx.Done():
			return
		}
	}
}

// handle processes one envelope and reports whether the socket stays usable.
func (c *Client) handle(ctx context.Context, conn *websocket.Conn, data []byte, bot string, receive func(context.Context, host.Input) error) bool {
	var env struct {
		Type       string          `json:"type"`
		EnvelopeID string          `json:"envelope_id"`
		Payload    json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(data, &env) != nil {
		return false
	}
	if env.Type == "disconnect" {
		conn.Close(websocket.StatusNormalClosure, "")
		return false
	}
	if env.Type == "events_api" {
		if stop, ok := ParseStop(env.Payload); ok {
			if c.OnStop != nil {
				if err := c.OnStop(ctx, stop); err != nil {
					return false
				}
			}
		} else if in, ok := Parse(env.Payload, bot); ok {
			if err := receive(ctx, in); err != nil {
				return false
			}
		}
	}
	if env.EnvelopeID == "" {
		return true
	}
	ack, _ := json.Marshal(map[string]string{"envelope_id": env.EnvelopeID})
	wctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return conn.Write(wctx, websocket.MessageText, ack) == nil
}
