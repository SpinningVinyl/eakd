// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package engine

import (
	"math/rand"
	"slices"
	"testing"
	"time"

	"eak/internal/config"
	"eak/internal/input"
	"eak/internal/keycode"
	"eak/internal/linuxinput"
)

// Exercise the real ordered consumer and check physical ownership after every call.
type engineRig struct {
	t       *testing.T
	e       *Engine
	w       *traceWriter
	f       *linuxinput.Forwarder
	now     time.Time
	actions []string
}

func newRig(t *testing.T, cfg config.Config) *engineRig {
	w := &traceWriter{down: make(map[uint16]bool)}
	return &engineRig{t: t, e: New(cfg), w: w, f: linuxinput.NewForwarder(w), now: time.Unix(1, 0)}
}
func heldConfig() config.Config {
	cfg := testConfig()
	cfg.ReservedModifiers = []keycode.Logical{keycode.LogicalLogo}
	cfg.Prefixes = append(cfg.Prefixes, config.Prefix{Keys: []keycode.Logical{keycode.LogicalLogo, keycode.Logical(keycode.KeyHome)}, Mode: config.Hold, Target: keycode.KeyInsert})
	return cfg
}
func (r *engineRig) apply(result Result) {
	r.t.Helper()
	for _, out := range result.Output {
		switch out.Kind {
		case ForwardFrame:
			if err := r.f.Frame(out.Frame); err != nil {
				r.t.Fatal(err)
			}
		case ReconcileDevice:
			if err := r.f.Resync(out.Device, out.Pressed); err != nil {
				r.t.Fatal(err)
			}
		case EmitAction:
			r.actions = append(r.actions, out.Action)
		default:
			r.t.Fatalf("unknown operation: %+v", out)
		}
	}
	counts := make(map[keycode.Logical]int)
	for device, keys := range r.e.physical {
		for code, p := range keys {
			if !p.down || p.device != device || p.code != code {
				r.t.Fatalf("bad physical lifetime: %+v", p)
			}
			counts[p.logical]++
		}
	}
	if len(counts) != len(r.e.logicalCount) {
		r.t.Fatal("physical counts disagree")
	}
	for key, n := range counts {
		if r.e.logicalCount[key] != n {
			r.t.Fatal("physical counts disagree")
		}
	}
	for p := range r.e.reuse {
		if !p.down || p.route != consumed {
			r.t.Fatal("invalid borrowed lifetime")
		}
	}
	if c := r.e.seq; c != nil {
		for _, p := range c.owned {
			if p.route != undecided {
				r.t.Fatal("candidate owns a committed press")
			}
		}
		for _, p := range c.members {
			if p.route == forwarded || c.held[p] != p.down {
				r.t.Fatal("candidate lifetime disagrees with physical state")
			}
		}
	}
	for _, a := range r.e.active {
		if !a.live || a.driver == nil {
			r.t.Fatal("invalid activation")
		}
		for _, p := range a.members {
			if !p.down || p.route != consumed {
				r.t.Fatal("activation lost its source ownership")
			}
		}
	}
	if r.e.mode == modeReserved {
		if _, pending := r.e.Deadline(); pending {
			r.t.Fatal("reserved wait has a deadline")
		}
	}
}
func (r *engineRig) key(device string, code uint16, value int32) {
	r.t.Helper()
	r.apply(r.e.HandleFrame(keyFrame(device, code, value), r.now))
}
func (r *engineRig) timeout() { r.now = r.now.Add(time.Second); r.apply(r.e.HandleTimeout(r.now)) }
func (r *engineRig) keys() []input.Event {
	var events []input.Event
	for _, ev := range r.w.events {
		if ev.Type == input.EVKey {
			events = append(events, ev)
		}
	}
	return events
}
func (r *engineRig) want(events ...input.Event) {
	r.t.Helper()
	if got := r.keys(); !slices.Equal(got, events) {
		r.t.Fatalf("keys=%+v, want %+v", got, events)
	}
	r.w.events = nil
}
func ev(code uint16, value int32) input.Event {
	return input.Event{Type: input.EVKey, Code: code, Value: value}
}

