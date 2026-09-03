// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package linuxinput

import (
	"errors"
	"strings"
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

type failingWriter struct {
	err error
}

func (w failingWriter) Write(input.Event) error { return w.err }

func TestFramePropagatesWriterError(t *testing.T) {
	wanted := errors.New("write failed")
	forwarder := NewForwarder(failingWriter{err: wanted})
	err := forwarder.Frame(input.Frame{Device: "kbd0", Events: []input.Event{
		{Type: input.EVKey, Code: keycode.KeyA, Value: 1},
	}})
	if !errors.Is(err, wanted) {
		t.Fatalf("Frame returned %v; want %v", err, wanted)
	}
}

func TestResyncReportsReleaseAndPressWriterErrors(t *testing.T) {
	wanted := errors.New("write failed")

	t.Run("release", func(t *testing.T) {
		forwarder := NewForwarder(&recordingWriter{})
		if err := forwarder.Frame(keyFrame("kbd0", keycode.KeyA, 1)); err != nil {
			t.Fatal(err)
		}
		forwarder.output = failingWriter{err: wanted}
		err := forwarder.Resync("kbd0", nil)
		if !errors.Is(err, wanted) || !strings.Contains(err.Error(), "resync release") {
			t.Fatalf("Resync returned %v; want contextual release error", err)
		}
	})

	t.Run("press", func(t *testing.T) {
		forwarder := NewForwarder(failingWriter{err: wanted})
		err := forwarder.Resync("kbd0", map[uint16]bool{keycode.KeyA: true})
		if !errors.Is(err, wanted) || !strings.Contains(err.Error(), "resync press") {
			t.Fatalf("Resync returned %v; want contextual press error", err)
		}
	})
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

func TestResyncAcrossKeyboardsReleasesBeforePressing(t *testing.T) {
	writer := &recordingWriter{}
	forwarder := NewForwarder(writer)
	for _, frame := range []input.Frame{
		keyFrame("kbd0", keycode.KeyA, 1),
		keyFrame("kbd0", keycode.KeyB, 1),
		keyFrame("kbd1", keycode.KeyA, 1),
	} {
		if err := forwarder.Frame(frame); err != nil {
			t.Fatal(err)
		}
	}

	writer.events = nil
	if err := forwarder.Resync("kbd0", map[uint16]bool{keycode.KeyC: true}); err != nil {
		t.Fatal(err)
	}
	want := []input.Event{
		{Type: input.EVKey, Code: keycode.KeyB, Value: 0},
		{Type: input.EVKey, Code: keycode.KeyC, Value: 1},
		{Type: input.EVSyn, Code: input.SynReport},
	}
	if len(writer.events) != len(want) {
		t.Fatalf("events = %#v; want %#v", writer.events, want)
	}
	for i := range want {
		if writer.events[i] != want[i] {
			t.Fatalf("event %d = %#v; want %#v", i, writer.events[i], want[i])
		}
	}

	writer.events = nil
	if err := forwarder.Resync("kbd1", nil); err != nil {
		t.Fatal(err)
	}
	if len(writer.events) != 2 || writer.events[0] != (input.Event{Type: input.EVKey, Code: keycode.KeyA, Value: 0}) {
		t.Fatalf("last shared-key release = %#v", writer.events)
	}
}

func keyFrame(device string, code uint16, value int32) input.Frame {
	return input.Frame{Device: device, Events: []input.Event{
		{Type: input.EVKey, Code: code, Value: value},
		{Type: input.EVSyn, Code: input.SynReport},
	}}
}

func modifierFrame(device string, value int32) input.Frame {
	return keyFrame(device, keycode.KeyLeftShift, value)
}
