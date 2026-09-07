// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package engine

import (
	"testing"
	"time"

	"eak/internal/keycode"
)

// Run with go test ./internal/engine -run '^$' -bench . -benchmem.
// Timings cover engine processing only, not forwarding or device I/O.

// One operation is an ordinary key's complete press/release pair.
func BenchmarkOrdinaryKey(b *testing.B) {
	e := New(heldConfig())
	down, up := keyFrame("kbd", keycode.KeyA, 1), keyFrame("kbd", keycode.KeyA, 0)
	now := time.Unix(1, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.HandleFrame(down, now)
		e.HandleFrame(up, now)
	}
}

// One operation activates and releases Insert while the source Win stays held.
func BenchmarkHeldRemap(b *testing.B) {
	e := New(heldConfig())
	now := time.Unix(1, 0)
	e.HandleFrame(keyFrame("kbd", keycode.KeyLeftMeta, 1), now)
	down, up := keyFrame("kbd", keycode.KeyHome, 1), keyFrame("kbd", keycode.KeyHome, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.HandleFrame(down, now)
		e.HandleFrame(up, now)
	}
}