func TestReservedWaitAndHeldRepeats(t *testing.T) {
	for _, modifierFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "source released first", true: "modifier released first"}[modifierFirst], func(t *testing.T) {
			r := newRig(t, heldConfig())
			r.key("kbd", keycode.KeyLeftMeta, 1)
			r.now = r.now.Add(10 * time.Second)
			r.apply(r.e.HandleTimeout(r.now))
			r.want()
			for i := 0; i < 1000; i++ {
				r.key("kbd", keycode.KeyLeftMeta, 2)
			}
			if len(r.e.journal) != 1 {
				t.Fatalf("reservation grows: %d frames", len(r.e.journal))
			}
			r.key("kbd", keycode.KeyHome, 1)
			r.want(ev(keycode.KeyInsert, 1))
			r.timeout()
			r.want()
			r.key("kbd", keycode.KeyHome, 2)
			r.key("kbd", keycode.KeyHome, 2)
			r.want(ev(keycode.KeyInsert, 2), ev(keycode.KeyInsert, 2))
			first, second := uint16(keycode.KeyHome), uint16(keycode.KeyLeftMeta)
			if modifierFirst {
				first, second = second, first
			}
			r.key("kbd", first, 0)
			r.want(ev(keycode.KeyInsert, 0))
			r.key("kbd", keycode.KeyHome, 2)
			r.want()
			r.key("kbd", second, 0)
			r.want()
			if len(r.e.active) != 0 || len(r.e.physical) != 0 || len(r.e.reuse) != 0 {
				t.Fatal("ownership remains")
			}
		})
	}
}

func TestReservedFallbackAndPrefix(t *testing.T) {
	for _, outcome := range []string{"release", "unrelated", "forwarded release", "prefix", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			r := newRig(t, heldConfig())
			if outcome == "forwarded release" {
				r.key("other", keycode.KeyA, 1)
				r.want(ev(keycode.KeyA, 1))
			}
			r.key("kbd", keycode.KeyLeftMeta, 1)
			r.now = r.now.Add(10 * time.Second)
			switch outcome {
			case "release":
				r.key("kbd", keycode.KeyLeftMeta, 0)
				r.want(ev(keycode.KeyLeftMeta, 1), ev(keycode.KeyLeftMeta, 0))
			case "unrelated":
				r.key("kbd", keycode.KeyA, 1)
				r.want(ev(keycode.KeyLeftMeta, 1), ev(keycode.KeyA, 1))
				r.key("kbd", keycode.KeyA, 0)
				r.key("kbd", keycode.KeyLeftMeta, 0)
				r.want(ev(keycode.KeyA, 0), ev(keycode.KeyLeftMeta, 0))
			case "forwarded release":
				r.key("other", keycode.KeyA, 0)
				r.want(ev(keycode.KeyLeftMeta, 1), ev(keycode.KeyA, 0))
				r.key("kbd", keycode.KeyLeftMeta, 0)
				r.want(ev(keycode.KeyLeftMeta, 0))
			case "prefix", "timeout":
				r.key("kbd", keycode.KeyT, 1)
				deadline, _ := r.e.Deadline()
				if !deadline.Equal(r.now.Add(r.e.cfg.CandidateTimeout)) {
					t.Fatal("deadline anchored before chord attempt")
				}
				if outcome == "timeout" {
					r.timeout()
					r.want(ev(keycode.KeyLeftMeta, 1), ev(keycode.KeyT, 1))
					r.key("kbd", keycode.KeyT, 0)
					r.key("kbd", keycode.KeyLeftMeta, 0)
					r.want(ev(keycode.KeyT, 0), ev(keycode.KeyLeftMeta, 0))
				} else {
					r.key("kbd", keycode.KeyT, 0)
					r.key("kbd", keycode.KeyLeftMeta, 0)
					r.key("kbd", keycode.Key1, 1)
					r.key("kbd", keycode.Key1, 0)
					r.want()
					if !slices.Equal(r.actions, []string{"terminal.one"}) {
						t.Fatalf("actions=%v", r.actions)
					}
				}
			}
			if len(r.w.down) != 0 {
				t.Fatal("virtual keys remain")
			}
		})
	}
}

