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

func TestManagerAppliesCompositorFeedbackBatch(t *testing.T) {
	state, writeFD := newFeedbackTestState(t)
	writeFeedbackEvents(t, writeFD, []input.Event{
		{Type: input.EVLed, Code: input.LEDCapsLock, Value: 1},
		{Type: input.EVLed, Code: input.LEDNumLock, Value: 1},
		{Type: input.EVLed, Code: input.LEDCapsLock, Value: 0},
		{Type: input.EVKey, Code: 30, Value: 1},
		{Type: input.EVLed, Code: 99, Value: 1},
	})
	if err := state.handleFeedbackReady(syscall.EpollEvent{Events: syscall.EPOLLIN}); err != nil {
		t.Fatal(err)
	}
	caps, num, scroll := state.manager.locks.Snapshot()
	if caps || !num || scroll {
		t.Fatalf("unexpected coalesced state: caps=%t num=%t scroll=%t", caps, num, scroll)
	}
}

func TestManagerIgnoresTransientCompositorFeedback(t *testing.T) {
	state, writeFD := newFeedbackTestState(t)
	if !state.manager.locks.SetLED(input.LEDCapsLock, false) {
		t.Fatal("failed to establish initial CapsLock state")
	}
	keyboard := make([]int, 2)
	if err := syscall.Pipe2(keyboard, syscall.O_NONBLOCK|syscall.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(keyboard[0]) })
	t.Cleanup(func() { syscall.Close(keyboard[1]) })
	state.byPath["keyboard"] = &physicalDevice{fd: keyboard[1], leds: [3]bool{false, true}}
	writeFeedbackEvents(t, writeFD, []input.Event{
		{Type: input.EVLed, Code: input.LEDCapsLock, Value: 1},
		{Type: input.EVLed, Code: input.LEDCapsLock, Value: 0},
	})
	if err := state.handleFeedbackReady(syscall.EpollEvent{Events: syscall.EPOLLIN}); err != nil {
		t.Fatal(err)
	}
	if _, err := syscall.Read(keyboard[0], make([]byte, kernelEventSize)); !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("unchanged final state synchronized physical LEDs: %v", err)
	}
}

func TestFeedbackLEDUpdatesDoNotRequireSynReport(t *testing.T) {
	state, writeFD := newFeedbackTestState(t)
	writeFeedbackEvents(t, writeFD, []input.Event{
		{Type: input.EVLed, Code: input.LEDCapsLock, Value: 1},
		{Type: input.EVKey, Code: 30, Value: 1},
		{Type: input.EVLed, Code: input.LEDNumLock, Value: 0},
		{Type: input.EVLed, Code: 99, Value: 1},
	})
	if err := state.handleFeedbackReady(syscall.EpollEvent{Events: syscall.EPOLLIN}); err != nil {
		t.Fatal(err)
	}
	caps, num, scroll := state.manager.locks.Snapshot()
	if !caps || num || scroll || !state.manager.locks.Known(input.LEDCapsLock) || !state.manager.locks.Known(input.LEDNumLock) {
		t.Fatalf("unexpected feedback state: caps=%t num=%t scroll=%t", caps, num, scroll)
	}
}

func TestHandleFeedbackReadyDrainsUntilEAGAIN(t *testing.T) {
	state, writeFD := newFeedbackTestState(t)
	if !state.manager.locks.SetLED(input.LEDCapsLock, false) {
		t.Fatal("failed to establish initial CapsLock state")
	}
	events := make([]input.Event, eventBufferSize+1)
	events[0] = input.Event{Type: input.EVLed, Code: input.LEDCapsLock, Value: 1}
	events[len(events)-1] = input.Event{Type: input.EVLed, Code: input.LEDCapsLock, Value: 0}
	writeFeedbackEvents(t, writeFD, events)
	if err := state.handleFeedbackReady(syscall.EpollEvent{Events: syscall.EPOLLIN}); err != nil {
		t.Fatal(err)
	}
	caps, num, scroll := state.manager.locks.Snapshot()
	if caps || num || scroll {
		t.Fatalf("unexpected drained feedback state: caps=%t num=%t scroll=%t", caps, num, scroll)
	}
}

