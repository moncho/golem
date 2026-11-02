package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"golem/internal/host"
)

func TestSendMapsPermanentAndTransientErrors(t *testing.T) {
	var answer func(w http.ResponseWriter)
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("path %s", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		answer(w)
	}))
	defer srv.Close()
	c := New("bot", "app")
	c.API = srv.URL
	thread := host.Thread{ID: "slack:C1:1", Source: "slack", Channel: "C1", ReplyTo: "1"}

	answer = func(w http.ResponseWriter) { w.Write([]byte(`{"ok":true}`)) }
	if err := c.Send(context.Background(), thread, "hello"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer bot" || got["channel"] != "C1" || got["thread_ts"] != "1" || got["text"] != "hello" || got["unfurl_links"] != false || got["unfurl_media"] != false {
		t.Fatalf("request %v %q", got, auth)
	}

	answer = func(w http.ResponseWriter) { w.Write([]byte(`{"ok":false,"error":"is_archived"}`)) }
	if err := c.Send(context.Background(), thread, "hello"); !errors.Is(err, ErrUndeliverable) {
		t.Fatalf("is_archived: %v", err)
	}
	answer = func(w http.ResponseWriter) { w.Write([]byte(`{"ok":false,"error":"ratelimited"}`)) }
	if err := c.Send(context.Background(), thread, "hello"); err == nil || errors.Is(err, ErrUndeliverable) {
		t.Fatalf("ratelimited: %v", err)
	}
	answer = func(w http.ResponseWriter) { http.Error(w, "boom", 500) }
	if err := c.Send(context.Background(), thread, "hello"); err == nil || errors.Is(err, ErrUndeliverable) {
		t.Fatalf("http failure: %v", err)
	}
}
