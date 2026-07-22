//go:build linux

package action

import (
	"context"
	stdio "io"
	"log"
	"testing"
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
