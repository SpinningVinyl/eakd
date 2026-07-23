package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndCompile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "eakd.json")
	data := []byte(`{
  "allowed_uids": [1000],
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
