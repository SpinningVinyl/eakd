// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package linuxinput

import (
	"context"
	"errors"
	stdio "io"
	"log"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"eak/internal/input"
)

func TestScheduleRetryTracksLimitsAndNewGenerations(t *testing.T) {
	state := newManagerTestState(context.Background(), make(chan input.Message, 1))
	path := "/dev/input/event7"
	first := deviceGeneration{devPath: "/devices/first", sequenceFloor: 10}
	before := time.Now()
	state.scheduleRetry(path, first)
	retry := state.retries[path]
	if !retry.scheduled || retry.generation != first || retry.attempts != 0 {
		t.Fatalf("initial retry = %#v", retry)
	}
	if retry.deadline.Before(before.Add(acquisitionRetryDelay)) {
		t.Fatalf("retry deadline %v is earlier than expected", retry.deadline)
	}

	retry.attempts = maxReacquisitionTries
	state.retries[path] = retry
	state.scheduleRetry(path, first)
	if state.retries[path].scheduled {
		t.Fatal("scheduled an exhausted generation")
	}

	second := deviceGeneration{devPath: "/devices/second", sequenceFloor: 20}
	state.scheduleRetry(path, second)
	retry = state.retries[path]
	if !retry.scheduled || retry.generation != second || retry.attempts != 0 {
		t.Fatalf("new-generation retry = %#v", retry)
	}
}

func TestBeginRetryCountsTrackedAttemptsAndResetsForReplacement(t *testing.T) {
	state := newManagerTestState(context.Background(), make(chan input.Message, 1))
	path := "/dev/input/event7"
	first := deviceGeneration{devPath: "/devices/first", sequenceFloor: 10}
	state.retries[path] = acquisitionRetry{
		generation: first, attempts: 2, scheduled: true,
	}
	if !state.beginRetry(path, first) {
		t.Fatal("tracked retry was rejected")
	}
	if retry := state.retries[path]; retry.attempts != 3 || retry.scheduled {
		t.Fatalf("retry after begin = %#v", retry)
	}

	second := deviceGeneration{devPath: "/devices/second", sequenceFloor: 20}
	if !state.beginRetry(path, second) {
		t.Fatal("replacement generation was rejected")
	}
	if retry := state.retries[path]; retry.generation != second || retry.attempts != 1 {
		t.Fatalf("replacement retry = %#v", retry)
	}

	state.retries[path] = acquisitionRetry{
		generation: second, attempts: maxReacquisitionTries,
	}
	if state.beginRetry(path, second) {
		t.Fatal("retry beyond the limit was accepted")
	}
}

func TestProcessRetriesRunsOnlyDueEntries(t *testing.T) {
	state := newManagerTestState(context.Background(), make(chan input.Message, 1))
	now := time.Now()
	duePath := filepath.Join(t.TempDir(), "missing-event")
	dueGeneration := deviceGeneration{devPath: "/devices/due", sequenceFloor: 10}
	state.retries[duePath] = acquisitionRetry{
		generation: dueGeneration, attempts: 1, scheduled: true, deadline: now.Add(-time.Second),
	}
	futurePath := filepath.Join(t.TempDir(), "future-event")
	future := acquisitionRetry{
		generation: deviceGeneration{devPath: "/devices/future"},
		attempts:   3, scheduled: true, deadline: now.Add(time.Hour),
	}
	state.retries[futurePath] = future

	if err := state.processRetries(now); err != nil {
		t.Fatal(err)
	}
	due := state.retries[duePath]
	if due.attempts != 2 || !due.scheduled || due.generation != dueGeneration {
		t.Fatalf("processed retry = %#v", due)
	}
	if got := state.retries[futurePath]; got != future {
		t.Fatalf("future retry changed from %#v to %#v", future, got)
	}
}

func TestCheckCandidatesClosesAndRetriesQueryFailures(t *testing.T) {
	state := newManagerTestState(context.Background(), make(chan input.Message, 1))
	readFD, writeFD := nonblockingPipe(t)
	defer syscall.Close(writeFD)
	path := "/dev/input/event-test"
	generation := deviceGeneration{devPath: "/devices/test", sequenceFloor: 10}
	state.candidates[path] = &physicalDevice{path: path, generation: generation, fd: readFD}
	now := time.Now()

	if err := state.checkCandidates(now); err != nil {
		t.Fatal(err)
	}
	if len(state.candidates) != 0 {
		t.Fatal("failed candidate remained registered")
	}
	if retry := state.retries[path]; !retry.scheduled || retry.generation != generation {
		t.Fatalf("retry = %#v", retry)
	}
	if state.nextCandidateCheck != now.Add(candidateCheckInterval) {
		t.Fatalf("next candidate check = %v", state.nextCandidateCheck)
	}
	if _, err := syscall.Read(readFD, make([]byte, 1)); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("candidate descriptor remained open: %v", err)
	}
}

