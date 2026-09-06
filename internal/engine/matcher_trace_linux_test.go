// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package engine

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"eak/internal/input"
	"eak/internal/keycode"
	"eak/internal/linuxinput"
)

// Records compositor-visible events and rejects invalid press/release ordering.
type traceWriter struct {
	events []input.Event
	down   map[uint16]bool
}

func (w *traceWriter) Write(event input.Event) error {
	w.events = append(w.events, event)
	if event.Type == input.EVKey {
		switch event.Value {
		case 1:
			if w.down[event.Code] {
				return fmt.Errorf("duplicate virtual press: %d", event.Code)
			}
			w.down[event.Code] = true
		case 0:
			if !w.down[event.Code] {
				return fmt.Errorf("virtual release without press: %d", event.Code)
			}
			delete(w.down, event.Code)
		case 2:
			if !w.down[event.Code] {
				return fmt.Errorf("virtual repeat without press: %d", event.Code)
			}
		}
	}
	return nil
}

// Checks exact remap output through the forwarder and leaves no held keys,
// pending candidates, or stale suppression after each trace.
func TestMatcherForwarderTraces(t *testing.T) {
	key := func(code uint16, value int32) input.Event {
		return input.Event{Type: input.EVKey, Code: code, Value: value}
	}
	syn := input.Event{Type: input.EVSyn, Code: input.SynReport}
	framed := func(events ...input.Event) []input.Event { return append(events, syn) }
	tap := []input.Event{key(keycode.KeyInsert, 1), syn, key(keycode.KeyInsert, 0), syn}
	type step struct {
		device  string
		events  []input.Event
		timeout bool
		resync  bool
		pressed map[uint16]bool
		want    []input.Event
		action  string
	}
	press := func(code uint16, value int32, want []input.Event) step {
		return step{device: "kbd", events: framed(key(code, value)), want: want}
	}
	// Every trace starts after one successful remap with Left Win still held.
	setup := []step{press(keycode.KeyLeftMeta, 1, nil), press(keycode.KeyHome, 1, nil), press(keycode.KeyHome, 0, tap)}
	cases := []struct {
		name  string
		steps []step
	}{
		{"ordinary input and repeat", []step{
			press(keycode.KeyA, 1, framed(key(keycode.KeyA, 1))),
			press(keycode.KeyA, 2, framed(key(keycode.KeyA, 2))),
			press(keycode.KeyA, 0, framed(key(keycode.KeyA, 0))),
			press(keycode.KeyLeftMeta, 0, nil),
		}},
		{"repeat timeout", []step{
			press(keycode.KeyHome, 1, nil),
			{timeout: true, want: framed(key(keycode.KeyHome, 1))},
			press(keycode.KeyHome, 0, framed(key(keycode.KeyHome, 0))),
			press(keycode.KeyLeftMeta, 0, nil),
		}},
		{"repeat resync", []step{
			press(keycode.KeyHome, 1, nil),
			{device: "kbd", resync: true, pressed: map[uint16]bool{keycode.KeyLeftMeta: true, keycode.KeyHome: true}, want: framed(key(keycode.KeyHome, 1))},
			press(keycode.KeyHome, 0, framed(key(keycode.KeyHome, 0))),
			press(keycode.KeyLeftMeta, 0, nil),
		}},
		{"repeat removal", []step{
			press(keycode.KeyHome, 1, nil),
			{device: "kbd", resync: true},
		}},
		{"atomic extra key rejects completion", []step{
			press(keycode.KeyHome, 1, nil),
			{device: "kbd", events: framed(key(keycode.KeyHome, 0), key(keycode.KeyA, 1)), want: append(framed(key(keycode.KeyHome, 1)), framed(key(keycode.KeyHome, 0), key(keycode.KeyA, 1))...)},
			press(keycode.KeyA, 0, framed(key(keycode.KeyA, 0))),
			press(keycode.KeyLeftMeta, 0, nil),
		}},
		{"separate extra key follows completion", []step{
			press(keycode.KeyHome, 1, nil), press(keycode.KeyHome, 0, tap),
			press(keycode.KeyA, 1, framed(key(keycode.KeyA, 1))),
			press(keycode.KeyA, 0, framed(key(keycode.KeyA, 0))),
			press(keycode.KeyLeftMeta, 0, nil),
		}},
	}
	// A fresh Win press must replay or match normally, even after a hidden release.
	for _, combined := range []bool{false, true} {
		for _, outcome := range []string{"timeout", "remap", "prefix"} {
			steps := []step{press(keycode.KeyLeftMeta, 0, nil), press(keycode.KeyLeftMeta, 1, nil)}
			if combined {
				steps = []step{{device: "kbd", events: framed(
					input.Event{Type: input.EVMsc, Code: input.MscScan, Value: 125}, key(keycode.KeyLeftMeta, 0),
					input.Event{Type: input.EVMsc, Code: input.MscScan, Value: 125}, key(keycode.KeyLeftMeta, 1))}}
			}
			switch outcome {
			case "timeout":
				steps = append(steps, step{timeout: true, want: framed(key(keycode.KeyLeftMeta, 1))}, press(keycode.KeyLeftMeta, 0, framed(key(keycode.KeyLeftMeta, 0))))
			case "remap":
				steps = append(steps, press(keycode.KeyHome, 1, nil), press(keycode.KeyHome, 0, tap), press(keycode.KeyLeftMeta, 0, nil))
			case "prefix":
				steps = append(steps, press(keycode.KeyT, 1, nil), press(keycode.KeyT, 0, nil), press(keycode.KeyLeftMeta, 0, nil), press(keycode.Key1, 1, nil))
				last := press(keycode.Key1, 0, nil)
				last.action = "terminal.one"
				steps = append(steps, last)
			}
			cases = append(cases, struct {
				name  string
				steps []step
			}{fmt.Sprintf("repress/combined=%t/%s", combined, outcome), steps})
		}
	}
	// Reusing consumed Left Win must never swallow forwarded Right Win's release.
	for _, device := range []string{"kbd", "other"} {
		for _, rightFirst := range []bool{false, true} {
			rightDown := press(keycode.KeyRightMeta, 1, framed(key(keycode.KeyRightMeta, 1)))
			rightDown.device = device
			rightUp := press(keycode.KeyRightMeta, 0, framed(key(keycode.KeyRightMeta, 0)))
			rightUp.device = device
			releases := []step{press(keycode.KeyLeftMeta, 0, nil), rightUp}
			if rightFirst {
				slices.Reverse(releases)
			}
			steps := append([]step{rightDown, press(keycode.KeyHome, 1, nil), press(keycode.KeyHome, 0, tap)}, releases...)
			cases = append(cases, struct {
				name  string
				steps []step
			}{fmt.Sprintf("variants/device=%s/rightFirst=%t", device, rightFirst), steps})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := remapEngine()
			writer := &traceWriter{down: make(map[uint16]bool)}
			forwarder := linuxinput.NewForwarder(writer)
			now := time.Unix(1, 0)
			for i, s := range append(slices.Clone(setup), tc.steps...) {
				writer.events = nil
				var r Result
				switch {
				case s.timeout:
					now = now.Add(time.Second)
					r = e.HandleTimeout(now)
				case s.resync:
					e.Resync(s.device, s.pressed)
					if err := forwarder.Resync(s.device, e.ForwardPressed(s.device, s.pressed)); err != nil {
						t.Fatal(err)
					}
				default:
					frame := input.Frame{Device: s.device, Events: s.events}
					original := frame.Clone()
					r = e.HandleFrame(frame, now)
					if !slices.Equal(frame.Events, original.Events) {
						t.Fatalf("step %d mutated input", i)
					}
				}
				for _, frame := range r.Forward {
					if err := forwarder.Frame(frame); err != nil {
						t.Fatalf("step %d: %v", i, err)
					}
				}
				var actions []string
				if s.action != "" {
					actions = []string{s.action}
				}
				if !slices.Equal(writer.events, s.want) || !slices.Equal(r.Actions, actions) {
					t.Fatalf("step %d: events=%+v actions=%v; want events=%+v actions=%v", i, writer.events, r.Actions, s.want, actions)
				}
			}
			if len(writer.down) != 0 {
				t.Fatalf("virtual keys stuck: %v", writer.down)
			}
			if _, pending := e.Deadline(); pending {
				t.Fatal("deadline remains after trace")
			}
			if e.mode != modeIdle || len(e.buffer) != 0 || len(e.logicalCount) != 0 {
				t.Fatalf("matcher state remains: %+v", e)
			}
			for device, keys := range e.physical {
				if len(keys) != 0 {
					t.Fatalf("physical keys remain on %s: %v", device, keys)
				}
			}
			for device, keys := range e.suppressed {
				if len(keys) != 0 {
					t.Fatalf("suppression remains on %s: %v", device, keys)
				}
			}
		})
	}
}
