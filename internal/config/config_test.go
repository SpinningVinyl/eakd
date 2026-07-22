package config

import (
	"os"
	"path/filepath"
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
