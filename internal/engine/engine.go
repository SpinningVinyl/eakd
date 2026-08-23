package engine

import (
	"slices"
	"time"

	"eak/internal/config"
	"eak/internal/input"
	"eak/internal/keycode"
)

type mode uint8

const (
	modeIdle mode = iota
	modePrefixCandidate
	modeAwaitBinding
	modeBindingCandidate
)

type Result struct {
	Forward []input.Frame
	Actions []string
}

type transition struct {
	key        keycode.Logical
	value      int32
	countAfter int
}

type sequence struct {
	possible []int
	seen     map[keycode.Logical]bool
	held     map[keycode.Logical]bool
	matched  int
}

type Engine struct {
	cfg config.Config

	mode         mode
	activePrefix int
	seq          sequence
	buffer       []input.Frame
	deadline     time.Time

	physical     map[string]map[uint16]bool
	logicalCount map[keycode.Logical]int
	startKeys    map[keycode.Logical]bool
	bindingCarry map[keycode.Logical]bool
}

func New(cfg config.Config) *Engine {
	e := &Engine{
		cfg:          cfg,
		activePrefix: -1,
		physical:     make(map[string]map[uint16]bool),
		logicalCount: make(map[keycode.Logical]int),
		startKeys:    make(map[keycode.Logical]bool),
	}
	for _, prefix := range cfg.Prefixes {
		for _, key := range prefix.Keys {
			if keycode.IsLogicalModifier(key) {
				e.startKeys[key] = true
			}
		}
	}
	return e
}

// HandleFrame updates authoritative physical state, advances the prefix
// machine, and returns frames that are safe to expose through uinput.
func (e *Engine) HandleFrame(frame input.Frame, now time.Time) Result {
	result := e.expire(now)
	next := e.handleFrame(frame, now)
	result.Forward = append(result.Forward, next.Forward...)
	result.Actions = append(result.Actions, next.Actions...)
	return result
}

func (e *Engine) handleFrame(frame input.Frame, now time.Time) Result {
	transitions := e.applyFrame(frame)
	if len(transitions) == 0 {
		if e.mode == modePrefixCandidate || e.mode == modeBindingCandidate {
			e.buffer = append(e.buffer, frame.Clone())
			return Result{}
		}
		return Result{Forward: []input.Frame{frame}}
	}

	consumed := false

	buffered := e.mode == modePrefixCandidate || e.mode == modeBindingCandidate
	if e.mode == modeIdle {
		for _, tr := range transitions {
			if tr.value == 1 && e.startKeys[tr.key] {
				e.startPrefixCandidate(tr.key, now)
				buffered = true
				break
			}
		}
	}
	if e.mode == modeAwaitBinding {
		for _, tr := range transitions {
			if tr.value == 1 {
				e.startBindingCandidate(tr.key)
				buffered = true
				break
			}
		}

		// Only carry releases if no binding candidate was started.
		if e.mode == modeAwaitBinding {
			consumed = true
			for _, tr := range transitions {
				carriedRelease := tr.value == 0 && e.bindingCarry[tr.key]
				if !carriedRelease {
					consumed = false
					continue
				}
				if tr.countAfter == 0 {
					delete(e.bindingCarry, tr.key)
				}
			}
		}
	}

	if buffered {
		e.buffer = append(e.buffer, frame.Clone())
	}

	var result Result
	for _, tr := range transitions {
		switch e.mode {
		case modePrefixCandidate:
			e.advancePrefix(tr, &result)
		case modeBindingCandidate:
			e.advanceBinding(tr, &result)
		}
		// A failed sequence may flush the complete frame. Do not begin a new
		// candidate from a later event in that already-forwarded frame.
		if len(result.Forward) != 0 {
			break
		}
	}
	// Complete a chord only after every transition in its atomic frame has
	// been validated. Any extra key instead fails and replays the candidate.
	if len(result.Forward) == 0 {
		switch e.mode {
		case modePrefixCandidate:
			if e.seq.matched >= 0 {
				if heldModifiers, ready := prefixReadyForBinding(e.cfg.Prefixes[e.seq.matched].Keys, e.seq.held); ready {
					e.activePrefix = e.seq.matched
					e.mode = modeAwaitBinding
					e.bindingCarry = heldModifiers
					e.seq = sequence{}
					e.buffer = nil // consume the complete prefix
					e.deadline = now.Add(e.cfg.SequenceTimeout)
				}
			}
		case modeBindingCandidate:
			bindings := e.cfg.Prefixes[e.activePrefix].Bindings
			if e.seq.matched >= 0 && chordReleased(bindings[e.seq.matched].Keys, e.seq.held) {
				result.Actions = append(result.Actions, bindings[e.seq.matched].Action)
				e.toIdle() // consume the continuation
			}
		}
	}

	if !buffered && !consumed && len(result.Forward) == 0 && len(result.Actions) == 0 {
		result.Forward = append(result.Forward, frame)
	}
	return result
}

