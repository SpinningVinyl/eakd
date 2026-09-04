// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package action

import (
	"bufio"
	"context"
	"errors"
	stdio "io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestServerPublishesToAuthenticatedClientAndShutsDownCleanly(t *testing.T) {
	server, path, stop := startTestServer(t, []uint32{uint32(os.Geteuid())})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o666 {
		t.Fatalf("socket permissions = %04o; want 0666", permissions)
	}

	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	waitForClientCount(t, server, 1)

	server.Publish("terminal.one")
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "{\"action\":\"terminal.one\"}\n" {
		t.Fatalf("notification = %q", line)
	}

	stop()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket exists after shutdown: %v", err)
	}
}

func TestServerRejectsUnauthorizedUID(t *testing.T) {
	unauthorized := uint32(os.Geteuid()) ^ 1
	_, path, _ := startTestServer(t, []uint32{unauthorized})
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Read(make([]byte, 1)); err == nil {
		t.Fatal("unauthorized connection remained open")
	}
}

func TestServerRejectsDuplicateUIDWithoutDisruptingFirstClient(t *testing.T) {
	server, path, _ := startTestServer(t, []uint32{uint32(os.Geteuid())})
	first, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	waitForClientCount(t, server, 1)

	duplicate, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Close()
	if err := duplicate.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := duplicate.Read(make([]byte, 1)); err == nil {
		t.Fatal("duplicate connection remained open")
	}

	server.Publish("still-connected")
	if err := first.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(first).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "{\"action\":\"still-connected\"}\n" {
		t.Fatalf("notification = %q", line)
	}
}

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

func TestPublishDropsClientWithFullQueue(t *testing.T) {
	connection := &stubClientConnection{}
	server := NewServer("/unused", []uint32{1000}, log.New(stdio.Discard, "", 0))
	connected := &client{uid: 1000, connection: connection, queue: make(chan []byte, 1)}
	connected.queue <- []byte("already queued")
	if !server.addClient(connected) {
		t.Fatal("client was rejected")
	}

	server.Publish("overflow")
	if !connection.closed {
		t.Fatal("overflowing client connection was not closed")
	}
	if len(server.clients) != 0 {
		t.Fatal("overflowing client remained registered")
	}
}

func TestWriteFailureReleasesClientUID(t *testing.T) {
	wanted := errors.New("write failed")
	connection := &stubClientConnection{writeErr: wanted}
	server := NewServer("/unused", []uint32{1000}, log.New(stdio.Discard, "", 0))
	connected := &client{uid: 1000, connection: connection, queue: make(chan []byte, 1)}
	connected.queue <- []byte("notification")
	if !server.addClient(connected) {
		t.Fatal("client was rejected")
	}

	server.writeClient(connected)
	if !connection.closed || len(server.clients) != 0 {
		t.Fatalf("failed client closed=%t clients=%d", connection.closed, len(server.clients))
	}
	if len(connection.writes) != 1 || string(connection.writes[0]) != "notification" {
		t.Fatalf("writes = %q", connection.writes)
	}
}

func TestCloseClientsClosesAndForgetsEveryClient(t *testing.T) {
	server := NewServer("/unused", []uint32{1000, 1001}, log.New(stdio.Discard, "", 0))
	connections := []*stubClientConnection{{}, {}}
	for index, uid := range []uint32{1000, 1001} {
		if !server.addClient(&client{
			uid: uid, connection: connections[index], queue: make(chan []byte, 1),
		}) {
			t.Fatalf("client %d was rejected", uid)
		}
	}

	server.closeClients()
	if len(server.clients) != 0 {
		t.Fatalf("%d clients remained registered", len(server.clients))
	}
	for index, connection := range connections {
		if !connection.closed {
			t.Errorf("connection %d was not closed", index)
		}
	}
}

func TestRemoveSocketAcceptsMissingPathAndRefusesRegularFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sock")
	if err := removeSocket(missing); err != nil {
		t.Fatalf("missing socket: %v", err)
	}
	regular := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regular, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeSocket(regular); err == nil || !strings.Contains(err.Error(), "non-socket") {
		t.Fatalf("removeSocket(regular file) = %v", err)
	}
}

type stubClientConnection struct {
	writes   [][]byte
	writeErr error
	closed   bool
}

func (c *stubClientConnection) Read([]byte) (int, error) { return 0, stdio.EOF }

func (c *stubClientConnection) Write(data []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), data...))
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return len(data), nil
}

func (c *stubClientConnection) Close() error {
	c.closed = true
	return nil
}

func startTestServer(t *testing.T, allowed []uint32) (*Server, string, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "eakd.sock")
	server := NewServer(path, allowed, log.New(stdio.Discard, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	select {
	case <-server.Ready():
	case err := <-done:
		t.Fatalf("server stopped before readiness: %v", err)
	case <-time.After(time.Second):
		t.Fatal("server did not become ready")
	}

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("server shutdown: %v", err)
				}
			case <-time.After(time.Second):
				t.Error("server did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return server, path, stop
}

func waitForClientCount(t *testing.T, server *Server, wanted int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		count := len(server.clients)
		server.mu.Unlock()
		if count == wanted {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server has %d clients; want %d", count, wanted)
		}
		time.Sleep(time.Millisecond)
	}
}