func TestHeldFrameAtomicity(t *testing.T) {
	for _, extra := range []bool{false, true} {
		r := newRig(t, heldConfig())
		r.key("kbd", keycode.KeyLeftMeta, 1)
		events := []input.Event{ev(keycode.KeyHome, 1), ev(keycode.KeyHome, 0)}
		if extra {
			events = append(events, ev(keycode.KeyA, 1))
		}
		events = append(events, input.Event{Type: input.EVSyn})
		r.apply(r.e.HandleFrame(input.Frame{Device: "kbd", Events: events}, r.now))
		if extra {
			r.want(ev(keycode.KeyLeftMeta, 1), ev(keycode.KeyHome, 1), ev(keycode.KeyHome, 0), ev(keycode.KeyA, 1))
		} else {
			r.want(ev(keycode.KeyInsert, 1), ev(keycode.KeyInsert, 0))
		}
	}
}

func TestHeldRecoveryAndSharedTarget(t *testing.T) {
	for _, end := range []string{"release", "resync", "remove", "close"} {
		t.Run(end, func(t *testing.T) {
			r := newRig(t, heldConfig())
			r.key("other", keycode.KeyInsert, 1)
			r.want(ev(keycode.KeyInsert, 1))
			r.key("kbd", keycode.KeyLeftMeta, 1)
			r.key("kbd", keycode.KeyHome, 1)
			r.want()
			switch end {
			case "release":
				r.key("kbd", keycode.KeyHome, 0)
				r.key("kbd", keycode.KeyLeftMeta, 0)
			case "resync":
				r.apply(r.e.Reconcile("kbd", map[uint16]bool{keycode.KeyLeftMeta: true, keycode.KeyHome: true}))
				r.key("kbd", keycode.KeyHome, 2)
				r.key("kbd", keycode.KeyHome, 0)
				r.key("kbd", keycode.KeyLeftMeta, 0)
			case "remove":
				r.apply(r.e.Reconcile("kbd", nil))
			case "close":
				r.apply(r.e.Close())
				r.want(ev(keycode.KeyInsert, 0))
				return
			}
			r.want()
			r.key("other", keycode.KeyInsert, 0)
			r.want(ev(keycode.KeyInsert, 0))
			if len(r.e.active) != 0 {
				t.Fatal("activation remains")
			}
		})
	}
}

func TestHeldReleaseQueuedBehindCandidate(t *testing.T) {
	for _, finish := range []string{"timeout", "close", "reconcile"} {
		t.Run(finish, func(t *testing.T) {
			cfg := heldConfig()
			cfg.Prefixes = append(cfg.Prefixes, config.Prefix{Keys: []keycode.Logical{keycode.LogicalCtrl, keycode.Logical(keycode.KeyB)}, Mode: config.Tap, Target: keycode.KeyC})
			r := newRig(t, cfg)
			r.key("kbd", keycode.KeyLeftMeta, 1)
			r.key("kbd", keycode.KeyHome, 1)
			r.want(ev(keycode.KeyInsert, 1))
			r.key("other", keycode.KeyLeftCtrl, 1)
			r.key("kbd", keycode.KeyHome, 2)
			r.key("kbd", keycode.KeyHome, 0)
			r.want()
			switch finish {
			case "timeout":
				r.timeout()
				r.want(ev(keycode.KeyLeftCtrl, 1), ev(keycode.KeyInsert, 0))
			case "reconcile":
				r.apply(r.e.Reconcile("other", nil))
				r.want(ev(keycode.KeyInsert, 0))
			case "close":
				r.apply(r.e.Close())
				r.want(ev(keycode.KeyInsert, 0))
			}
		})
	}
}

func TestConsumedPrefixReleaseAfterBindingFailure(t *testing.T) {
	r := newRig(t, testConfig())
	r.key("kbd", keycode.KeyLeftMeta, 1)
	r.key("kbd", keycode.KeyT, 1)
	r.key("kbd", keycode.KeyT, 0)
	r.key("kbd", keycode.KeyA, 1)
	r.key("kbd", keycode.KeyLeftMeta, 0)
	r.key("kbd", keycode.KeyA, 0)
	r.want(ev(keycode.KeyA, 1), ev(keycode.KeyA, 0))
}

