//go:build linux

package action

import (
	"context"
	stdio "io"
	"log"
	"net"
	"testing"
	"time"
)

func TestReadNotifications(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	actions := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() { errCh <- readNotifications(context.Background(), client, actions) }()
	if _, err := server.Write([]byte("{\"action\":\"terminal.one\"}\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-actions:
		if got != "terminal.one" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("notification was not delivered")
	}
}

func TestReadNotificationsRejectsEmptyAction(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	actions := make(chan string)
	errCh := make(chan error, 1)
	go func() { errCh <- readNotifications(context.Background(), client, actions) }()
	if _, err := server.Write([]byte("{\"action\":\"\"}\n")); err != nil {
		t.Fatal(err)
	}
	_ = server.Close()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("empty action was accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not return")
	}
}

func TestClientCancellationClosesEstablishedConnection(t *testing.T) {
	server, connection := net.Pipe()
	defer server.Close()
	connected := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	client := NewClient("/unused", log.New(stdio.Discard, "", 0))
	client.dial = func(context.Context, string, string) (net.Conn, error) {
		close(connected)
		return connection, nil
	}
	go func() { done <- client.Run(ctx, make(chan string)) }()
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("client did not connect")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not stop after cancellation")
	}
}
