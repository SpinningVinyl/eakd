// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package linuxinput

import (
	"fmt"
	"slices"
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

func TestDecodeEventsRejectsTruncatedRecords(t *testing.T) {
	for _, size := range []int{1, kernelEventSize - 1, kernelEventSize + 1} {
		t.Run(fmt.Sprintf("%d_bytes", size), func(t *testing.T) {
			if _, err := decodeEvents(make([]byte, size)); err == nil {
				t.Fatalf("accepted %d-byte input", size)
			}
		})
	}
}

func TestDecodeEventsAcceptsEmptyAndMultipleRecords(t *testing.T) {
	events, err := decodeEvents(nil)
	if err != nil || len(events) != 0 {
		t.Fatalf("empty input returned events=%#v err=%v", events, err)
	}
	want := []input.Event{
		{Type: input.EVKey, Code: 30, Value: 1},
		{Type: input.EVSyn, Code: input.SynReport},
	}
	data := append(append([]byte(nil), encodeEvent(want[0])...), encodeEvent(want[1])...)
	events, err = decodeEvents(data)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %#v; want %#v", events, want)
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
