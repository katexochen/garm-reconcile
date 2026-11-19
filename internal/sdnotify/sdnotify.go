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

// Package sdnotify reports the state of the process to systemd.
package sdnotify

import (
	"net"
	"os"
)

// Ready tells systemd that the service finished starting up.
func Ready() error {
	return notify("READY=1")
}

// Status sets the status line systemd shows for the service.
func Status(status string) error {
	return notify("STATUS=" + status)
}

// notify sends a notification as described in sd_notify(3). It does nothing
// if the process was not started by systemd as a service that sends them.
func notify(state string) error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}
