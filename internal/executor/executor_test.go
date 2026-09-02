// SPDX-License-Identifier: GPL-2.0-or-later

package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eak/internal/clientconfig"
)

func TestRunnerLimitsParallelismAndRunsEveryAction(t *testing.T) {
	const (
		parallelism = 2
		jobCount    = 7
	)
	runner := New(
		map[string]clientconfig.Action{"work": {Command: []string{"work"}}},
		parallelism,
		log.New(io.Discard, "", 0),
	)

	var active atomic.Int32
	var maximum atomic.Int32
	var calls atomic.Int32
	started := make(chan struct{}, jobCount)
	release := make(chan struct{})
	runner.runCommand = func(ctx context.Context, _ clientconfig.Action) error {
		current := active.Add(1)
		calls.Add(1)
		for observed := maximum.Load(); current > observed; observed = maximum.Load() {
			if maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		active.Add(-1)
		return nil
	}

	input := make(chan string, jobCount)
	for range jobCount {
		input <- "work"
	}
	close(input)
	done := make(chan struct{})
	go func() {
		runner.Run(context.Background(), input)
		close(done)
	}()

	for range parallelism {
		<-started
	}
	select {
	case <-started:
		t.Fatal("started more actions than the configured parallelism")
	default:
	}
	close(release)
	waitForRunner(t, done)

	if got := calls.Load(); got != jobCount {
		t.Fatalf("executed %d actions; want %d", got, jobCount)
	}
	if got := maximum.Load(); got != parallelism {
		t.Fatalf("maximum concurrency %d; want %d", got, parallelism)
	}
}

func TestRunnerIgnoresUnknownActionsAndContinuesAfterFailure(t *testing.T) {
	var logs bytes.Buffer
	runner := New(
		map[string]clientconfig.Action{
			"fail": {Command: []string{"fail"}},
			"pass": {Command: []string{"pass"}},
		},
		1,
		log.New(&logs, "", 0),
	)
	var commands []string
	runner.runCommand = func(_ context.Context, action clientconfig.Action) error {
		commands = append(commands, action.Command[0])
		if action.Command[0] == "fail" {
			return errors.New("deliberate failure")
		}
		return nil
	}

	input := make(chan string, 3)
	input <- "unknown"
	input <- "fail"
	input <- "pass"
	close(input)
	runner.Run(context.Background(), input)

	if !slices.Equal(commands, []string{"fail", "pass"}) {
		t.Fatalf("executed commands %q; want [fail pass]", commands)
	}
	for _, message := range []string{
		`ignore unconfigured action "unknown"`,
		`action "fail" failed: deliberate failure`,
		`action "pass" completed`,
	} {
		if !strings.Contains(logs.String(), message) {
			t.Errorf("log does not contain %q:\n%s", message, logs.String())
		}
	}
}

func TestRunnerCancelsAnActiveAction(t *testing.T) {
	runner := New(
		map[string]clientconfig.Action{"wait": {Command: []string{"wait"}}},
		1,
		log.New(io.Discard, "", 0),
	)
	started := make(chan struct{})
	cancelled := make(chan error, 1)
	runner.runCommand = func(ctx context.Context, _ clientconfig.Action) error {
		close(started)
		<-ctx.Done()
		cancelled <- ctx.Err()
		return ctx.Err()
	}

	input := make(chan string, 1)
	input <- "wait"
	close(input)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx, input)
		close(done)
	}()

	<-started
	cancel()
	waitForRunner(t, done)
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("active action received %v; want context.Canceled", err)
	}
}

func TestExecuteCommandPassesArgumentsAndWorkingDirectory(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory := t.TempDir()
	outputPath := filepath.Join(t.TempDir(), "helper-output")
	action := clientconfig.Action{
		Command: []string{
			executable, "-test.run=^TestExecutorHelperProcess$", "--",
			outputPath, "first", "second",
		},
		WorkingDirectory: workingDirectory,
	}

	if err := executeCommand(context.Background(), action); err != nil {
		t.Fatalf("execute helper command: %v", err)
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read helper output: %v", err)
	}
	want := strings.Join([]string{workingDirectory, "first", "second"}, "\n")
	if got := string(contents); got != want {
		t.Fatalf("helper output %q; want %q", got, want)
	}
}

func TestExecutorHelperProcess(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		return
	}
	arguments := os.Args[separator+1:]
	if len(arguments) < 1 {
		t.Fatal("missing helper output path")
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Join(append([]string{workingDirectory}, arguments[1:]...), "\n")
	if err := os.WriteFile(arguments[0], []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForRunner(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop")
	}
}
