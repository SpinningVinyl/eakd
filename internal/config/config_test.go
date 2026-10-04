// SPDX-License-Identifier: GPL-2.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eak/internal/action"
	"eak/internal/keycode"
)

func TestLoadAndCompile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "eakd.json")
	data := []byte(`{
  "allowed_uids": [1000],
  "allowed_devices": ["ABCD:0123"],
  "prefixes": [{
    "keys": ["LOGO", "T"],
    "bindings": [{"keys": ["1"], "action": "terminal.one"}]
  }]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Prefixes) != 1 || cfg.Prefixes[0].Bindings[0].Action != "terminal.one" {
		t.Fatalf("unexpected compiled configuration: %#v", cfg)
	}
	if len(cfg.AllowedDevices) != 1 || cfg.AllowedDevices[0] != "abcd:0123" {
		t.Fatalf("unexpected allowed devices: %v", cfg.AllowedDevices)
	}
}

func TestCompileAllowedDevices(t *testing.T) {
	for _, id := range []string{"", "123:5678", "12345:5678", "1234:567", "1234:56789", "1234-5678", "1234:xyz0", "+234:5678", " 1234:5678", "1234:5678:9"} {
		t.Run(id, func(t *testing.T) {
			_, err := compile(File{AllowedUIDs: []uint32{1000}, AllowedDevices: []string{id}})
			if err == nil || !strings.Contains(err.Error(), "allowed_devices") {
				t.Fatalf("compile returned %v, want an allowed_devices error", err)
			}
		})
	}
	for _, ids := range [][]string{nil, {}, {"0000:0000", "ffff:FFFF"}} {
		cfg, err := compile(File{
			AllowedUIDs: []uint32{1000}, AllowedDevices: ids,
			Remaps: []FileRemap{{Keys: []string{"LOGO", "HOME"}, Tap: "INSERT"}},
		})
		if err != nil || len(cfg.AllowedDevices) != len(ids) {
			t.Fatalf("compile(%v) returned devices=%v err=%v", ids, cfg.AllowedDevices, err)
		}
	}
}

func TestLoadRejectsMissingOrEmptyAllowedUIDs(t *testing.T) {
	tests := map[string]string{
		"missing": "",
		"empty":   `"allowed_uids": [],`,
	}
	for name, allowedUIDs := range tests {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "eakd.json")
			data := []byte(`{
  ` + allowedUIDs + `
  "prefixes": [{
    "keys": ["LOGO", "T"],
    "bindings": [{"keys": ["1"], "action": "terminal.one"}]
  }]
}`)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path, true); err == nil {
				t.Fatal("Load accepted configuration without an allowed UID")
			}
		})
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "eakd.json")
	data := []byte(`{
  "allowed_uids": [1000],
  "candidate_timeot": "2s",
  "prefixes": [{
    "keys": ["LOGO", "T"],
    "bindings": [{"keys": ["1"], "action": "terminal.one"}]
  }]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, true)
	if err == nil || !strings.Contains(err.Error(), `unknown field "candidate_timeot"`) {
		t.Fatalf("Load returned %v, want an unknown-field error", err)
	}
}

func TestLoadRejectsMultipleJSONValues(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "eakd.json")
	data := []byte(`{
  "allowed_uids": [1000],
  "prefixes": [{
    "keys": ["LOGO", "T"],
    "bindings": [{"keys": ["1"], "action": "terminal.one"}]
  }]
} {}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, true)
	if err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("Load returned %v, want a multiple-JSON-values error", err)
	}
}

func TestLockKeysMayBeConsumedBySequences(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "eakd.json")
	data := []byte(`{
  "allowed_uids": [1000],
  "prefixes": [{
    "keys": ["LOGO", "T"],
    "bindings": [{"keys": ["KEY_CAPSLOCK"], "action": "caps.action"}]
  }]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, true); err != nil {
		t.Fatalf("configuration rejected a compositor-remappable lock key: %v", err)
	}
}

func TestChordSignatureIsIndependentOfKeyOrder(t *testing.T) {
	left := []keycode.Logical{keycode.LogicalLogo, keycode.Logical(keycode.KeyT)}
	right := []keycode.Logical{keycode.Logical(keycode.KeyT), keycode.LogicalLogo}

	if chordSignature(left) != chordSignature(right) {
		t.Fatalf("equivalent chords have different signatures: %q and %q", chordSignature(left), chordSignature(right))
	}
	if left[0] != keycode.LogicalLogo {
		t.Fatal("chordSignature modified its input")
	}
}

func TestLoadRejectsDuplicatePrefixWithDifferentKeyOrder(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "eakd.json")
	data := []byte(`{
  "allowed_uids": [1000],
  "prefixes": [
    {
      "keys": ["LOGO", "T"],
      "bindings": [{"keys": ["1"], "action": "terminal.one"}]
    },
    {
      "keys": ["T", "LOGO"],
      "bindings": [{"keys": ["2"], "action": "terminal.two"}]
    }
  ]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path, true)
	if err == nil || !strings.Contains(err.Error(), "duplicate prefix") {
		t.Fatalf("Load returned %v, want a duplicate-prefix error", err)
	}
}

func TestCompileRejectsOverlongActionID(t *testing.T) {
	raw := File{
		AllowedUIDs: []uint32{1000},
		Prefixes: []FilePrefix{{
			Keys: []string{"LOGO", "T"},
			Bindings: []FileBinding{{
				Keys:   []string{"1"},
				Action: strings.Repeat("a", action.MaxActionIDBytes+1),
			}},
		}},
	}

	if _, err := compile(raw); err == nil || !strings.Contains(err.Error(), "maximum is 1024") {
		t.Fatalf("compile returned %v, want an action-ID length error", err)
	}
}