// HandleTimeout resolves the currently ambiguous input. An incomplete prefix
// is replayed. A recognized prefix remains consumed, while an incomplete
// continuation is replayed.
func (e *Engine) HandleTimeout(now time.Time) Result {
	return e.expire(now)
}

func (e *Engine) expire(now time.Time) Result {
	if e.deadline.IsZero() || now.Before(e.deadline) {
		return Result{}
	}
	var result Result
	switch e.mode {
	case modePrefixCandidate, modeBindingCandidate:
		result.Forward = append(result.Forward, e.buffer...)
	case modeAwaitBinding:
		// The prefix was recognized and is intentionally consumed.
	}
	e.toIdle()
	return result
}

func (e *Engine) Deadline() (time.Time, bool) {
	return e.deadline, !e.deadline.IsZero()
}

// Resync replaces one device's physical state after SYN_DROPPED or removal.
// Ambiguous buffered input cannot be trusted after an overrun and is dropped;
// the forwarder independently reconciles its virtual state to Pressed.
func (e *Engine) Resync(device string, pressed map[uint16]bool) {
	old := e.physical[device]
	for code := range old {
		logical := keycode.Canonical(code)
		e.logicalCount[logical]--
		if e.logicalCount[logical] <= 0 {
			delete(e.logicalCount, logical)
		}
	}
	if len(pressed) == 0 {
		delete(e.physical, device)
	} else {
		replacement := make(map[uint16]bool, len(pressed))
		for code, down := range pressed {
			if !down {
				continue
			}
			replacement[code] = true
			e.logicalCount[keycode.Canonical(code)]++
		}
		e.physical[device] = replacement
	}
	e.toIdle()
}

func (e *Engine) applyFrame(frame input.Frame) []transition {
	deviceState := e.physical[frame.Device]
	if deviceState == nil {
		deviceState = make(map[uint16]bool)
		e.physical[frame.Device] = deviceState
	}
	var transitions []transition
	for _, event := range frame.Events {
		if event.Type != input.EVKey {
			continue
		}
		logical := keycode.Canonical(event.Code)
		switch event.Value {
		case 1:
			if deviceState[event.Code] {
				continue
			}
			deviceState[event.Code] = true
			e.logicalCount[logical]++
		case 0:
			if !deviceState[event.Code] {
				continue
			}
			delete(deviceState, event.Code)
			e.logicalCount[logical]--
			if e.logicalCount[logical] <= 0 {
				delete(e.logicalCount, logical)
			}
		case 2:
			// Repeats affect neither physical nor matching state.
		default:
			continue
		}
		transitions = append(transitions, transition{
			key: logical, value: event.Value, countAfter: e.logicalCount[logical],
		})
	}
	return transitions
}

func (e *Engine) startPrefixCandidate(first keycode.Logical, now time.Time) {
	possible := make([]int, 0, len(e.cfg.Prefixes))
	for i, prefix := range e.cfg.Prefixes {
		if slices.Contains(prefix.Keys, first) {
			possible = append(possible, i)
		}
	}
	e.mode = modePrefixCandidate
	e.seq = newSequence(possible)
	e.deadline = now.Add(e.cfg.CandidateTimeout)
}

