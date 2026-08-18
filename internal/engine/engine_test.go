package engine

import (
	"testing"
	"time"

	"eak/internal/config"
	"eak/internal/input"
	"eak/internal/keycode"
)

func testConfig() config.Config {
	return config.Config{
		CandidateTimeout: 500 * time.Millisecond,
		SequenceTimeout:  750 * time.Millisecond,
		Prefixes: []config.Prefix{{
			Keys: []keycode.Logical{keycode.LogicalLogo, keycode.Logical(keycode.KeyT)},
			Bindings: []config.Binding{{
				Keys:   []keycode.Logical{keycode.Logical(keycode.Key1)},
				Action: "terminal.one",
			}},
		}},
	}
}

func keyFrame(device string, code uint16, value int32) input.Frame {
	return input.Frame{Device: device, Events: []input.Event{
		{Type: input.EVKey, Code: code, Value: value},
		{Type: input.EVSyn, Code: input.SynReport},
	}}
}

func TestOrdinaryInputIsForwarded(t *testing.T) {
	e := New(testConfig())
	frame := keyFrame("kbd0", keycode.KeyA, 1)
	result := e.HandleFrame(frame, time.Unix(1, 0))
	if len(result.Forward) != 1 || result.Forward[0].Events[0].Code != keycode.KeyA {
		t.Fatalf("ordinary key was not forwarded: %#v", result)
	}
}

func TestRecognizedSequenceIsConsumedAndEmitsAction(t *testing.T) {
	e := New(testConfig())
	now := time.Unix(1, 0)
	sequence := []input.Frame{
		keyFrame("kbd0", keycode.KeyLeftMeta, 1),
		keyFrame("kbd0", keycode.KeyT, 1),
		keyFrame("kbd0", keycode.KeyT, 0),
		keyFrame("kbd0", keycode.KeyLeftMeta, 0),
		keyFrame("kbd0", keycode.Key1, 1),
		keyFrame("kbd0", keycode.Key1, 0),
	}
	var forwarded []input.Frame
	var actions []string
	for i, frame := range sequence {
		result := e.HandleFrame(frame, now.Add(time.Duration(i)*time.Millisecond))
		forwarded = append(forwarded, result.Forward...)
		actions = append(actions, result.Actions...)
	}
	if len(forwarded) != 0 {
		t.Fatalf("recognized sequence leaked %d frames", len(forwarded))
	}
	if len(actions) != 1 || actions[0] != "terminal.one" {
		t.Fatalf("unexpected actions: %#v", actions)
	}
}

func TestFailedPrefixCandidateIsReplayed(t *testing.T) {
	e := New(testConfig())
	now := time.Unix(1, 0)
	logo := keyFrame("kbd0", keycode.KeyLeftMeta, 1)
	if result := e.HandleFrame(logo, now); len(result.Forward) != 0 {
		t.Fatal("candidate modifier was forwarded before disambiguation")
	}
	x := keyFrame("kbd0", keycode.KeyX, 1)
	result := e.HandleFrame(x, now.Add(time.Millisecond))
	if len(result.Forward) != 2 {
		t.Fatalf("wanted both buffered frames replayed, got %d", len(result.Forward))
	}
	if result.Forward[0].Events[0].Code != keycode.KeyLeftMeta || result.Forward[1].Events[0].Code != keycode.KeyX {
		t.Fatalf("frames replayed out of order: %#v", result.Forward)
	}
}

func TestCandidateTimeoutReplaysButPrefixTimeoutConsumes(t *testing.T) {
	now := time.Unix(1, 0)
	e := New(testConfig())
	e.HandleFrame(keyFrame("kbd0", keycode.KeyLeftMeta, 1), now)
	result := e.HandleTimeout(now.Add(time.Second))
	if len(result.Forward) != 1 {
		t.Fatalf("incomplete candidate should replay, got %#v", result)
	}

	e = New(testConfig())
	frames := []input.Frame{
		keyFrame("kbd0", keycode.KeyLeftMeta, 1),
		keyFrame("kbd0", keycode.KeyT, 1),
		keyFrame("kbd0", keycode.KeyT, 0),
		keyFrame("kbd0", keycode.KeyLeftMeta, 0),
	}
	for i, frame := range frames {
		e.HandleFrame(frame, now.Add(time.Duration(i)*time.Millisecond))
	}
	result = e.HandleTimeout(now.Add(2 * time.Second))
	if len(result.Forward) != 0 {
		t.Fatalf("recognized prefix should remain consumed, got %#v", result)
	}
}

