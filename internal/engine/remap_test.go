// SPDX-License-Identifier: GPL-2.0-or-later

package engine

import (
	"eak/internal/config"
	"eak/internal/input"
	"eak/internal/keycode"
	"slices"
	"testing"
	"time"
)

func TestSuppressedModifierRepressFollowup(t *testing.T) {
	for _, outcome := range []string{"timeout", "failure", "remap"} {
		t.Run(outcome, func(t *testing.T) {
			e := remapEngine()
			now := time.Unix(1, 0)
			e.HandleFrame(keyFrame("kbd", keycode.KeyLeftMeta, 1), now)
			e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 1), now)
			e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)
			frame := input.Frame{Device: "kbd", Events: []input.Event{
				{Type: input.EVMsc, Code: input.MscScan, Value: 125},
				{Type: input.EVKey, Code: keycode.KeyLeftMeta, Value: 0},
				{Type: input.EVMsc, Code: input.MscScan, Value: 125},
				{Type: input.EVKey, Code: keycode.KeyLeftMeta, Value: 1},
				{Type: input.EVSyn, Code: input.SynReport},
			}}
			original := frame.Clone()
			r := e.HandleFrame(frame, now)
			if len(r.Forward) != 0 || len(r.Actions) != 0 || !slices.Equal(frame.Events, original.Events) {
				t.Fatalf("repress emitted output or mutated input: %+v", r)
			}
			var want []input.Frame
			switch outcome {
			case "timeout":
				r = e.HandleTimeout(now.Add(time.Second))
				want = []input.Frame{keyFrame("kbd", keycode.KeyLeftMeta, 1)}
			case "failure":
				r = e.HandleFrame(keyFrame("kbd", keycode.KeyA, 1), now)
				want = []input.Frame{keyFrame("kbd", keycode.KeyLeftMeta, 1), keyFrame("kbd", keycode.KeyA, 1)}
			case "remap":
				if r = e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 1), now); len(r.Forward) != 0 {
					t.Fatalf("Home press leaked: %+v", r)
				}
				r = e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)
				want = []input.Frame{keyFrame("eakd-remap", keycode.KeyInsert, 1), keyFrame("eakd-remap", keycode.KeyInsert, 0)}
			}
			if len(r.Actions) != 0 || len(r.Forward) != len(want) {
				t.Fatalf("unexpected output: %+v", r)
			}
			for i := range want {
				if r.Forward[i].Device != want[i].Device || !slices.Equal(r.Forward[i].Events, want[i].Events) {
					t.Fatalf("frame %d: got %+v, want %+v", i, r.Forward[i], want[i])
				}
			}
			r = e.HandleFrame(keyFrame("kbd", keycode.KeyLeftMeta, 0), now.Add(time.Second))
			if outcome == "remap" && len(r.Forward) != 0 {
				t.Fatalf("consumed repress release leaked: %+v", r)
			}
			if outcome != "remap" && (len(r.Forward) != 1 || !slices.Equal(r.Forward[0].Events, keyFrame("kbd", keycode.KeyLeftMeta, 0).Events)) {
				t.Fatalf("forwarded repress release missing: %+v", r)
			}
		})
	}
}

func TestSuppressionPreservesNonMatchingEvents(t *testing.T) {
	e := remapEngine()
	now := time.Unix(1, 0)
	e.HandleFrame(keyFrame("kbd", keycode.KeyLeftMeta, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)
	frame := input.Frame{Device: "kbd", Events: []input.Event{
		{Type: input.EVKey, Code: keycode.KeyA, Value: 1},
		{Type: input.EVKey, Code: keycode.KeyA, Value: 1}, // Duplicate press.
		{Type: input.EVKey, Code: keycode.KeyA, Value: 2}, // Repeat.
		{Type: input.EVKey, Code: keycode.KeyB, Value: 0}, // Unmatched release.
		{Type: input.EVSyn, Code: input.SynReport},
	}}
	r := e.HandleFrame(frame, now)
	if len(r.Actions) != 0 || len(r.Forward) != 1 || !slices.Equal(r.Forward[0].Events, frame.Events) {
		t.Fatalf("non-matching events were filtered: %+v", r)
	}
}

func remapEngine() *testEngine {
	cfg := testConfig()
	cfg.Prefixes = append(cfg.Prefixes, config.Prefix{Keys: []keycode.Logical{keycode.LogicalLogo, keycode.Logical(keycode.KeyHome)}, Mode: config.Tap, Target: keycode.KeyInsert})
	return newTestEngine(cfg)
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
		var r observedResult
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
	r := e.Reconcile("kbd", pressed)
	visible := r.Output[len(r.Output)-1].Pressed
	if visible[keycode.KeyLeftMeta] || !visible[keycode.KeyA] {
		t.Fatalf("visible keys: %v", visible)
	}
	e.Reconcile("kbd", nil)
	if len(e.physical["kbd"]) != 0 || len(e.reuse) != 0 {
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
	if len(e.reuse) != 0 {
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

func TestSuppressedModifierRepressInSameFrameReplaysOnTimeout(t *testing.T) {
	e := remapEngine()
	now := time.Unix(1, 0)
	e.HandleFrame(keyFrame("kbd", keycode.KeyLeftMeta, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)

	r := e.HandleFrame(input.Frame{Device: "kbd", Events: []input.Event{
		{Type: input.EVKey, Code: keycode.KeyLeftMeta, Value: 0},
		{Type: input.EVKey, Code: keycode.KeyLeftMeta, Value: 1},
		{Type: input.EVSyn, Code: input.SynReport},
	}}, now)
	if len(r.Forward) != 0 || len(r.Actions) != 0 {
		t.Fatalf("new modifier candidate emitted premature output: %+v", r)
	}
	r = e.HandleTimeout(now.Add(time.Second))
	want := input.Event{Type: input.EVKey, Code: keycode.KeyLeftMeta, Value: 1}
	if len(r.Actions) != 0 || len(r.Forward) != 1 || len(r.Forward[0].Events) != 2 || r.Forward[0].Events[0] != want {
		t.Fatalf("expected only the new modifier press to replay: %+v", r)
	}
}

func TestRepeatedRemapModifierReleasedBeforePressInFrame(t *testing.T) {
	e := remapEngine()
	now := time.Unix(1, 0)
	e.HandleFrame(keyFrame("kbd", keycode.KeyLeftMeta, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 1), now)
	e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now)

	r := e.HandleFrame(input.Frame{Device: "kbd", Events: []input.Event{
		{Type: input.EVKey, Code: keycode.KeyLeftMeta, Value: 0},
		{Type: input.EVKey, Code: keycode.KeyHome, Value: 1},
		{Type: input.EVSyn, Code: input.SynReport},
	}}, now)
	r.Forward = append(r.Forward, e.HandleFrame(keyFrame("kbd", keycode.KeyHome, 0), now).Forward...)
	if len(r.Forward) != 2 || len(r.Actions) != 0 {
		t.Fatalf("expected ordinary Home press and release: %+v", r)
	}
	for i, value := range []int32{1, 0} {
		if events := r.Forward[i].Events; len(events) != 2 || events[0] != (input.Event{Type: input.EVKey, Code: keycode.KeyHome, Value: value}) {
			t.Fatalf("unexpected forwarded events: %+v", events)
		}
	}
	if _, pending := e.Deadline(); pending {
		t.Fatal("modifier release left a candidate pending")
	}
}