func (e *Engine) advancePrefix(tr transition, result *Result) {
	if !e.advanceSequence(tr, func(index int) []keycode.Logical {
		return e.cfg.Prefixes[index].Keys
	}) {
		result.Forward = append(result.Forward, e.buffer...)
		e.toIdle()
	}
}

func (e *Engine) startBindingCandidate(first keycode.Logical) {
	bindings := e.cfg.Prefixes[e.activePrefix].Bindings
	possible := make([]int, 0, len(bindings))
	for i, binding := range e.cfg.Prefixes[e.activePrefix].Bindings {
		if !slices.Contains(binding.Keys, first) {
			continue
		}
		includesCarry := true
		for key := range e.bindingCarry {
			if !slices.Contains(binding.Keys, key) {
				includesCarry = false
				break
			}
		}
		if includesCarry {
			possible = append(possible, i)
		}
	}
	e.mode = modeBindingCandidate
	e.seq = newSequence(possible)
	for key := range e.bindingCarry {
		e.seq.seen[key] = true
		e.seq.held[key] = true
	}
}

func (e *Engine) advanceBinding(tr transition, result *Result) {
	bindings := e.cfg.Prefixes[e.activePrefix].Bindings
	if !e.advanceSequence(tr, func(index int) []keycode.Logical { return bindings[index].Keys }) {
		result.Forward = append(result.Forward, e.buffer...)
		e.toIdle()
	}
}

// advanceSequence returns false when the observed combination can no longer
// match. A chord is recognized only while all its keys are simultaneously held.
func (e *Engine) advanceSequence(tr transition, chord func(int) []keycode.Logical) bool {
	if tr.value == 2 {
		return true
	}
	if tr.value == 1 {
		// reject a candidate when a sequence keypress is not the first global press
		if tr.countAfter != 1 {
			return false
		}
		e.seq.seen[tr.key] = true
		e.seq.held[tr.key] = true
		filtered := e.seq.possible[:0]
		for _, index := range e.seq.possible {
			if slices.Contains(chord(index), tr.key) {
				filtered = append(filtered, index)
			}
		}
		e.seq.possible = filtered
		if len(filtered) == 0 {
			return false
		}
		for _, index := range filtered {
			if chordHeldAndSeen(chord(index), e.seq.seen, e.seq.held) {
				e.seq.matched = index
				break
			}
		}
		return true
	}
	if tr.value == 0 && tr.countAfter == 0 {
		e.seq.held[tr.key] = false
		if e.seq.matched < 0 {
			return false
		}
		if !slices.Contains(chord(e.seq.matched), tr.key) {
			return false
		}
	}
	return true
}

func newSequence(possible []int) sequence {
	return sequence{
		possible: possible,
		seen:     make(map[keycode.Logical]bool),
		held:     make(map[keycode.Logical]bool),
		matched:  -1,
	}
}

func (e *Engine) toIdle() {
	e.mode = modeIdle
	e.activePrefix = -1
	e.seq = sequence{}
	e.buffer = nil
	e.deadline = time.Time{}
	e.bindingCarry = nil
}

func chordHeldAndSeen(keys []keycode.Logical, seen, held map[keycode.Logical]bool) bool {
	for _, key := range keys {
		if !seen[key] || !held[key] {
			return false
		}
	}
	return true
}

func chordReleased(keys []keycode.Logical, held map[keycode.Logical]bool) bool {
	for _, key := range keys {
		if held[key] {
			return false
		}
	}
	return true
}

func prefixReadyForBinding(prefixKeys []keycode.Logical, held map[keycode.Logical]bool) (carry map[keycode.Logical]bool, ready bool) {
	result := make(map[keycode.Logical]bool)
	for _, key := range prefixKeys {
		if held[key] && keycode.IsLogicalModifier(key) {
			result[key] = true
		} else if held[key] && !keycode.IsLogicalModifier(key) {
			return nil, false
		}
	}
	return result, true
}
