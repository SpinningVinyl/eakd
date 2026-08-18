//go:build linux

package action

import (
	"context"
	"errors"
	stdio "io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServerDoesNotBecomeReadyWhenStartupValidationFails(t *testing.T) {
	server := NewServer("/unused", nil, log.New(stdio.Discard, "", 0))
	if err := server.Serve(context.Background()); err == nil {
		t.Fatal("Serve succeeded without an allowed UID")
	}
	select {
	case <-server.Ready():
		t.Fatal("server became ready after startup failure")
	default:
	}
}

func TestRemoveSocketRefusesActiveAndRemovesStaleSockets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eakd.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)

	if err := removeSocket(path); err == nil || !strings.Contains(err.Error(), "active socket") {
		t.Fatalf("removeSocket(active) = %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := removeSocket(path); err != nil {
		t.Fatalf("removeSocket(stale) = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("socket still exists after removal: %v", err)
	}
}

func TestServerAcceptsOneClientPerUID(t *testing.T) {
	server := NewServer("/unused", []uint32{1000, 1001}, log.New(stdio.Discard, "", 0))
	first := &client{uid: 1000}
	duplicate := &client{uid: 1000}
	other := &client{uid: 1001}

	if !server.addClient(first) {
		t.Fatal("first client for uid 1000 was rejected")
	}
	if server.addClient(duplicate) {
		t.Fatal("second client for uid 1000 was accepted")
	}
	if !server.addClient(other) {
		t.Fatal("first client for uid 1001 was rejected")
	}
}

func TestClientDisconnectReleasesUID(t *testing.T) {
	connection, peer := net.Pipe()
	server := NewServer("/unused", []uint32{1000}, log.New(stdio.Discard, "", 0))
	connected := &client{uid: 1000, connection: connection, queue: make(chan []byte, 16)}
	if !server.addClient(connected) {
		t.Fatal("first client was rejected")
	}
	watchDone := make(chan struct{})
	go func() {
		server.watchClient(connected)
		close(watchDone)
	}()

	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-watchDone:
	case <-time.After(time.Second):
		t.Fatal("client disconnect was not detected")
	}
	if !server.addClient(&client{uid: 1000}) {
		t.Fatal("UID remained occupied after client disconnect")
	}
}
