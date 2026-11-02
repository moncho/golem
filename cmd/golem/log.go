package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"golem/internal/watch"
)

// newLogger writes readable lines on a terminal and logfmt elsewhere, so a
// service manager's log file stays parseable.
func newLogger(stderr io.Writer) *slog.Logger {
	level := slog.LevelInfo
	if os.Getenv("GOLEM_DEBUG") != "" {
		level = slog.LevelDebug
	}
	if f, ok := stderr.(*os.File); ok && watch.IsTerminal(f) {
		return slog.New(newPrettyHandler(f, true).withLevel(level))
	}
	return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
}

// prettyHandler prints `HH:MM:SS message`, a level tag only for warnings and
// errors, short attributes inline and longer records as an aligned block.
type prettyHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	color bool
	level slog.Level
	attrs []slog.Attr
}

func newPrettyHandler(w io.Writer, color bool) *prettyHandler {
	return &prettyHandler{mu: &sync.Mutex{}, w: w, color: color, level: slog.LevelInfo}
}

func (h *prettyHandler) withLevel(level slog.Level) *prettyHandler {
	c := *h
	c.level = level
	return &c
}

func (h *prettyHandler) Enabled(_ context.Context, level slog.Level) bool { return level >= h.level }

func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &c
}

func (h *prettyHandler) WithGroup(string) slog.Handler { return h }

func (h *prettyHandler) paint(code int, s string) string {
	if !h.color {
		return s
	}
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[0m", code, s)
}

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := append([]slog.Attr{}, h.attrs...)
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a); return true })
	var b strings.Builder
	b.WriteString(h.paint(240, r.Time.Format("15:04:05")))
	switch {
	case r.Level >= slog.LevelError:
		b.WriteString(" " + h.paint(203, "ERROR"))
	case r.Level >= slog.LevelWarn:
		b.WriteString(" " + h.paint(214, "WARN "))
	case r.Level < slog.LevelInfo:
		b.WriteString(" " + h.paint(240, "DEBUG"))
	}
	b.WriteString(" " + r.Message)
	if len(attrs) <= 5 {
		for i, a := range attrs {
			sep := " "
			if i == 0 {
				sep = "  "
			}
			b.WriteString(sep + h.paint(240, a.Key+"="+a.Value.String()))
		}
		b.WriteString("\n")
	} else {
		b.WriteString("\n")
		keyWidth := 0
		for _, a := range attrs {
			keyWidth = max(keyWidth, len(a.Key))
		}
		for _, a := range attrs {
			b.WriteString(strings.Repeat(" ", 9) + h.paint(240, fmt.Sprintf("%-*s", keyWidth, a.Key)) + "  " + a.Value.String() + "\n")
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

// hostLog writes one log call from the host: its level, message and fields,
// in the order the host sent them.
func hostLog(logger *slog.Logger, raw json.RawMessage) error {
	var p struct {
		Level, Message string
		Fields         json.RawMessage
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	level := slog.LevelInfo
	switch p.Level {
	case "error":
		level = slog.LevelError
	case "warn":
		level = slog.LevelWarn
	case "debug":
		level = slog.LevelDebug
	}
	attrs, err := orderedAttrs(p.Fields)
	if err != nil {
		return err
	}
	logger.LogAttrs(context.Background(), level, p.Message, attrs...)
	return nil
}

// orderedAttrs decodes a JSON object keeping its key order, which a map would
// lose; numbers stay numbers.
func orderedAttrs(raw json.RawMessage) ([]slog.Attr, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("log fields must be an object")
	}
	var attrs []slog.Attr
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var value any
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		attrs = append(attrs, slog.Any(fmt.Sprint(key), value))
	}
	return attrs, nil
}
