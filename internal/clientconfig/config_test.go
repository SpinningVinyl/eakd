// SPDX-License-Identifier: GPL-2.0-or-later

package clientconfig

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"eak/internal/action"
)

func TestLoadClientConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eakc.json")
	data := []byte(`{
  "actions": {
    "terminal.one": {"type": "exec", "command": ["/usr/bin/foot", "--title", "one"]},
    "audio.toggle": {"type": "shell", "script": "wpctl set-mute @DEFAULT_AUDIO_SINK@ toggle"}
  }
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SocketPath != defaultSocketPath || cfg.MaxParallel != defaultMaxParallel || cfg.QueueSize != defaultQueueSize {
		t.Fatalf("defaults not applied: %#v", cfg)
	}
	if got := cfg.Actions["terminal.one"].Command; len(got) != 3 || got[0] != "/usr/bin/foot" {
		t.Fatalf("unexpected exec action: %#v", got)
	}
	if got, want := cfg.Actions["audio.toggle"].Command,
		[]string{"/bin/sh", "-c", "wpctl set-mute @DEFAULT_AUDIO_SINK@ toggle"}; !slices.Equal(got, want) {
		t.Fatalf("shell command = %#v, want %#v", got, want)
	}
}

func TestCompileRejectsAmbiguousActionForms(t *testing.T) {
	tests := []File{
		{Actions: map[string]FileAction{"bad": {Type: "exec", Command: []string{"true"}, Script: "true"}}},
		{Actions: map[string]FileAction{"bad": {Type: "shell", Script: ""}}},
		{Actions: map[string]FileAction{"bad": {Type: "native"}}},
	}
	for _, raw := range tests {
		if _, err := compile(raw); err == nil {
			t.Fatalf("accepted invalid configuration: %#v", raw)
		}
	}
}

func TestCompileRejectsOverlongActionID(t *testing.T) {
	id := strings.Repeat("a", action.MaxActionIDBytes+1)
	raw := File{Actions: map[string]FileAction{
		id: {Type: "exec", Command: []string{"true"}},
	}}

	if _, err := compile(raw); err == nil || !strings.Contains(err.Error(), "maximum is 1024") {
		t.Fatalf("compile returned %v, want an action-ID length error", err)
	}
}
