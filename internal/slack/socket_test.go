package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"golem/internal/host"
)

// fakeSlack answers auth.test and apps.connections.open and serves one socket
// that sends a single events_api envelope; acks reports each acknowledgement.
func fakeSlack(t *testing.T) (*Client, chan string) {
	t.Helper()
	acks := make(chan string, 4)
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"user_id":"SELF"}`))
	})
	mux.HandleFunc("/apps.connections.open", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer app" {
			t.Errorf("app token missing")
		}
		w.Write([]byte(`{"ok":true,"url":"ws` + strings.TrimPrefix(srv.URL, "http") + `/socket"}`))
	})
	mux.HandleFunc("/socket", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		env := `{"type":"events_api","envelope_id":"E1","payload":{"event":{"type":"message","user":"U1","channel":"C1","ts":"12","text":"hello"}}}`
		if err := conn.Write(ctx, websocket.MessageText, []byte(env)); err != nil {
			return
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var ack struct {
			EnvelopeID string `json:"envelope_id"`
		}
		json.Unmarshal(data, &ack)
		acks <- ack.EnvelopeID
		conn.Read(ctx)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("bot", "app")
	c.API = srv.URL
	return c, acks
}

func TestRunAcknowledgesAfterAdmission(t *testing.T) {
	c, acks := fakeSlack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var returned time.Time
	var got host.Input
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, func(_ context.Context, in host.Input) error {
			got = in
			time.Sleep(100 * time.Millisecond)
			returned = time.Now()
			return nil
		})
	}()
	select {
	case id := <-acks:
		at := time.Now()
		if id != "E1" {
			t.Fatalf("ack %q", id)
		}
		if at.Before(returned) || returned.IsZero() {
			t.Fatal("acknowledged before admission finished")
		}
		if got.Key != "slack:C1:12" || got.Text != "hello" {
			t.Fatalf("input %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no acknowledgement")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunDoesNotAcknowledgeFailedAdmission(t *testing.T) {
	c, acks := fakeSlack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, func(context.Context, host.Input) error {
			select {
			case called <- struct{}{}:
			default:
			}
			return context.DeadlineExceeded
		})
	}()
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("receive not called")
	}
	select {
	case id := <-acks:
		t.Fatalf("acknowledged %q after failed admission", id)
	case <-time.After(500 * time.Millisecond):
	}
	cancel()
	<-done
}

func TestRunFailsWithoutBotIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer srv.Close()
	c := New("bot", "app")
	c.API = srv.URL
	err := c.Run(context.Background(), nil)
	if err == nil || err.Error() != "Slack bot identity is missing." {
		t.Fatalf("got %v", err)
	}
}
