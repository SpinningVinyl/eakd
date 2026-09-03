// SPDX-License-Identifier: GPL-2.0-or-later

package keycode

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseModifierAliases(t *testing.T) {
	tests := map[string]Logical{
		"CTRL": LogicalCtrl, "CONTROL": LogicalCtrl,
		"SHIFT": LogicalShift,
		"ALT":   LogicalAlt,
		"LOGO":  LogicalLogo, "META": LogicalLogo, "SUPER": LogicalLogo,
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			assertParsedKey(t, "  "+strings.ToLower(name)+"  ", want)
		})
	}
}

func TestParseLettersAndDigits(t *testing.T) {
	for index, code := range letterCodes {
		letter := string(rune('A' + index))
		t.Run(letter, func(t *testing.T) {
			assertParsedKey(t, letter, Logical(code))
			assertParsedKey(t, "KEY_"+letter, Logical(code))
		})
	}
	for digit := byte('0'); digit <= '9'; digit++ {
		name := string(digit)
		want := Logical(Key0)
		if digit != '0' {
			want = Logical(Key1 + uint16(digit-'1'))
		}
		t.Run(name, func(t *testing.T) {
			assertParsedKey(t, name, want)
			assertParsedKey(t, "KEY_"+name, want)
		})
	}
}

func TestParseFunctionKeys(t *testing.T) {
	for number := 1; number <= 12; number++ {
		want := Logical(KeyF1 + uint16(number-1))
		if number > 10 {
			want = Logical(KeyF11 + uint16(number-11))
		}
		t.Run(fmt.Sprintf("F%d", number), func(t *testing.T) {
			assertParsedKey(t, fmt.Sprintf("f%d", number), want)
		})
	}
}

func TestParseFixedAndKeypadNames(t *testing.T) {
	for name, code := range fixedNames {
		t.Run(name, func(t *testing.T) {
			assertParsedKey(t, strings.ToLower(name), Canonical(code))
			if strings.HasPrefix(name, "KEY_KP") {
				assertParsedKey(t, strings.TrimPrefix(name, "KEY_"), Canonical(code))
			}
		})
	}
}

func TestParseNumericCodes(t *testing.T) {
	tests := map[string]Logical{
		"CODE_0":                       0,
		"code_30":                      KeyA,
		"CODE_29":                      LogicalCtrl,
		fmt.Sprintf("CODE_%d", KeyMax): KeyMax,
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			assertParsedKey(t, name, want)
		})
	}
}

func TestParseRejectsInvalidNames(t *testing.T) {
	for _, name := range []string{
		"", "   ", "F0", "F13", "FX", "KEY_AB", "KEY_F1", "KP10",
		"CODE_", "CODE_-1", fmt.Sprintf("CODE_%d", KeyMax+1), "CODE_12x", "UNKNOWN",
	} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			if key, err := Parse(name); err == nil {
				t.Fatalf("Parse(%q) = %v, nil; want error", name, key)
			}
		})
	}
}

func TestCanonicalCombinesLeftAndRightModifiers(t *testing.T) {
	tests := map[uint16]Logical{
		KeyLeftCtrl: LogicalCtrl, KeyRightCtrl: LogicalCtrl,
		KeyLeftShift: LogicalShift, KeyRightShift: LogicalShift,
		KeyLeftAlt: LogicalAlt, KeyRightAlt: LogicalAlt,
		KeyLeftMeta: LogicalLogo, KeyRightMeta: LogicalLogo,
		KeyA: KeyA,
	}
	for code, want := range tests {
		if got := Canonical(code); got != want {
			t.Errorf("Canonical(%d) = %d; want %d", code, got, want)
		}
	}
}

func TestModifierPredicates(t *testing.T) {
	for _, key := range []Logical{LogicalCtrl, LogicalShift, LogicalAlt, LogicalLogo} {
		if !IsLogicalModifier(key) {
			t.Errorf("logical modifier %d was rejected", key)
		}
	}
	for _, key := range []Logical{0, KeyA, logicalBase - 1, LogicalLogo + 1} {
		if IsLogicalModifier(key) {
			t.Errorf("non-modifier %d was accepted as logical modifier", key)
		}
	}

	physical := []uint16{
		KeyLeftCtrl, KeyRightCtrl, KeyLeftShift, KeyRightShift,
		KeyLeftAlt, KeyRightAlt, KeyLeftMeta, KeyRightMeta,
	}
	for _, code := range physical {
		if !IsPhysicalModifier(code) {
			t.Errorf("physical modifier %d was rejected", code)
		}
	}
	for _, code := range []uint16{0, KeyA, KeyCapsLock, KeyCompose} {
		if IsPhysicalModifier(code) {
			t.Errorf("non-modifier %d was accepted as physical modifier", code)
		}
	}
}

func assertParsedKey(t *testing.T, name string, want Logical) {
	t.Helper()
	got, err := Parse(name)
	if err != nil {
		t.Fatalf("Parse(%q): %v", name, err)
	}
	if got != want {
		t.Fatalf("Parse(%q) = %d; want %d", name, got, want)
	}
}
