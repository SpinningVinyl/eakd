// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package action

import (
	"context"
	"errors"
	stdio "io"
	"log"
	"net"
	"strings"
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

func TestReadNotificationsRejectsMalformedAndOversizedMessages(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "malformed JSON", payload: "{not-json}\n"},
		{name: "truncated JSON", payload: "{\"action\":"},
		{name: "oversized", payload: strings.Repeat("x", maximumMessageSize+1) + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := readNotificationPayload(t, test.payload, make(chan string, 1))
			if err == nil {
				t.Fatalf("accepted payload of %d bytes", len(test.payload))
			}
		})
	}
}

func TestReadNotificationsCancellationUnblocksDelivery(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- readNotifications(ctx, client, make(chan string))
		client.Close()
	}()
	if _, err := server.Write([]byte("{\"action\":\"blocked\"}\n")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled reader returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader remained blocked on action delivery")
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

func TestClientDialFailureStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient("/unused", log.New(stdio.Discard, "", 0))
	wanted := errors.New("dial failed")
	calls := 0
	client.dial = func(context.Context, string, string) (net.Conn, error) {
		calls++
		cancel()
		return nil, wanted
	}

	if err := client.Run(ctx, make(chan string)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("dial calls = %d; want 1", calls)
	}
}

func TestClientReconnectsAfterEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewClient("/unused", log.New(stdio.Discard, "", 0))
	calls := 0
	client.dial = func(context.Context, string, string) (net.Conn, error) {
		calls++
		if calls == 1 {
			connection, peer := net.Pipe()
			peer.Close()
			return connection, nil
		}
		cancel()
		return nil, errors.New("stop reconnect test")
	}

	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, make(chan string)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not reconnect after EOF")
	}
	if calls != 2 {
		t.Fatalf("dial calls = %d; want 2", calls)
	}
}

func TestWaitForRetry(t *testing.T) {
	if !waitForRetry(context.Background(), time.Millisecond) {
		t.Fatal("retry timer did not fire")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForRetry(ctx, time.Hour) {
		t.Fatal("cancelled retry reported that its timer fired")
	}
}

func readNotificationPayload(t *testing.T, payload string, actions chan<- string) error {
	t.Helper()
	server, client := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- readNotifications(context.Background(), client, actions)
		client.Close()
	}()
	_, _ = server.Write([]byte(payload))
	server.Close()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("notification reader did not stop")
		return nil
	}
}
