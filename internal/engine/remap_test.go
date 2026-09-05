// SPDX-License-Identifier: GPL-2.0-or-later

package engine

import (
	"eak/internal/config"
	"eak/internal/input"
	"eak/internal/keycode"
	"testing"
	"time"
)

func remapEngine() *Engine {
	cfg := testConfig()
	cfg.Prefixes = append(cfg.Prefixes, config.Prefix{Keys: []keycode.Logical{keycode.LogicalLogo, keycode.Logical(keycode.KeyHome)}, Tap: keycode.KeyInsert})
	return New(cfg)
}

func TestRemapTapAndModifierSuppression(t *testing.T) {
	for _, modifier := range []uint16{keycode.KeyLeftMeta, keycode.KeyRightMeta} {
		e := remapEngine()
		now := time.Unix(1, 0)
		for _, event := range []struct {
			code  uint16
			value int32
		}{{modifier, 1}, {keycode.KeyHome, 1}, {keycode.KeyHome, 2}} {
			r := e.HandleFrame(keyFrame("kbd", event.code, event.value), now)
			if len(r.Forward) != 0 || len(r.Actions) != 0 {
				t.Fatalf("premature output: %+v", r)
			}
		}
		r := e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)
		if len(r.Forward) != 2 || len(r.Actions) != 0 {
			t.Fatalf("tap: %+v", r)
		}
		for i, value := range []int32{1, 0} {
			ev := r.Forward[i].Events[0]
			if ev.Type != input.EVKey || ev.Code != keycode.KeyInsert || ev.Value != value {
				t.Fatalf("tap event: %+v", ev)
			}
		}
		for repeat := 0; repeat < 3; repeat++ {
			if r := e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 1), now); len(r.Forward) != 0 {
				t.Fatal("repeated Home press leaked")
			}
			r := e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)
			if len(r.Forward) != 2 || r.Forward[0].Events[0].Code != keycode.KeyInsert {
				t.Fatalf("repeated tap: %+v", r)
			}
		}
		if r := e.HandleFrame(keyFrame("kbd", keycode.KeyA, 1), now); len(r.Forward) != 1 {
			t.Fatal("unrelated key suppressed")
		}
		if r := e.HandleFrame(keyFrame("kbd", modifier, 0), now); len(r.Forward) != 0 {
			t.Fatal("modifier release leaked")
		}
		if _, pending := e.Deadline(); pending {
			t.Fatal("remap left a deadline")
		}
	}
}

func TestRemapFailureAndTimeoutReplay(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		e := remapEngine()
		now := time.Unix(1, 0)
		e.HandleFrame(keyFrame("kbd", keycode.KeyLeftMeta, 1), now)
		var r Result
		if timeout {
			r = e.HandleTimeout(now.Add(time.Second))
		} else {
			r = e.HandleFrame(keyFrame("kbd", keycode.KeyA, 1), now)
		}
		if len(r.Forward) == 0 || r.Forward[0].Events[0].Code != keycode.KeyLeftMeta {
			t.Fatalf("missing replay: %+v", r)
		}
	}
}

func TestRemapResyncKeepsConsumedModifierHidden(t *testing.T) {
	e := remapEngine()
	now := time.Unix(1, 0)
	e.HandleFrame(keyFrame("kbd", keycode.KeyLeftMeta, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)
	pressed := map[uint16]bool{keycode.KeyLeftMeta: true, keycode.KeyA: true}
	e.Resync("kbd", pressed)
	visible := e.ForwardPressed("kbd", pressed)
	if visible[keycode.KeyLeftMeta] || !visible[keycode.KeyA] {
		t.Fatalf("visible keys: %v", visible)
	}
	e.Resync("kbd", nil)
	e.ForwardPressed("kbd", nil)
	if len(e.suppressed["kbd"]) != 0 {
		t.Fatal("removed device retained suppression")
	}
}

func TestRepeatedRemapModifierReleasedFirst(t *testing.T) {
	e := remapEngine()
	now := time.Unix(1, 0)
	e.HandleFrame(keyFrame("kbd", keycode.KeyLeftMeta, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 1), now)
	r := e.HandleFrame(keyFrame("kbd", keycode.KeyLeftMeta, 0), now)
	if len(r.Forward) != 0 {
		t.Fatalf("modifier release leaked: %+v", r)
	}
	r = e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)
	if len(r.Forward) != 2 || r.Forward[0].Events[0].Code != keycode.KeyInsert {
		t.Fatalf("missing tap: %+v", r)
	}
	if len(e.suppressed["kbd"]) != 0 {
		t.Fatal("released modifier remains suppressed")
	}
}

func TestRepeatedRemapModifierReleasedInPressFrame(t *testing.T) {
	e := remapEngine()
	now := time.Unix(1, 0)
	e.HandleFrame(keyFrame("kbd", keycode.KeyLeftMeta, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)

	// Home is pressed before the consumed modifier is released in one frame.
	r := e.HandleFrame(input.Frame{Device: "kbd", Events: []input.Event{
		{Type: input.EVKey, Code: keycode.KeyHome, Value: 1},
		{Type: input.EVKey, Code: keycode.KeyLeftMeta, Value: 0},
		{Type: input.EVSyn, Code: input.SynReport},
	}}, now)
	if len(r.Forward) != 0 || len(r.Actions) != 0 {
		t.Fatalf("source frame leaked: %+v", r)
	}
	r = e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)
	if len(r.Forward) != 2 || len(r.Actions) != 0 {
		t.Fatalf("expected Insert tap: %+v", r)
	}
	for i, value := range []int32{1, 0} {
		ev := r.Forward[i].Events[0]
		if ev.Type != input.EVKey || ev.Code != keycode.KeyInsert || ev.Value != value {
			t.Fatalf("unexpected tap event: %+v", ev)
		}
	}
}
