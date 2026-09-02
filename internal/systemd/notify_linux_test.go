// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package systemd

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadySendsDatagramToNotifySocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "notify.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", socketPath)

	if err := Ready(); err != nil {
		t.Fatal(err)
	}
	if err := listener.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	count, _, err := listener.ReadFromUnix(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buffer[:count]); got != "READY=1" {
		t.Fatalf("notification = %q; want READY=1", got)
	}
}

func TestReadyReportsNotifySocketError(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "missing.sock")
	t.Setenv("NOTIFY_SOCKET", socketPath)

	err := Ready()
	if err == nil {
		t.Fatal("notification to a missing socket succeeded")
	}
	if !strings.Contains(err.Error(), "notify systemd of readiness") {
		t.Fatalf("error %q lacks readiness context", err)
	}
}

func TestReadyNotifiesSystemd(t *testing.T) {
	var gotPath, gotMessage string
	err := ready("/run/systemd/notify", func(path string, message []byte) error {
		gotPath = path
		gotMessage = string(message)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/run/systemd/notify" || gotMessage != "READY=1" {
		t.Fatalf("notification path=%q message=%q", gotPath, gotMessage)
	}
}

func TestReadyOutsideSystemdIsNoop(t *testing.T) {
	called := false
	if err := ready("", func(string, []byte) error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("sent a readiness notification without NOTIFY_SOCKET")
	}
}

func TestReadyReportsNotificationFailure(t *testing.T) {
	wanted := errors.New("send failed")
	err := ready("@notify", func(string, []byte) error { return wanted })
	if !errors.Is(err, wanted) {
		t.Fatalf("got %v, want wrapped send error", err)
	}
}
