package slack

import (
	"context"
	"slices"
	"time"

	"golem/internal/host"
)

const refuseEvery = time.Hour

// Refuse tells a person outside the allowlist, who addressed the bot directly,
// that it does not take their requests. It is a fixed text sent by the channel
// adapter with no model involved: never to bots, never for ambient messages,
// and at most once per person per hour, which also absorbs Slack delivering a
// mention twice. An empty text turns it off. It reports whether it replied.
func (c *Client) Refuse(ctx context.Context, in host.Input, allowed []string, text string) bool {
	if text == "" || !in.Explicit || in.Bot || in.Sender == "" || slices.Contains(allowed, in.Sender) {
		return false
	}
	now := c.now()
	c.refusedMu.Lock()
	if last, ok := c.refused[in.Sender]; ok && now.Sub(last) < refuseEvery {
		c.refusedMu.Unlock()
		return false
	}
	if c.refused == nil {
		c.refused = map[string]time.Time{}
	}
	c.refused[in.Sender] = now
	c.refusedMu.Unlock()
	if err := c.Send(ctx, in.Thread, text); err != nil {
		c.Logger.Warn("Slack refusal not delivered", "user", in.Sender)
		return false
	}
	c.Logger.Info("request refused", "source", "slack", "user", in.Sender, "thread", in.Thread.ID)
	return true
}
