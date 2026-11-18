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
