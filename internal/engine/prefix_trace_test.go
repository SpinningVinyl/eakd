// SPDX-License-Identifier: GPL-2.0-or-later

package engine

import (
	"reflect"
	"testing"
	"time"

	"eak/internal/input"
	"eak/internal/keycode"
)

// Checks prefix actions, replay, and exact deadlines for split/combined frames.
func TestPrefixTraceCompatibility(t *testing.T) {
	frame := func(code uint16, value int32) input.Frame { return keyFrame("kbd", code, value) }
	logoDown, logoUp := frame(keycode.KeyLeftMeta, 1), frame(keycode.KeyLeftMeta, 0)
	tDown, tUp := frame(keycode.KeyT, 1), frame(keycode.KeyT, 0)
	oneDown, oneUp := frame(keycode.Key1, 1), frame(keycode.Key1, 0)
	type step struct {
		frame        input.Frame
		at, deadline time.Duration
		want         Result
	}
	prefix := []step{
		{frame: logoDown, deadline: 500 * time.Millisecond},
		{frame: tDown, deadline: 500 * time.Millisecond},
		{frame: tUp, deadline: 750 * time.Millisecond},
		{frame: logoUp, deadline: 750 * time.Millisecond},
	}
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"success", append(append([]step(nil), prefix...),
			step{frame: oneDown, deadline: 750 * time.Millisecond},
			step{frame: oneUp, want: Result{Actions: []string{"terminal.one"}}})},
		// A key arriving exactly at the source deadline must replay the prefix.
		{"source deadline", []step{
			{frame: logoDown, deadline: 500 * time.Millisecond},
			{frame: tDown, at: 500 * time.Millisecond, want: Result{Forward: []input.Frame{logoDown, tDown}}},
			{frame: tUp, at: 500 * time.Millisecond, want: Result{Forward: []input.Frame{tUp}}},
			{frame: logoUp, at: 500 * time.Millisecond, want: Result{Forward: []input.Frame{logoUp}}},
		}},
		// Starting a binding just before expiry must not extend its deadline.
		{"binding deadline", append(append([]step(nil), prefix...),
			step{frame: oneDown, at: 749 * time.Millisecond, deadline: 750 * time.Millisecond},
			step{frame: oneUp, at: 750 * time.Millisecond, want: Result{Forward: []input.Frame{oneDown, oneUp}}})},
		// A binding cannot start at the sequence deadline; its keys pass through.
		{"sequence deadline", append(append([]step(nil), prefix...),
			step{frame: oneDown, at: 750 * time.Millisecond, want: Result{Forward: []input.Frame{oneDown}}},
			step{frame: oneUp, at: 750 * time.Millisecond, want: Result{Forward: []input.Frame{oneUp}}})},
		{"combined source press", []step{
			{frame: input.Frame{Device: "kbd", Events: []input.Event{logoDown.Events[0], tDown.Events[0], tDown.Events[1]}}, deadline: 500 * time.Millisecond},
			{frame: tUp, deadline: 750 * time.Millisecond},
			{frame: logoUp, deadline: 750 * time.Millisecond},
			{frame: oneDown, deadline: 750 * time.Millisecond},
			{frame: oneUp, want: Result{Actions: []string{"terminal.one"}}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(testConfig())
			start := time.Unix(1, 0)
			for i, s := range tc.steps {
				if got := e.HandleFrame(s.frame, start.Add(s.at)); !reflect.DeepEqual(got, s.want) {
					t.Fatalf("step %d: got %+v, want %+v", i, got, s.want)
				}
				deadline, pending := e.Deadline()
				if pending != (s.deadline != 0) || pending && !deadline.Equal(start.Add(s.deadline)) {
					t.Fatalf("step %d: deadline=%v pending=%t, want offset %v", i, deadline, pending, s.deadline)
				}
			}
			if len(e.logicalCount) != 0 {
				t.Fatalf("physical keys remain: %v", e.logicalCount)
			}
		})
	}
}
