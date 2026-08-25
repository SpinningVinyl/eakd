// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package linuxinput

import (
	"eak/internal/input"
)

// LockState mirrors the compositor-authoritative global lock/LED state. Its
// zero value is unknown, so eakd does not impose a state before feedback has
// arrived through the virtual keyboard.
type LockState struct {
	values [3]bool
	known  [3]bool
}

func (s *LockState) LED(code uint16) bool {
	return code <= input.LEDScrollLock && s.values[code]
}

func (s *LockState) Known(code uint16) bool {
	return code <= input.LEDScrollLock && s.known[code]
}

func (s *LockState) SetLED(code uint16, enabled bool) bool {
	if code > input.LEDScrollLock {
		return false
	}
	changed := !s.known[code] || s.values[code] != enabled
	s.known[code] = true
	s.values[code] = enabled
	return changed
}

func (s *LockState) Snapshot() (caps, num, scroll bool) {
	return s.LED(input.LEDCapsLock), s.LED(input.LEDNumLock), s.LED(input.LEDScrollLock)
}
