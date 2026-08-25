// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package systemd

import (
	"errors"
	"testing"
)

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
