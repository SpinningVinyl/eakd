// SPDX-License-Identifier: GPL-2.0-or-later

package engine

import (
	"fmt"
	"slices"
	"time"

	"eak/internal/config"
	"eak/internal/input"
	"eak/internal/keycode"
)

type OutputKind uint8

const (
	ForwardFrame OutputKind = iota
	ReconcileDevice
	EmitAction
)

// Output operations must be applied synchronously in order. Stop on any error.
type Output struct {
	Kind    OutputKind
	Frame   input.Frame
	Device  string
	Pressed map[uint16]bool
	Action  string
}

type Result struct{ Output []Output }

type disposition uint8

const (
	undecided disposition = iota
	forwarded
	consumed
)

// A release/repress creates a new lifetime, even within one input frame.
type press struct {
	id      uint64
	device  string
	code    uint16
	logical keycode.Logical
	down    bool
	route   disposition
}

type mode uint8

const (
	modeIdle mode = iota
	modeReserved
	modePrefixCandidate
	modeAwaitBinding
	modeBindingCandidate
)

type candidate struct {
	possible     []int
	matched      int
	members      map[keycode.Logical]*press
	held         map[*press]bool
	owned        []*press
	matchMembers []*press
	driver       *press
	broken       bool
	route        disposition
}

type activation struct {
	device  string
	target  uint16
	members []*press
	driver  *press
	live    bool
}

type recordedEvent struct {
	event  input.Event
	press  *press
	output *Output
	repeat *activation
}

type journalFrame struct {
	device    string
	events    []recordedEvent
	dropScan  bool
	candidate *candidate
}

// Physical ingestion owns physical/counts; matching owns candidate membership;
// resolve is the only operation that commits a candidate's press dispositions.
type Engine struct {
	cfg            config.Config
	physical       map[string]map[uint16]*press
	logicalCount   map[keycode.Logical]int
	nextID         uint64
	mode           mode
	seq            *candidate
	activePrefix   int
	carry          map[keycode.Logical]*press
	reuse          map[*press]bool
	deadline       time.Time
	journal        []*journalFrame
	active         []*activation
	nextActivation uint64
}

func New(cfg config.Config) *Engine {
	return &Engine{cfg: cfg, physical: make(map[string]map[uint16]*press),
		logicalCount: make(map[keycode.Logical]int), reuse: make(map[*press]bool), activePrefix: -1}
}

func (e *Engine) Deadline() (time.Time, bool) { return e.deadline, !e.deadline.IsZero() }

func (e *Engine) HandleTimeout(now time.Time) Result {
	if !e.deadline.IsZero() && !now.Before(e.deadline) {
		e.fail()
	}
	return e.drain()
}

func (e *Engine) HandleFrame(frame input.Frame, now time.Time) Result {
	result := e.HandleTimeout(now)
	f := &journalFrame{device: frame.Device, candidate: e.seq}
	for _, p := range e.physical[frame.Device] {
		f.dropScan = f.dropScan || p.route == consumed
	}
	e.journal = append(e.journal, f)
	var history []transition
	var fresh []*press
	failed, meaningful := false, false
	for _, event := range frame.Events {
		p, tr, valid := e.ingest(frame.Device, event)
		if valid && event.Value == 1 {
			fresh = append(fresh, p)
		}
		// Pending modifier chatter must not grow an indefinite reservation.
		chatter := e.mode == modeReserved && p != nil && p.route == undecided &&
			(event.Value == 2 || event.Value == 1 && !valid)
		if !chatter {
			f.events = append(f.events, recordedEvent{event: event, press: p})
		}
		if event.Type != input.EVSyn && event.Type != input.EVMsc && !chatter {
			meaningful = true
		}
		if valid {
			outputs := e.activationEvents(tr)
			if len(outputs) > 0 && e.mode == modeReserved {
				e.fail()
				failed = true
			}
			f.events = append(f.events, outputs...)
		}
		if failed {
			continue
		}
		if e.mode == modeReserved && !chatter && event.Type != input.EVSyn && event.Type != input.EVMsc &&
			(!valid || p != nil && p.route == forwarded) {
			e.fail()
			failed = true
			continue
		}
		if !valid {
			continue
		}
		started := false
		if event.Value == 1 && p.route == undecided {
			switch e.mode {
			case modeIdle:
				started = e.startSource(p, now)
			case modeAwaitBinding:
				e.startBinding()
				started = true
			}
		}
		if started {
			f.candidate = e.seq
			// Preserve atomic matching when the modifier follows another key.
			for _, earlier := range history {
				if !e.advance(earlier, now) {
					failed = true
					break
				}
			}
		}
		if !failed && e.seq != nil && !e.advance(tr, now) {
			failed = true
		}
		history = append(history, tr)
	}
	// Output earlier in this same frame must not get trapped behind a later
	// reserved modifier (notably a held remap's already-staged release).
	if e.mode == modeReserved {
		for _, r := range f.events {
			if r.output != nil || r.event.Type != input.EVSyn && r.event.Type != input.EVMsc &&
				(r.press == nil || r.press.route == forwarded) {
				e.fail()
				failed = true
				break
			}
		}
	}
	if !failed {
		e.complete(now)
	}
	// Ordinary presses cannot become candidates in a later frame.
	for _, p := range fresh {
		if p.route == undecided && (e.seq == nil || !slices.Contains(e.seq.owned, p)) {
			p.route = forwarded
		}
	}
	if e.mode == modeReserved && !meaningful {
		e.journal = e.journal[:len(e.journal)-1]
	}
	result.Output = append(result.Output, e.drain().Output...)
	return result
}