func TestUnknownContinuationIsReplayedWithoutPrefix(t *testing.T) {
	e := New(testConfig())
	now := time.Unix(1, 0)
	for i, frame := range []input.Frame{
		keyFrame("kbd0", keycode.KeyLeftMeta, 1),
		keyFrame("kbd0", keycode.KeyT, 1),
		keyFrame("kbd0", keycode.KeyT, 0),
		keyFrame("kbd0", keycode.KeyLeftMeta, 0),
	} {
		e.HandleFrame(frame, now.Add(time.Duration(i)*time.Millisecond))
	}
	result := e.HandleFrame(keyFrame("kbd0", keycode.KeyX, 1), now.Add(5*time.Millisecond))
	if len(result.Forward) != 1 || result.Forward[0].Events[0].Code != keycode.KeyX {
		t.Fatalf("unknown continuation was not replayed: %#v", result)
	}
}

func TestExtraTransitionInCompletionFrameReplaysCandidate(t *testing.T) {
	e := New(testConfig())
	now := time.Unix(1, 0)
	completeTestPrefix(e, now)
	e.HandleFrame(keyFrame("kbd0", keycode.Key1, 1), now.Add(5*time.Millisecond))

	result := e.HandleFrame(input.Frame{Device: "kbd0", Events: []input.Event{
		{Type: input.EVKey, Code: keycode.Key1, Value: 0},
		{Type: input.EVKey, Code: keycode.KeyX, Value: 1},
		{Type: input.EVSyn, Code: input.SynReport},
	}}, now.Add(6*time.Millisecond))
	if len(result.Actions) != 0 {
		t.Fatalf("ambiguous completion emitted actions: %#v", result.Actions)
	}
	if len(result.Forward) != 2 || result.Forward[1].Events[1].Code != keycode.KeyX {
		t.Fatalf("candidate frame was not replayed intact: %#v", result.Forward)
	}
}

func TestFrameAtCandidateDeadlineCannotAdvancePrefix(t *testing.T) {
	e := New(testConfig())
	now := time.Unix(1, 0)
	e.HandleFrame(keyFrame("kbd0", keycode.KeyLeftMeta, 1), now)

	result := e.HandleFrame(keyFrame("kbd0", keycode.KeyT, 1), now.Add(testConfig().CandidateTimeout))
	if len(result.Actions) != 0 {
		t.Fatalf("expired prefix emitted actions: %#v", result.Actions)
	}
	if len(result.Forward) != 2 ||
		result.Forward[0].Events[0].Code != keycode.KeyLeftMeta ||
		result.Forward[1].Events[0].Code != keycode.KeyT {
		t.Fatalf("expired prefix frames were not forwarded in order: %#v", result.Forward)
	}
}

func TestFrameAtSequenceDeadlineCannotStartContinuation(t *testing.T) {
	e := New(testConfig())
	now := time.Unix(1, 0)
	completeTestPrefix(e, now)
	deadline, exists := e.Deadline()
	if !exists {
		t.Fatal("recognized prefix has no sequence deadline")
	}

	result := e.HandleFrame(keyFrame("kbd0", keycode.Key1, 1), deadline)
	if len(result.Actions) != 0 {
		t.Fatalf("late continuation emitted actions: %#v", result.Actions)
	}
	if len(result.Forward) != 1 || result.Forward[0].Events[0].Code != keycode.Key1 {
		t.Fatalf("late continuation was not forwarded: %#v", result.Forward)
	}
}

func TestReleaseAtSequenceDeadlineCannotCompleteContinuation(t *testing.T) {
	e := New(testConfig())
	now := time.Unix(1, 0)
	completeTestPrefix(e, now)
	deadline, exists := e.Deadline()
	if !exists {
		t.Fatal("recognized prefix has no sequence deadline")
	}
	e.HandleFrame(keyFrame("kbd0", keycode.Key1, 1), deadline.Add(-time.Millisecond))

	result := e.HandleFrame(keyFrame("kbd0", keycode.Key1, 0), deadline)
	if len(result.Actions) != 0 {
		t.Fatalf("expired continuation emitted actions: %#v", result.Actions)
	}
	if len(result.Forward) != 2 ||
		result.Forward[0].Events[0].Value != 1 ||
		result.Forward[1].Events[0].Value != 0 {
		t.Fatalf("expired continuation was not replayed in order: %#v", result.Forward)
	}
}

func completeTestPrefix(e *Engine, now time.Time) {
	for i, frame := range []input.Frame{
		keyFrame("kbd0", keycode.KeyLeftMeta, 1),
		keyFrame("kbd0", keycode.KeyT, 1),
		keyFrame("kbd0", keycode.KeyT, 0),
		keyFrame("kbd0", keycode.KeyLeftMeta, 0),
	} {
		e.HandleFrame(frame, now.Add(time.Duration(i)*time.Millisecond))
	}
}
