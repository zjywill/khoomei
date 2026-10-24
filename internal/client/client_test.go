package client

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The first connection is cut before any response; the retry must succeed.
func TestRetriesOnceOnConnectionDrop(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"version":"t"}`))
	}))
	srv.Start()
	defer srv.Close()

	c := New(srv.URL, nil)
	resp, err := c.Health(context.Background())
	if err != nil || !resp.OK {
		t.Fatalf("health after drop: %v %+v", err, resp)
	}
	if hits.Load() != 2 {
		t.Fatalf("want exactly one retry, got %d attempts", hits.Load())
	}
}

func TestNoRetryOnTimeout(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(3 * time.Second)
	}))
	defer srv.Close()
	c := New(srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := c.Health(ctx); err == nil {
		t.Fatal("expected timeout")
	}
	if hits.Load() != 1 {
		t.Fatalf("timeouts must not retry, got %d attempts", hits.Load())
	}
}

func TestTransientClassification(t *testing.T) {
	if !transient(&net.OpError{Op: "dial", Err: errConnRefused{}}) {
		t.Error("dial OpError should be transient")
	}
}

type errConnRefused struct{}

func (errConnRefused) Error() string { return "connection refused" }

// The default transport must route through HTTP_PROXY: Muse's sandbox allows the tailnet only that way.
func TestDefaultTransportHonoursProxyEnv(t *testing.T) {
	proxied := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied <- r.Host
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	c := New("http://100.118.108.64:7767", nil)
	resp, err := c.Health(context.Background())
	if err != nil || !resp.OK {
		t.Fatalf("via proxy: %v %+v", err, resp)
	}
	if got := <-proxied; got != "100.118.108.64:7767" {
		t.Fatalf("proxy saw host %q", got)
	}
}
