//go:build linux

package linuxinput

import (
	"testing"

	"eak/internal/input"
	"eak/internal/keycode"
)

type recordingWriter struct {
	events []input.Event
}

func (w *recordingWriter) Write(event input.Event) error {
	w.events = append(w.events, event)
	return nil
}

func TestModifierReferenceCountingAcrossKeyboards(t *testing.T) {
	writer := &recordingWriter{}
	forwarder := NewForwarder(writer)
	frames := []input.Frame{
		modifierFrame("kbd0", 1),
		modifierFrame("kbd1", 1),
		modifierFrame("kbd0", 0),
		modifierFrame("kbd1", 0),
	}
	for _, frame := range frames {
		if err := forwarder.Frame(frame); err != nil {
			t.Fatal(err)
		}
	}
	var values []int32
	for _, event := range writer.events {
		if event.Type == input.EVKey && event.Code == keycode.KeyLeftShift {
			values = append(values, event.Value)
		}
	}
	if len(values) != 2 || values[0] != 1 || values[1] != 0 {
		t.Fatalf("virtual modifier changed before global 0/1 transitions: %v", values)
	}
}

func TestResyncRepairsDroppedRelease(t *testing.T) {
	writer := &recordingWriter{}
	forwarder := NewForwarder(writer)
	if err := forwarder.Frame(modifierFrame("kbd0", 1)); err != nil {
		t.Fatal(err)
	}
	if err := forwarder.Resync("kbd0", nil); err != nil {
		t.Fatal(err)
	}
	var values []int32
	for _, event := range writer.events {
		if event.Type == input.EVKey && event.Code == keycode.KeyLeftShift {
			values = append(values, event.Value)
		}
	}
	if len(values) != 2 || values[0] != 1 || values[1] != 0 {
		t.Fatalf("resync did not synthesize missing release: %v", values)
	}
}

func modifierFrame(device string, value int32) input.Frame {
	return input.Frame{Device: device, Events: []input.Event{
		{Type: input.EVKey, Code: keycode.KeyLeftShift, Value: value},
		{Type: input.EVSyn, Code: input.SynReport},
	}}
}
