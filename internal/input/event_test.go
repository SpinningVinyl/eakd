// SPDX-License-Identifier: GPL-2.0-or-later

package input

import (
	"slices"
	"testing"
)

func TestFrameCloneCopiesContentsAndOwnsEvents(t *testing.T) {
	original := Frame{
		Device: "kbd0",
		Events: []Event{
			{Type: EVKey, Code: 30, Value: 1},
			{Type: EVSyn, Code: SynReport},
		},
	}
	clone := original.Clone()

	if clone.Device != original.Device || !slices.Equal(clone.Events, original.Events) {
		t.Fatalf("clone = %#v; want %#v", clone, original)
	}
	clone.Device = "kbd1"
	clone.Events[0].Value = 0
	clone.Events = append(clone.Events, Event{Type: EVLed, Code: LEDCapsLock, Value: 1})
	if original.Device != "kbd0" {
		t.Fatalf("mutating clone changed original device to %q", original.Device)
	}
	if original.Events[0].Value != 1 || len(original.Events) != 2 {
		t.Fatalf("mutating clone changed original events to %#v", original.Events)
	}
}

func TestFrameCloneHandlesNoEvents(t *testing.T) {
	for _, events := range [][]Event{nil, {}} {
		original := Frame{Device: "kbd0", Events: events}
		clone := original.Clone()
		if clone.Device != original.Device || len(clone.Events) != 0 {
			t.Fatalf("clone = %#v; want empty frame for %q", clone, original.Device)
		}
	}
}
