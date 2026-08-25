// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package linuxinput

import (
	"testing"
	"unsafe"

	"eak/internal/input"
)

func TestInputEventEncodingRoundTrip(t *testing.T) {
	wanted := input.Event{Type: input.EVKey, Code: 125, Value: 1}
	decoded, err := decodeEvents(append([]byte(nil), encodeEvent(wanted)...))
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0] != wanted {
		t.Fatalf("round trip: got %#v, want %#v", decoded, wanted)
	}
}

func TestGenericIoctlNumbers(t *testing.T) {
	if got, want := iow(evdevBase, 0x90, unsafe.Sizeof(int32(0))), uintptr(0x40044590); got != want {
		t.Fatalf("EVIOCGRAB = %#x, want %#x", got, want)
	}
	if got, want := iow(uinputBase, 100, unsafe.Sizeof(int32(0))), uintptr(0x40045564); got != want {
		t.Fatalf("UI_SET_EVBIT = %#x, want %#x", got, want)
	}
}
