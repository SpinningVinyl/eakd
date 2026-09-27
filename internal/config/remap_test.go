// SPDX-License-Identifier: GPL-2.0-or-later

package config

import (
	"testing"

	"eak/internal/keycode"
)

func TestReservedAndHeldConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, tap, hold string
		reserved        []string
		valid           bool
	}{
		{"held", "", "INSERT", []string{"LOGO"}, true},
		{"tap reserved", "INSERT", "", []string{"LOGO"}, true},
		{"both", "INSERT", "INSERT", nil, false},
		{"neither", "", "", nil, false},
		{"held modifier", "", "CTRL", nil, false},
		{"held reserved key", "", "CODE_0", nil, false},
		{"unknown target", "", "invalid", nil, false},
		{"unknown reservation", "INSERT", "", []string{"invalid"}, false},
		{"nonmodifier reservation", "INSERT", "", []string{"HOME"}, false},
		{"unused reservation", "INSERT", "", []string{"CTRL"}, false},
		{"duplicate alias", "INSERT", "", []string{"LOGO", "KEY_LEFTMETA"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := compile(File{AllowedUIDs: []uint32{1000}, ReservedModifiers: tc.reserved,
				Remaps: []FileRemap{{Keys: []string{"LOGO", "HOME"}, Tap: tc.tap, Hold: tc.hold}}})
			if (err == nil) != tc.valid {
				t.Fatalf("error=%v, valid=%t", err, tc.valid)
			}
			if tc.valid {
				want := Tap
				if tc.hold != "" {
					want = Hold
				}
				if cfg.Prefixes[0].Mode != want || cfg.Prefixes[0].Target != keycode.KeyInsert || len(cfg.ReservedModifiers) != 1 || cfg.ReservedModifiers[0] != keycode.LogicalLogo {
					t.Fatalf("incorrect compiled output: %+v", cfg)
				}
			}
		})
	}
}

func TestRemapConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		remaps   []FileRemap
		prefixes []FilePrefix
		valid    bool
	}{
		{"only remaps", []FileRemap{{Keys: []string{"LOGO", "HOME"}, Tap: "INSERT"}}, nil, true},
		{"missing modifier", []FileRemap{{Keys: []string{"HOME"}, Tap: "INSERT"}}, nil, false},
		{"only modifier", []FileRemap{{Keys: []string{"LOGO"}, Tap: "INSERT"}}, nil, false},
		{"modifier output", []FileRemap{{Keys: []string{"LOGO", "HOME"}, Tap: "CTRL"}}, nil, false},
		{"unknown output", []FileRemap{{Keys: []string{"LOGO", "HOME"}, Tap: "unknown"}}, nil, false},
		{"reserved output", []FileRemap{{Keys: []string{"LOGO", "HOME"}, Tap: "CODE_0"}}, nil, false},
		{"duplicate", []FileRemap{{Keys: []string{"LOGO", "HOME"}, Tap: "INSERT"}, {Keys: []string{"HOME", "LOGO"}, Tap: "DELETE"}}, nil, false},
		{"prefix overlap", []FileRemap{{Keys: []string{"LOGO", "HOME"}, Tap: "INSERT"}}, []FilePrefix{{Keys: []string{"LOGO"}, Bindings: []FileBinding{{Keys: []string{"A"}, Action: "test"}}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compile(File{AllowedUIDs: []uint32{1000}, Remaps: tc.remaps, Prefixes: tc.prefixes})
			if (err == nil) != tc.valid {
				t.Fatalf("compile error = %v, valid = %v", err, tc.valid)
			}
		})
	}
}
