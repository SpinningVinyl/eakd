//go:build linux

package linuxinput

import (
	"fmt"
	"slices"

	"eak/internal/input"
	"eak/internal/keycode"
)

// Forwarder owns compositor-visible state. This is intentionally separate
// from the matcher's physical state: consumed sequences must never alter it.
type Forwarder struct {
	output eventWriter
	down   map[string]map[uint16]bool
	counts map[uint16]int
}

type eventWriter interface {
	Write(input.Event) error
}

func NewForwarder(output eventWriter) *Forwarder {
	return &Forwarder{
		output: output,
		down:   make(map[string]map[uint16]bool),
		counts: make(map[uint16]int),
	}
}

func (f *Forwarder) Frame(frame input.Frame) error {
	device := f.down[frame.Device]
	if device == nil {
		device = make(map[uint16]bool)
		f.down[frame.Device] = device
	}
	wrote := false
	for _, event := range frame.Events {
		switch event.Type {
		case input.EVKey:
			emit, normalized := f.keyEvent(device, event)
			if !emit {
				continue
			}
			if err := f.output.Write(normalized); err != nil {
				return err
			}
			wrote = true
		case input.EVMsc:
			if event.Code != input.MscScan {
				continue
			}
			if err := f.output.Write(event); err != nil {
				return err
			}
			wrote = true
		}
	}
	if wrote {
		return f.output.Write(input.Event{Type: input.EVSyn, Code: input.SynReport})
	}
	return nil
}

func (f *Forwarder) keyEvent(device map[uint16]bool, event input.Event) (bool, input.Event) {
	switch event.Value {
	case 1:
		if device[event.Code] {
			return false, event
		}
		device[event.Code] = true
		f.counts[event.Code]++
		return f.counts[event.Code] == 1, event
	case 0:
		if !device[event.Code] {
			return false, event
		}
		delete(device, event.Code)
		f.counts[event.Code]--
		if f.counts[event.Code] > 0 {
			return false, event
		}
		delete(f.counts, event.Code)
		return true, event
	case 2:
		if !device[event.Code] || keycode.IsPhysicalModifier(event.Code) {
			return false, event
		}
		return true, event
	default:
		return false, event
	}
}

// Resync makes virtual state agree with EVIOCGKEY after a dropped event. It
// releases missing keys before pressing newly observed ones, then emits one
// synchronization frame.
func (f *Forwarder) Resync(deviceID string, actual map[uint16]bool) error {
	known := f.down[deviceID]
	if known == nil {
		known = make(map[uint16]bool)
		f.down[deviceID] = known
	}
	var releases, presses []uint16
	for code := range known {
		if !actual[code] {
			releases = append(releases, code)
		}
	}
	for code, down := range actual {
		if down && !known[code] {
			presses = append(presses, code)
		}
	}
	slices.Sort(releases)
	slices.Sort(presses)

	wrote := false
	for _, code := range releases {
		emit, event := f.keyEvent(known, input.Event{Type: input.EVKey, Code: code, Value: 0})
		if emit {
			if err := f.output.Write(event); err != nil {
				return fmt.Errorf("resync release: %w", err)
			}
			wrote = true
		}
	}
	for _, code := range presses {
		emit, event := f.keyEvent(known, input.Event{Type: input.EVKey, Code: code, Value: 1})
		if emit {
			if err := f.output.Write(event); err != nil {
				return fmt.Errorf("resync press: %w", err)
			}
			wrote = true
		}
	}
	if len(actual) == 0 {
		delete(f.down, deviceID)
	}
	if wrote {
		return f.output.Write(input.Event{Type: input.EVSyn, Code: input.SynReport})
	}
	return nil
}
