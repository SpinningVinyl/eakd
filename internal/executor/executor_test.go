package executor

import (
	"context"
	"testing"

	"eak/internal/clientconfig"
)

func TestBuildExecCommandPreservesArguments(t *testing.T) {
	command, err := buildCommand(context.Background(), clientconfig.Action{
		Type: "exec", Command: []string{"/bin/echo", "one two", "$HOME"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/echo", "one two", "$HOME"}
	if len(command.Args) != len(want) {
		t.Fatalf("got %#v", command.Args)
	}
	for i := range want {
		if command.Args[i] != want[i] {
			t.Fatalf("argument %d = %q, want %q", i, command.Args[i], want[i])
		}
	}
}

func TestBuildShellCommandUsesPOSIXShell(t *testing.T) {
	command, err := buildCommand(context.Background(), clientconfig.Action{Type: "shell", Script: "echo ok"})
	if err != nil {
		t.Fatal(err)
	}
	if len(command.Args) != 3 || command.Args[0] != "/bin/sh" || command.Args[1] != "-c" || command.Args[2] != "echo ok" {
		t.Fatalf("got %#v", command.Args)
	}
}
