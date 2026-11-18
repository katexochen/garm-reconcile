package garm

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWaitReady(t *testing.T) {
	// Reserve an address, but only start serving on it after the client
	// began waiting.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	client, err := NewClient("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	client.retryInterval = 10 * time.Millisecond

	go func() {
		time.Sleep(50 * time.Millisecond)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		// An error response is enough: the API answers.
		_ = http.Serve(listener, http.NotFoundHandler())
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWaitReadyGivesUp(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	client, err := NewClient("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	client.retryInterval = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = client.WaitReady(ctx)
	if err == nil || !strings.Contains(err.Error(), "waiting for garm") {
		t.Errorf("WaitReady() = %v, want it to give up", err)
	}
}

func TestWaitReadyIgnoresServerErrors(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests < 3 {
			http.Error(w, "upstream is starting", http.StatusBadGateway)
			return
		}
		http.Error(w, "not json", http.StatusConflict)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.retryInterval = time.Millisecond
	if err := client.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Errorf("WaitReady() returned after %d requests, want 3", requests)
	}

	// An error without the body garm sends is described by its status.
	err = client.do(context.Background(), http.MethodGet, nil, nil, "controller-info")
	if err == nil || err.Error() != "garm: Conflict (HTTP 409)" {
		t.Errorf("error = %v, want the status text", err)
	}
}
