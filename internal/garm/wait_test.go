// Copyright 2026 Paul Meyer
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

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
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

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
