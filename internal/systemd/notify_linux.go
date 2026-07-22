//go:build linux

package systemd

import (
	"fmt"
	"net"
	"os"
)

// Ready implements the systemd notification protocol without requiring a
// libsystemd dependency. NOTIFY_SOCKET may name a filesystem or Linux abstract
// Unix socket. Running eakd outside systemd makes this function a no-op.
func Ready() error {
	return ready(os.Getenv("NOTIFY_SOCKET"), sendDatagram)
}

func ready(path string, send func(string, []byte) error) error {
	if path == "" {
		return nil
	}
	if err := send(path, []byte("READY=1")); err != nil {
		return fmt.Errorf("notify systemd of readiness: %w", err)
	}
	return nil
}

func sendDatagram(path string, message []byte) error {
	connection, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer connection.Close()
	_, err = connection.Write(message)
	return err
}