func TestReservedMultiModifierAndModifierOnlyPrefix(t *testing.T) {
	for _, partialRelease := range []bool{false, true} {
		cfg := heldConfig()
		cfg.ReservedModifiers = append(cfg.ReservedModifiers, keycode.LogicalCtrl)
		cfg.Prefixes = []config.Prefix{{Keys: []keycode.Logical{keycode.LogicalLogo, keycode.LogicalCtrl, keycode.Logical(keycode.KeyHome)}, Mode: config.Hold, Target: keycode.KeyInsert}}
		r := newRig(t, cfg)
		r.key("kbd", keycode.KeyLeftMeta, 1)
		r.timeout()
		r.key("other", keycode.KeyLeftCtrl, 1)
		r.timeout()
		r.want()
		if partialRelease {
			r.key("kbd", keycode.KeyLeftMeta, 0)
			r.want(ev(keycode.KeyLeftMeta, 1), ev(keycode.KeyLeftCtrl, 1), ev(keycode.KeyLeftMeta, 0))
			r.key("other", keycode.KeyLeftCtrl, 0)
			r.want(ev(keycode.KeyLeftCtrl, 0))
		} else {
			r.key("kbd", keycode.KeyHome, 1)
			r.want(ev(keycode.KeyInsert, 1))
			r.key("other", keycode.KeyLeftCtrl, 0)
			r.want(ev(keycode.KeyInsert, 0))
			r.key("kbd", keycode.KeyHome, 0)
			r.key("kbd", keycode.KeyLeftMeta, 0)
			r.want()
		}
	}
	cfg := testConfig()
	cfg.ReservedModifiers = []keycode.Logical{keycode.LogicalLogo}
	cfg.Prefixes[0].Keys = []keycode.Logical{keycode.LogicalLogo}
	r := newRig(t, cfg)
	r.key("kbd", keycode.KeyLeftMeta, 1)
	if r.e.mode != modeAwaitBinding {
		t.Fatal("modifier-only prefix remained reserved")
	}
	r.key("kbd", keycode.KeyLeftMeta, 0)
	r.key("kbd", keycode.Key1, 1)
	r.key("kbd", keycode.Key1, 0)
	r.want()
	if len(r.actions) != 1 {
		t.Fatal("modifier-only prefix did not fire")
	}
	r = newRig(t, cfg)
	r.apply(r.e.HandleFrame(input.Frame{Device: "kbd", Events: []input.Event{
		ev(keycode.KeyLeftMeta, 1), ev(keycode.KeyLeftMeta, 0), {Type: input.EVSyn},
	}}, r.now))
	r.key("kbd", keycode.Key1, 1)
	r.key("kbd", keycode.Key1, 0)
	r.want()
	if len(r.actions) != 1 {
		t.Fatal("same-frame modifier-only prefix did not fire")
	}
}

func TestMultipleSourceKeysRepeatDriver(t *testing.T) {
	cfg := heldConfig()
	cfg.Prefixes[1].Keys = append(cfg.Prefixes[1].Keys, keycode.Logical(keycode.KeyA))
	r := newRig(t, cfg)
	r.key("kbd", keycode.KeyLeftMeta, 1)
	r.key("kbd", keycode.KeyHome, 1)
	r.want()
	r.key("kbd", keycode.KeyA, 1)
	r.want(ev(keycode.KeyInsert, 1))
	r.key("kbd", keycode.KeyHome, 2)
	r.key("kbd", keycode.KeyLeftMeta, 2)
	r.want()
	r.key("kbd", keycode.KeyA, 2)
	r.want(ev(keycode.KeyInsert, 2))
	r.key("kbd", keycode.KeyHome, 0)
	r.want(ev(keycode.KeyInsert, 0))
	r.key("kbd", keycode.KeyA, 2)
	r.key("kbd", keycode.KeyA, 0)
	r.key("kbd", keycode.KeyLeftMeta, 0)
	r.want()
}