func TestHandleRemoveClearsMatchingState(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		state := newManagerTestState(context.Background(), make(chan input.Message, 1))
		path := "/dev/input/event7"
		generation := deviceGeneration{devPath: "/devices/test", sequenceFloor: 10}
		state.retries[path] = acquisitionRetry{generation: generation, scheduled: true}
		if err := state.handleRemove(removalEvent(path, generation, 11)); err != nil {
			t.Fatal(err)
		}
		if _, exists := state.retries[path]; exists {
			t.Fatal("matching retry survived removal")
		}
	})

	t.Run("candidate", func(t *testing.T) {
		state := newManagerTestState(context.Background(), make(chan input.Message, 1))
		readFD, writeFD := nonblockingPipe(t)
		defer syscall.Close(writeFD)
		path := "/dev/input/event7"
		generation := deviceGeneration{devPath: "/devices/test", sequenceFloor: 10}
		state.candidates[path] = &physicalDevice{path: path, generation: generation, fd: readFD}
		if err := state.handleRemove(removalEvent(path, generation, 11)); err != nil {
			t.Fatal(err)
		}
		if _, exists := state.candidates[path]; exists {
			t.Fatal("matching candidate survived removal")
		}
		if _, err := syscall.Read(readFD, make([]byte, 1)); !errors.Is(err, syscall.EBADF) {
			t.Fatalf("candidate descriptor remained open: %v", err)
		}
	})

	t.Run("active", func(t *testing.T) {
		output := make(chan input.Message, 1)
		state := newManagerTestState(context.Background(), output)
		readFD, writeFD := nonblockingPipe(t)
		defer syscall.Close(writeFD)
		path := "/dev/input/event7"
		generation := deviceGeneration{devPath: "/devices/test", sequenceFloor: 10}
		device := &physicalDevice{path: path, generation: generation, fd: readFD}
		state.byPath[path] = device
		state.byFD[readFD] = device
		if err := state.handleRemove(removalEvent(path, generation, 11)); err != nil {
			t.Fatal(err)
		}
		if len(state.byPath) != 0 || len(state.byFD) != 0 {
			t.Fatal("matching active device survived removal")
		}
		message := <-output
		if message.Removed != path {
			t.Fatalf("removal message = %#v", message)
		}
	})
}

func TestHandleRemoveIgnoresStaleGeneration(t *testing.T) {
	state := newManagerTestState(context.Background(), make(chan input.Message, 1))
	path := "/dev/input/event7"
	generation := deviceGeneration{devPath: "/devices/current", sequenceFloor: 20}
	state.retries[path] = acquisitionRetry{generation: generation, scheduled: true}

	if err := state.handleRemove(removalEvent(path, generation, 19)); err != nil {
		t.Fatal(err)
	}
	if _, exists := state.retries[path]; !exists {
		t.Fatal("older removal cleared the current generation")
	}
}

func TestHandleAddAdoptsObservedGeneration(t *testing.T) {
	for _, collection := range []string{"active", "candidate"} {
		t.Run(collection, func(t *testing.T) {
			state := newManagerTestState(context.Background(), make(chan input.Message, 1))
			path := "/dev/input/event7"
			device := &physicalDevice{
				path:       path,
				generation: deviceGeneration{devPath: "/devices/test", sequenceFloor: 30},
				fd:         -1,
			}
			if collection == "active" {
				state.byPath[path] = device
			} else {
				state.candidates[path] = device
			}
			event := deviceEvent{
				action: "add", path: path, devPath: device.generation.devPath, seqNum: 25,
			}
			if err := state.handleAdd(event); err != nil {
				t.Fatal(err)
			}
			if !device.generation.observedAdd || device.generation.sequenceFloor != 30 {
				t.Fatalf("adopted generation = %#v", device.generation)
			}
		})
	}
}

func TestHandleDeviceHangupRemovesAndSchedulesRetry(t *testing.T) {
	output := make(chan input.Message, 1)
	state := newManagerTestState(context.Background(), output)
	readFD, writeFD := nonblockingPipe(t)
	defer syscall.Close(writeFD)
	path := "/dev/input/event7"
	generation := deviceGeneration{devPath: "/devices/test", sequenceFloor: 10}
	device := &physicalDevice{path: path, generation: generation, fd: readFD}
	state.byPath[path] = device
	state.byFD[readFD] = device

	if err := state.handleDeviceReady(syscall.EpollEvent{Fd: int32(readFD), Events: syscall.EPOLLHUP}); err != nil {
		t.Fatal(err)
	}
	if len(state.byPath) != 0 || len(state.byFD) != 0 {
		t.Fatal("hung-up device remained active")
	}
	if retry := state.retries[path]; !retry.scheduled || retry.generation != generation {
		t.Fatalf("retry = %#v", retry)
	}
	if message := <-output; message.Removed != path {
		t.Fatalf("removal message = %#v", message)
	}
}

func TestWakeReadyRequiresCancellation(t *testing.T) {
	state := newManagerTestState(context.Background(), make(chan input.Message, 1))
	if err := state.handleWakeReady(); err == nil {
		t.Fatal("unexpected wake was accepted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state.ctx = ctx
	if err := state.handleWakeReady(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wake returned %v", err)
	}
}

func newManagerTestState(ctx context.Context, output chan<- input.Message) *managerState {
	manager := NewManager(&VirtualKeyboard{name: "test", fd: -2}, log.New(stdio.Discard, "", 0))
	return &managerState{
		manager: manager, ctx: ctx, output: output, epfd: -1, monitorFD: -3,
		wakeRead: -4, wakeWrite: -1,
		byPath: make(map[string]*physicalDevice), byFD: make(map[int]*physicalDevice),
		candidates: make(map[string]*physicalDevice), retries: make(map[string]acquisitionRetry),
		latestSequence: make(map[string]uint64),
		readBuffer:     make([]byte, kernelEventSize*eventBufferSize),
	}
}

func nonblockingPipe(t *testing.T) (int, int) {
	t.Helper()
	var pipe [2]int
	if err := syscall.Pipe2(pipe[:], syscall.O_NONBLOCK|syscall.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	return pipe[0], pipe[1]
}

func removalEvent(path string, generation deviceGeneration, sequence uint64) deviceEvent {
	return deviceEvent{
		action: "remove", path: path, devPath: generation.devPath, seqNum: sequence,
	}
}
