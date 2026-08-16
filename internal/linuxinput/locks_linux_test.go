//go:build linux

package linuxinput

import (
	"context"
	"errors"
	stdio "io"
	"log"
	"syscall"
	"testing"
	"time"

	"eak/internal/input"
)

func TestLockStateStartsUnknownAndFollowsCompositor(t *testing.T) {
	var state LockState
	caps, num, scroll := state.Snapshot()
	if caps || num || scroll {
		t.Fatalf("zero values are not false: caps=%t num=%t scroll=%t", caps, num, scroll)
	}
	for code := uint16(0); code <= input.LEDScrollLock; code++ {
		if state.Known(code) {
			t.Fatalf("LED %d is known before compositor feedback", code)
		}
	}

	if !state.SetLED(input.LEDCapsLock, false) {
		t.Fatal("first explicit off state was not treated as a transition")
	}
	state.SetLED(input.LEDNumLock, true)
	state.SetLED(input.LEDScrollLock, true)
	caps, num, scroll = state.Snapshot()
	if caps || !num || !scroll || !state.Known(input.LEDCapsLock) {
		t.Fatalf("unexpected final state: caps=%t num=%t scroll=%t", caps, num, scroll)
	}
}

func TestLockStateAcceptsCompositorReconciliation(t *testing.T) {
	var state LockState
	if !state.SetLED(input.LEDCapsLock, true) || !state.LED(input.LEDCapsLock) {
		t.Fatal("failed to adopt compositor CapsLock state")
	}
	if state.SetLED(input.LEDCapsLock, true) {
		t.Fatal("setting an unchanged LED reported a transition")
	}
	if state.SetLED(99, true) {
		t.Fatal("accepted an unsupported LED")
	}
}

func TestManagerCoalescesCompositorFeedbackBatch(t *testing.T) {
	m := NewManager("test", log.New(stdio.Discard, "", 0))
	m.SetLEDs([]LEDUpdate{
		{Code: input.LEDCapsLock, Enabled: true},
		{Code: input.LEDNumLock, Enabled: true},
		{Code: input.LEDCapsLock, Enabled: false},
	})
	if !m.consumePendingLEDs() {
		t.Fatal("feedback batch did not change lock state")
	}
	caps, num, scroll := m.locks.Snapshot()
	if caps || !num || scroll {
		t.Fatalf("unexpected coalesced state: caps=%t num=%t scroll=%t", caps, num, scroll)
	}
}

func TestFeedbackLEDUpdatesDoNotRequireSynReport(t *testing.T) {
	updates := feedbackLEDUpdates([]input.Event{
		{Type: input.EVLed, Code: input.LEDCapsLock, Value: 1},
		{Type: input.EVKey, Code: 30, Value: 1},
		{Type: input.EVLed, Code: input.LEDNumLock, Value: 0},
		{Type: input.EVLed, Code: 99, Value: 1},
	})
	if len(updates) != 2 {
		t.Fatalf("got %d LED updates, want 2: %#v", len(updates), updates)
	}
	if updates[0] != (LEDUpdate{Code: input.LEDCapsLock, Enabled: true}) {
		t.Fatalf("unexpected CapsLock update: %#v", updates[0])
	}
	if updates[1] != (LEDUpdate{Code: input.LEDNumLock, Enabled: false}) {
		t.Fatalf("unexpected NumLock update: %#v", updates[1])
	}
}

func TestSendMessageUnblocksWhenContextIsCanceled(t *testing.T) {
	tests := []struct {
		name   string
		output chan input.Message
	}{
		{name: "unbuffered", output: make(chan input.Message)},
		{name: "full", output: fullMessageChannel()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() {
				result <- sendMessage(ctx, test.output, input.Message{Removed: "kbd0"})
			}()
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("send returned %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("send remained blocked after cancellation")
			}
		})
	}
}

func fullMessageChannel() chan input.Message {
	output := make(chan input.Message, 1)
	output <- input.Message{}
	return output
}

func TestAcceptCandidateClosesDeviceWhenInitialKeyQueryFails(t *testing.T) {
	pipe := make([]int, 2)
	if err := syscall.Pipe(pipe); err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(pipe[1])
	if err := syscall.SetNonblock(pipe[0], true); err != nil {
		t.Fatal(err)
	}
	m := NewManager("test", log.New(stdio.Discard, "", 0))
	device := &physicalDevice{path: "/dev/input/event-test", fd: pipe[0]}
	state := &managerState{
		manager:    m,
		candidates: make(map[string]*physicalDevice),
		retries:    make(map[string]acquisitionRetry),
	}
	if err := state.acceptCandidate(device); err != nil {
		t.Fatal(err)
	}
	if len(state.candidates) != 0 {
		t.Fatal("failed candidate remained registered")
	}
	if _, err := syscall.Read(pipe[0], make([]byte, 1)); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("device descriptor remained open after EVIOCGKEY failure: %v", err)
	}
}

func TestReacquisitionAttemptsAreLimited(t *testing.T) {
	var retry acquisitionRetry
	for attempt := 1; attempt <= maxReacquisitionTries; attempt++ {
		if !retry.begin() {
			t.Fatalf("reacquisition attempt %d was rejected", attempt)
		}
		if retry.attempts != attempt {
			t.Fatalf("attempt counter is %d, want %d", retry.attempts, attempt)
		}
	}
	if retry.begin() {
		t.Fatalf("reacquisition attempt %d was accepted", maxReacquisitionTries+1)
	}
}

func TestKeyboardCapabilityQueryFailureIsNotARejection(t *testing.T) {
	wanted := errors.New("capability query failed")
	isKeyboard, err := classifyKeyboardCapabilities(nil, wanted)
	if isKeyboard {
		t.Fatal("failed capability query identified the device as a keyboard")
	}
	if !errors.Is(err, wanted) {
		t.Fatalf("got error %v, want wrapped %v", err, wanted)
	}
}

func TestNonKeyboardCapabilitiesAreRejectedWithoutError(t *testing.T) {
	isKeyboard, err := classifyKeyboardCapabilities(nil, nil)
	if err != nil {
		t.Fatalf("empty capabilities returned an error: %v", err)
	}
	if isKeyboard {
		t.Fatal("empty capabilities identified the device as a keyboard")
	}
}