func (e *Engine) resolve(route disposition) {
	if e.seq == nil {
		return
	}
	for _, p := range e.seq.owned {
		if p.route != undecided {
			panic("engine: resolving an already owned press")
		}
		p.route = route
	}
	e.seq.route = route
}

func (e *Engine) idle() {
	e.mode, e.seq, e.activePrefix, e.carry = modeIdle, nil, -1, nil
	e.deadline = time.Time{}
}

func (e *Engine) fail() { e.resolve(forwarded); e.idle() }

func keyOutput(device string, code uint16, value int32) Output {
	return Output{Kind: ForwardFrame, Frame: input.Frame{Device: device, Events: []input.Event{
		{Type: input.EVKey, Code: code, Value: value}, {Type: input.EVSyn, Code: input.SynReport},
	}}}
}

func (e *Engine) queueOutput(output Output) {
	e.journal = append(e.journal, &journalFrame{events: []recordedEvent{{output: &output}}})
}

func (e *Engine) drain() Result {
	var result Result
	count := 0
	for _, f := range e.journal {
		pending, hasKey, visibleKey := false, false, false
		for _, r := range f.events {
			pending = pending || r.press != nil && r.press.route == undecided
			if r.event.Type == input.EVKey {
				hasKey = true
				visibleKey = visibleKey || r.press == nil || r.press.route == forwarded
			}
		}
		if pending {
			break
		}
		var events []input.Event
		flush := func() {
			if len(events) > 0 {
				events = append(events, input.Event{Type: input.EVSyn, Code: input.SynReport})
				result.Output = append(result.Output, Output{Kind: ForwardFrame, Frame: input.Frame{Device: f.device, Events: events}})
				events = nil
			}
		}
		for _, r := range f.events {
			if r.output != nil {
				if r.repeat != nil && !r.repeat.live {
					continue
				}
				flush()
				result.Output = append(result.Output, *r.output)
				continue
			}
			if r.event.Type == input.EVSyn || r.press != nil && r.press.route == consumed {
				continue
			}
			if r.event.Type == input.EVMsc && (f.dropScan || hasKey && !visibleKey || !hasKey && f.candidate != nil && f.candidate.route == consumed) {
				continue
			}
			events = append(events, r.event)
		}
		flush()
		count++
	}
	clear(e.journal[:count])
	e.journal = e.journal[count:]
	if len(e.journal) == 0 {
		e.journal = nil
	}
	return result
}

func (e *Engine) activate(c *candidate, target uint16) {
	e.nextActivation++
	a := &activation{device: fmt.Sprintf("eakd-remap:%d", e.nextActivation), target: target,
		members: c.matchMembers, driver: c.driver, live: true}
	e.queueOutput(keyOutput(a.device, target, 1))
	if c.broken {
		e.stopActivation(a)
		return
	}
	e.active = append(e.active, a)
}

func (e *Engine) stopActivation(a *activation) {
	a.live = false
	e.queueOutput(keyOutput(a.device, a.target, 0))
	e.queueOutput(Output{Kind: ReconcileDevice, Device: a.device})
}

func (e *Engine) activationEvents(tr transition) []recordedEvent {
	var events []recordedEvent
	for _, a := range e.active {
		if tr.value == 0 && slices.Contains(a.members, tr.press) {
			a.live = false
			up := keyOutput(a.device, a.target, 0)
			cleanup := Output{Kind: ReconcileDevice, Device: a.device}
			events = append(events, recordedEvent{output: &up}, recordedEvent{output: &cleanup})
		} else if tr.value == 2 && tr.press == a.driver {
			repeat := keyOutput(a.device, a.target, 2)
			events = append(events, recordedEvent{output: &repeat, repeat: a})
		}
	}
	e.active = slices.DeleteFunc(e.active, func(a *activation) bool { return !a.live })
	return events
}
