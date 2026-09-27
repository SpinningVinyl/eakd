// SPDX-License-Identifier: GPL-2.0-or-later

package engine

import (
	"slices"

	"eak/internal/input"
	"eak/internal/keycode"
)

type transition struct {
	press      *press
	value      int32
	countAfter int
}

func (e *Engine) ingest(device string, event input.Event) (*press, transition, bool) {
	if event.Type != input.EVKey {
		return nil, transition{}, false
	}
	p := e.physical[device][event.Code]
	switch event.Value {
	case 1:
		if p != nil {
			return p, transition{}, false
		}
		e.nextID++
		p = &press{id: e.nextID, device: device, code: event.Code, logical: keycode.Canonical(event.Code), down: true}
		if e.physical[device] == nil {
			e.physical[device] = make(map[uint16]*press)
		}
		e.physical[device][event.Code] = p
		e.logicalCount[p.logical]++
	case 0:
		if p == nil {
			return nil, transition{}, false
		}
		e.release(p)
	case 2:
		if p == nil {
			return nil, transition{}, false
		}
	default:
		return p, transition{}, false
	}
	return p, transition{press: p, value: event.Value, countAfter: e.logicalCount[p.logical]}, true
}

func (e *Engine) release(p *press) {
	p.down = false
	delete(e.physical[p.device], p.code)
	if len(e.physical[p.device]) == 0 {
		delete(e.physical, p.device)
	}
	e.logicalCount[p.logical]--
	if e.logicalCount[p.logical] == 0 {
		delete(e.logicalCount, p.logical)
	}
	delete(e.reuse, p)
	if e.carry[p.logical] == p {
		delete(e.carry, p.logical)
	}
}

// Reconcile cancels ambiguous matching and emits recovery in one ordered
// transaction. A snapshot never recognizes a chord or starts a held output.
func (e *Engine) Reconcile(device string, pressed map[uint16]bool) Result {
	e.fail()
	var ended []*activation
	for _, a := range e.active {
		for _, p := range a.members {
			if p.device == device {
				a.live = false
				ended = append(ended, a)
				break
			}
		}
	}
	e.active = slices.DeleteFunc(e.active, func(a *activation) bool { return !a.live })
	// Retain generated operations but discard untrustworthy physical events.
	for _, f := range e.journal {
		if f.device == device {
			f.events = slices.DeleteFunc(f.events, func(r recordedEvent) bool { return r.output == nil })
		}
	}
	result := e.drain()
	for _, a := range ended {
		e.stopActivation(a)
	}
	for code, p := range e.physical[device] {
		if !pressed[code] {
			e.release(p)
		}
	}
	var codes []uint16
	for code, down := range pressed {
		if down {
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	visible := make(map[uint16]bool)
	for _, code := range codes {
		p := e.physical[device][code]
		if p == nil {
			p, _, _ = e.ingest(device, input.Event{Type: input.EVKey, Code: code, Value: 1})
		}
		if p.route == undecided {
			p.route = forwarded
		}
		if p.route == forwarded {
			visible[code] = true
		}
	}
	e.queueOutput(Output{Kind: ReconcileDevice, Device: device, Pressed: visible})
	result.Output = append(result.Output, e.drain().Output...)
	return result
}

// Close releases virtual owners without replaying ambiguous input during exit.
func (e *Engine) Close() Result {
	e.fail()
	// Keep queued releases, including activations that already ended behind a
	// candidate. Discarding their journal entries would strand virtual owners.
	for _, f := range e.journal {
		f.events = slices.DeleteFunc(f.events, func(r recordedEvent) bool {
			return r.output == nil && (r.event.Type != input.EVKey || r.event.Value != 0)
		})
	}
	for _, a := range e.active {
		e.stopActivation(a)
	}
	e.active = nil
	var devices []string
	for device := range e.physical {
		devices = append(devices, device)
	}
	slices.Sort(devices)
	for _, device := range devices {
		for _, p := range e.physical[device] {
			e.release(p)
		}
		e.queueOutput(Output{Kind: ReconcileDevice, Device: device})
	}
	return e.drain()
}
