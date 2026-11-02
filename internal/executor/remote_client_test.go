package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteKeepsStableJobIDsAcrossLostResponse(t *testing.T) {
	l := localForTest(t)
	h := Handler(l, "test-token")
	dropped := false
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && !dropped {
			dropped = true
			h.ServeHTTP(httptest.NewRecorder(), r)
			w.WriteHeader(503)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	r := NewRemote(proxy.URL+"/", "test-token")
	ctx := context.Background()
	spec := Spec{ID: "stable", Command: "printf x", TimeoutSeconds: 5}
	if _, err := r.Start(ctx, spec); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first start: %v", err)
	}
	j, err := r.Start(ctx, spec)
	if err != nil || j.Spec.ID != "stable" {
		t.Fatalf("second start: %+v %v", j, err)
	}
	awaitJob(t, r, "stable")
	c, err := r.Output(ctx, "stable", 0, 1)
	if err != nil || c.Output != "x" || c.Bytes != 1 {
		t.Fatalf("output: %+v %v", c, err)
	}
	if err := r.Stop(ctx, "stable"); err != nil {
		t.Fatal(err)
	}
	bad := NewRemote(proxy.URL, "wrong")
	if _, err := bad.Poll(ctx, "stable"); err == nil || !strings.Contains(err.Error(), "rejected") || errors.Is(err, ErrUnavailable) {
		t.Fatalf("wrong token: %v", err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer down.Close()
	if _, err := NewRemote(down.URL, "t").Poll(ctx, "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("503: %v", err)
	}
	down.Close()
	if _, err := NewRemote(down.URL, "t").Poll(ctx, "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("network: %v", err)
	}
}

func TestRemoteMapsExpiredOutput(t *testing.T) {
	l := localForTest(t)
	srv := httptest.NewServer(Handler(l, "t"))
	defer srv.Close()
	ctx := context.Background()
	r := NewRemote(srv.URL, "t")
	if _, err := r.Start(ctx, Spec{ID: "old", Command: "echo hi", TimeoutSeconds: 5}); err != nil {
		t.Fatal(err)
	}
	awaitJob(t, r, "old")
	if _, err := l.Sweep(time.Now().Add(48*time.Hour), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Output(ctx, "old", 0, 10); !errors.Is(err, ErrOutputExpired) {
		t.Fatalf("expired: %v", err)
	}
}