func TestConcurrentHeldTargetsAndBorrowedRelease(t *testing.T) {
	cfg := heldConfig()
	cfg.Prefixes = append(cfg.Prefixes, config.Prefix{Keys: []keycode.Logical{keycode.LogicalLogo, keycode.Logical(keycode.KeyA)}, Mode: config.Hold, Target: keycode.KeyInsert})
	r := newRig(t, cfg)
	r.key("kbd", keycode.KeyLeftMeta, 1)
	r.key("kbd", keycode.KeyHome, 1)
	r.want(ev(keycode.KeyInsert, 1))
	r.key("kbd", keycode.KeyA, 1)
	r.want()
	if len(r.e.active) != 2 {
		t.Fatal("expected independent output owners")
	}
	r.key("other", keycode.KeyRightMeta, 1)
	r.want(ev(keycode.KeyRightMeta, 1))
	r.key("kbd", keycode.KeyHome, 0)
	r.want()
	r.key("kbd", keycode.KeyLeftMeta, 0)
	r.want(ev(keycode.KeyInsert, 0))
	r.key("kbd", keycode.KeyA, 2)
	r.key("kbd", keycode.KeyA, 0)
	r.want()
	r.key("other", keycode.KeyRightMeta, 0)
	r.want(ev(keycode.KeyRightMeta, 0))
}

func TestReservationDoesNotBlockHeldOutputRelease(t *testing.T) {
	cfg := heldConfig()
	cfg.ReservedModifiers = append(cfg.ReservedModifiers, keycode.LogicalCtrl)
	cfg.Prefixes = append(cfg.Prefixes, config.Prefix{Keys: []keycode.Logical{keycode.LogicalCtrl, keycode.Logical(keycode.KeyB)}, Mode: config.Hold, Target: keycode.KeyC})
	r := newRig(t, cfg)
	r.key("kbd", keycode.KeyLeftMeta, 1)
	r.key("kbd", keycode.KeyHome, 1)
	r.want(ev(keycode.KeyInsert, 1))
	r.key("other", keycode.KeyLeftCtrl, 1)
	r.want()
	r.key("kbd", keycode.KeyHome, 0)
	r.want(ev(keycode.KeyLeftCtrl, 1), ev(keycode.KeyInsert, 0))
	r.key("other", keycode.KeyLeftCtrl, 0)
	r.key("kbd", keycode.KeyLeftMeta, 0)
	r.want(ev(keycode.KeyLeftCtrl, 0))
	for _, releaseFirst := range []bool{false, true} {
		r = newRig(t, cfg)
		r.key("kbd", keycode.KeyLeftMeta, 1)
		r.key("kbd", keycode.KeyHome, 1)
		r.want(ev(keycode.KeyInsert, 1))
		events := []input.Event{ev(keycode.KeyLeftCtrl, 1), ev(keycode.KeyHome, 0)}
		want := []input.Event{ev(keycode.KeyLeftCtrl, 1), ev(keycode.KeyInsert, 0)}
		if releaseFirst {
			slices.Reverse(events)
			slices.Reverse(want)
		}
		r.apply(r.e.HandleFrame(input.Frame{Device: "kbd", Events: append(events, input.Event{Type: input.EVSyn})}, r.now))
		r.want(want...)
		r.key("kbd", keycode.KeyLeftCtrl, 0)
		r.key("kbd", keycode.KeyLeftMeta, 0)
		r.want(ev(keycode.KeyLeftCtrl, 0))
	}
}

func TestReconcileRetainsUnaffectedRelease(t *testing.T) {
	r := newRig(t, heldConfig())
	r.key("other", keycode.KeyA, 1)
	r.want(ev(keycode.KeyA, 1))
	r.key("kbd", keycode.KeyLeftMeta, 1)
	r.key("kbd", keycode.KeyT, 1)
	// Repeats don't reject a timed candidate; the following release does.
	r.key("other", keycode.KeyA, 2)
	r.want()
	r.apply(r.e.Reconcile("kbd", nil))
	r.want(ev(keycode.KeyA, 2))
	r.key("other", keycode.KeyA, 0)
	r.want(ev(keycode.KeyA, 0))

	r = newRig(t, heldConfig())
	r.key("other", keycode.KeyA, 1)
	r.key("third", keycode.KeyA, 1)
	r.want(ev(keycode.KeyA, 1))
	r.key("kbd", keycode.KeyLeftMeta, 1)
	r.key("kbd", keycode.KeyT, 1)
	r.key("other", keycode.KeyA, 0)
	r.want()
	r.apply(r.e.Reconcile("kbd", nil))
	r.want()
	r.key("third", keycode.KeyA, 0)
	r.want(ev(keycode.KeyA, 0))
}

