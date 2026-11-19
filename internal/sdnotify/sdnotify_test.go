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

package sdnotify

import (
	"crypto/rand"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestNotifications(t *testing.T) {
	// An abstract socket works where the path of the temporary directory
	// is too long for a socket address.
	socket := "@garm-reconcile-" + rand.Text()
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	t.Setenv("NOTIFY_SOCKET", socket)

	receive := func() string {
		t.Helper()
		buf := make([]byte, 256)
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf[:n])
	}

	if err := Ready(); err != nil {
		t.Fatal(err)
	}
	if got := receive(); got != "READY=1" {
		t.Errorf("Ready() sent %q, want READY=1", got)
	}
	if err := Status("waiting"); err != nil {
		t.Fatal(err)
	}
	if got := receive(); got != "STATUS=waiting" {
		t.Errorf("Status() sent %q, want STATUS=waiting", got)
	}
}

func TestNotifyOutsideSystemd(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := Ready(); err != nil {
		t.Errorf("Ready() without a notification socket: %v", err)
	}
}

func TestNotifyUnreachableSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "missing"))
	if err := Ready(); err == nil {
		t.Error("Ready() succeeded without anyone listening on the socket")
	}
}