func TestHandleFeedbackReadyRejectsMalformedEvent(t *testing.T) {
	state, writeFD := newFeedbackTestState(t)
	if _, err := syscall.Write(writeFD, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := state.handleFeedbackReady(syscall.EpollEvent{Events: syscall.EPOLLIN}); err == nil {
		t.Fatal("malformed feedback was accepted")
	}
}

func newFeedbackTestState(t *testing.T) (*managerState, int) {
	t.Helper()
	pipe := make([]int, 2)
	if err := syscall.Pipe2(pipe, syscall.O_NONBLOCK|syscall.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(pipe[0]) })
	t.Cleanup(func() { syscall.Close(pipe[1]) })
	m := NewManager(&VirtualKeyboard{name: "test", fd: pipe[0]}, log.New(stdio.Discard, "", 0))
	return &managerState{
		manager:    m,
		readBuffer: make([]byte, kernelEventSize*eventBufferSize),
		byPath:     make(map[string]*physicalDevice),
	}, pipe[1]
}

func writeFeedbackEvents(t *testing.T, fd int, events []input.Event) {
	t.Helper()
	data := make([]byte, 0, len(events)*kernelEventSize)
	for _, event := range events {
		data = append(data, encodeEvent(event)...)
	}
	if _, err := syscall.Write(fd, data); err != nil {
		t.Fatal(err)
	}
}

func TestEpollTimeoutUsesEarliestDeadline(t *testing.T) {
	now := time.Unix(100, 0)
	state := &managerState{
		candidates: make(map[string]*physicalDevice),
		retries:    make(map[string]acquisitionRetry),
	}
	if got := state.epollTimeout(now); got != -1 {
		t.Fatalf("idle timeout = %d, want -1", got)
	}
	state.candidates["kbd"] = &physicalDevice{}
	state.nextCandidateCheck = now.Add(5 * time.Millisecond)
	if got := state.epollTimeout(now); got != 5 {
		t.Fatalf("candidate timeout = %d, want 5", got)
	}
	state.retries["kbd"] = acquisitionRetry{scheduled: true, deadline: now.Add(2 * time.Millisecond)}
	if got := state.epollTimeout(now); got != 2 {
		t.Fatalf("earliest timeout = %d, want 2", got)
	}
	state.retries["ignored"] = acquisitionRetry{deadline: now.Add(time.Nanosecond)}
	state.retries["kbd"] = acquisitionRetry{scheduled: true, deadline: now.Add(-time.Nanosecond)}
	if got := state.epollTimeout(now); got != 0 {
		t.Fatalf("overdue timeout = %d, want 0", got)
	}
	delete(state.retries, "kbd")
	delete(state.candidates, "kbd")
	if got := state.epollTimeout(now); got != -1 {
		t.Fatalf("unscheduled retry timeout = %d, want -1", got)
	}
	state.candidates["kbd"] = &physicalDevice{}
	state.nextCandidateCheck = now.Add(500 * time.Microsecond)
	if got := state.epollTimeout(now); got != 1 {
		t.Fatalf("rounded timeout = %d, want 1", got)
	}
}

func TestManagerWakePipeCancelsIndefiniteWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(&VirtualKeyboard{name: "test", fd: -1}, log.New(stdio.Discard, "", 0))
	state := &managerState{
		manager:        m,
		ctx:            ctx,
		epfd:           epfd,
		monitorFD:      -1,
		wakeRead:       -1,
		wakeWrite:      -1,
		events:         make([]syscall.EpollEvent, 4),
		readBuffer:     make([]byte, kernelEventSize*eventBufferSize),
		byPath:         make(map[string]*physicalDevice),
		candidates:     make(map[string]*physicalDevice),
		retries:        make(map[string]acquisitionRetry),
		latestSequence: make(map[string]uint64),
	}
	if err := state.initWake(); err != nil {
		state.close()
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- state.loop()
	}()
	select {
	case err := <-result:
		t.Fatalf("manager exited before cancellation: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("manager loop returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manager remained blocked after cancellation")
	}
	state.close()
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
	m := NewManager(&VirtualKeyboard{name: "test", fd: -1}, log.New(stdio.Discard, "", 0))
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
