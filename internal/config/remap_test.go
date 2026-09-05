// SPDX-License-Identifier: GPL-2.0-or-later

package config

import "testing"

func TestRemapConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		remaps   []FileRemap
		prefixes []FilePrefix
		valid    bool
	}{
		{"only remaps", []FileRemap{{[]string{"LOGO", "HOME"}, "INSERT"}}, nil, true},
		{"missing modifier", []FileRemap{{[]string{"HOME"}, "INSERT"}}, nil, false},
		{"only modifier", []FileRemap{{[]string{"LOGO"}, "INSERT"}}, nil, false},
		{"modifier output", []FileRemap{{[]string{"LOGO", "HOME"}, "CTRL"}}, nil, false},
		{"unknown output", []FileRemap{{[]string{"LOGO", "HOME"}, "unknown"}}, nil, false},
		{"reserved output", []FileRemap{{[]string{"LOGO", "HOME"}, "CODE_0"}}, nil, false},
		{"duplicate", []FileRemap{{[]string{"LOGO", "HOME"}, "INSERT"}, {[]string{"HOME", "LOGO"}, "DELETE"}}, nil, false},
		{"prefix overlap", []FileRemap{{[]string{"LOGO", "HOME"}, "INSERT"}}, []FilePrefix{{Keys: []string{"LOGO"}, Bindings: []FileBinding{{Keys: []string{"A"}, Action: "test"}}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compile(File{AllowedUIDs: []uint32{1000}, Remaps: tc.remaps, Prefixes: tc.prefixes})
			if (err == nil) != tc.valid {
				t.Fatalf("compile error = %v, valid = %v", err, tc.valid)
			}
		})
	}
}