func TestLongerBindingStillMatches(t *testing.T) {
	cfg := testConfig()
	cfg.Prefixes[0].Bindings = append(cfg.Prefixes[0].Bindings, config.Binding{Keys: []keycode.Logical{keycode.Logical(keycode.Key1), keycode.Logical(keycode.KeyA)}, Action: "long"})
	r := newRig(t, cfg)
	r.key("kbd", keycode.KeyLeftMeta, 1)
	r.key("kbd", keycode.KeyT, 1)
	r.key("kbd", keycode.KeyT, 0)
	r.key("kbd", keycode.KeyLeftMeta, 0)
	r.key("kbd", keycode.Key1, 1)
	r.key("kbd", keycode.KeyA, 1)
	r.key("kbd", keycode.Key1, 0)
	r.key("kbd", keycode.KeyA, 0)
	r.want()
	if !slices.Equal(r.actions, []string{"long"}) {
		t.Fatalf("actions=%v", r.actions)
	}
}

// Bounded reproducible traces check ownership even for duplicate and reordered
// events. Exact-output tests above prevent an engine that drops everything passing.
func TestOwnershipTraceInvariants(t *testing.T) {
	for seed := int64(0); seed < 64; seed++ {
		data := make([]byte, 512)
		_, _ = rand.New(rand.NewSource(seed)).Read(data)
		runOwnershipTrace(t, data)
	}
}

func FuzzOwnershipTraces(f *testing.F) {
	f.Add([]byte{4, 0, 1, 0, 4, 2, 1, 0, 4, 2, 2, 0, 4, 0, 0, 0})
	f.Add([]byte{4, 0, 1, 0, 0, 0, 0, 0, 4, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) { runOwnershipTrace(t, data) })
}

func runOwnershipTrace(t *testing.T, data []byte) {
	t.Helper()
	if len(data) > 1024 {
		data = data[:1024]
	}
	r := newRig(t, heldConfig())
	codes := []uint16{keycode.KeyLeftMeta, keycode.KeyRightMeta, keycode.KeyHome, keycode.KeyA, keycode.KeyT, keycode.Key1}
	owners := make(map[*press]disposition)
	for i := 0; i+3 < len(data); i += 4 {
		device := []string{"kbd", "other"}[int(data[i]/16)%2]
		switch data[i] % 16 {
		case 0:
			r.timeout()
		case 1:
			r.apply(r.e.Reconcile(device, nil))
		case 2:
			snapshot := make(map[uint16]bool)
			for j, code := range codes {
				if data[i+1]&(1<<j) != 0 {
					snapshot[code] = true
				}
			}
			r.apply(r.e.Reconcile(device, snapshot))
		default:
			events := []input.Event{ev(codes[int(data[i+1])%len(codes)], int32(data[i+2]%3))}
			if data[i+3]&1 != 0 {
				events = append(events, ev(codes[int(data[i+3]>>1)%len(codes)], int32(data[i+1]%3)))
			}
			if data[i+3]&2 != 0 {
				events = append([]input.Event{{Type: input.EVMsc, Code: input.MscScan, Value: 125}}, events...)
			}
			events = append(events, input.Event{Type: input.EVSyn})
			r.apply(r.e.HandleFrame(input.Frame{Device: device, Events: events}, r.now))
		}
		for p, route := range owners {
			if route != undecided && p.route != route {
				t.Fatalf("step %d changed committed ownership", i/4)
			}
			owners[p] = p.route
		}
		for _, keys := range r.e.physical {
			for _, p := range keys {
				owners[p] = p.route
			}
		}
		for _, frame := range r.e.journal {
			for _, entry := range frame.events {
				if entry.press != nil {
					owners[entry.press] = entry.press.route
				}
			}
		}
	}
	// Release real remaining keys, not a synthetic reset that could conceal leaks.
	for _, device := range []string{"kbd", "other"} {
		for _, code := range codes {
			if r.e.physical[device][code] != nil {
				r.key(device, code, 0)
			}
		}
	}
	r.timeout()
	if len(r.w.down) != 0 || len(r.e.physical) != 0 || len(r.e.reuse) != 0 || len(r.e.active) != 0 || len(r.e.journal) != 0 {
		t.Fatalf("trace left ownership: virtual=%v engine=%+v", r.w.down, r.e)
	}
}
