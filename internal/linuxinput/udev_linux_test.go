//go:build linux

package linuxinput

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestParseKernelDeviceEvent(t *testing.T) {
	message := []byte("add@/devices/test\x00ACTION=add\x00SUBSYSTEM=input\x00DEVNAME=input/event17\x00DEVPATH=/devices/test/input/input1/event17\x00SEQNUM=42\x00")
	event, ok := parseDeviceEvent(message)
	if !ok {
		t.Fatal("input event uevent was rejected")
	}
	if event != (deviceEvent{action: "add", path: "/dev/input/event17", devPath: "/devices/test/input/input1/event17", seqNum: 42}) {
		t.Fatalf("got %#v", event)
	}
}

func TestParsePostRuleUdevEvent(t *testing.T) {
	properties := []byte("ACTION=remove\x00SUBSYSTEM=input\x00DEVNAME=/dev/input/event3\x00DEVPATH=/devices/test/input/input2/event3\x00SEQNUM=99\x00")
	message := make([]byte, udevHeaderSize+len(properties))
	copy(message, []byte("libudev\x00"))
	binary.BigEndian.PutUint32(message[8:12], udevMagic)
	binary.NativeEndian.PutUint32(message[12:16], udevHeaderSize)
	binary.NativeEndian.PutUint32(message[16:20], udevHeaderSize)
	binary.NativeEndian.PutUint32(message[20:24], uint32(len(properties)))
	copy(message[udevHeaderSize:], properties)

	event, ok := parseDeviceEvent(message)
	if !ok || event != (deviceEvent{action: "remove", path: "/dev/input/event3", devPath: "/devices/test/input/input2/event3", seqNum: 99}) {
		t.Fatalf("got %#v, accepted=%t", event, ok)
	}
}

func TestParseDeviceEventRejectsUnrelatedAndUnsafeNodes(t *testing.T) {
	tests := [][]byte{
		[]byte("ACTION=change\x00SUBSYSTEM=input\x00DEVNAME=input/event1\x00DEVPATH=/devices/test/event1\x00SEQNUM=1\x00"),
		[]byte("ACTION=add\x00SUBSYSTEM=sound\x00DEVNAME=input/event1\x00DEVPATH=/devices/test/event1\x00SEQNUM=1\x00"),
		[]byte("ACTION=add\x00SUBSYSTEM=input\x00DEVNAME=input/mouse0\x00DEVPATH=/devices/test/mouse0\x00SEQNUM=1\x00"),
		[]byte("ACTION=add\x00SUBSYSTEM=input\x00DEVNAME=input/event-not-a-number\x00DEVPATH=/devices/test/event1\x00SEQNUM=1\x00"),
		[]byte("ACTION=add\x00SUBSYSTEM=input\x00DEVNAME=input/../event1\x00DEVPATH=/devices/test/event1\x00SEQNUM=1\x00"),
		[]byte("ACTION=add\x00SUBSYSTEM=input\x00DEVNAME=input/event1\x00DEVPATH=/devices/test/event1\x00"),
		[]byte("ACTION=add\x00SUBSYSTEM=input\x00DEVNAME=input/event1\x00DEVPATH=/devices/test/event1\x00SEQNUM=invalid\x00"),
		[]byte("ACTION=add\x00SUBSYSTEM=input\x00DEVNAME=input/event1\x00DEVPATH=../../devices/test/event1\x00SEQNUM=1\x00"),
	}
	for _, message := range tests {
		if event, ok := parseDeviceEvent(message); ok {
			t.Fatalf("accepted %q as %#v", message, event)
		}
	}
}

func TestEventSequenceRejectsDuplicatesAndOlderGenerations(t *testing.T) {
	latest := make(map[string]uint64)
	event := deviceEvent{path: "/dev/input/event4", seqNum: 20}
	if !acceptEventSequence(latest, event) {
		t.Fatal("first event was rejected")
	}
	if acceptEventSequence(latest, event) {
		t.Fatal("duplicate sequence was accepted")
	}
	event.seqNum = 19
	if acceptEventSequence(latest, event) {
		t.Fatal("older sequence was accepted")
	}
	event.seqNum = 21
	if !acceptEventSequence(latest, event) {
		t.Fatal("newer sequence was rejected")
	}
}

func TestGenerationRemovalOrdering(t *testing.T) {
	current := deviceGeneration{devPath: "/devices/current/event4", sequenceFloor: 20, observedAdd: true}
	if generationMatchesRemoval(current, deviceEvent{devPath: current.devPath, seqNum: 19}) {
		t.Fatal("older removal matched current generation")
	}
	if generationMatchesRemoval(current, deviceEvent{devPath: "/devices/old/event4", seqNum: 21}) {
		t.Fatal("different DEVPATH matched current generation")
	}
	if !generationMatchesRemoval(current, deviceEvent{devPath: current.devPath, seqNum: 21}) {
		t.Fatal("newer removal did not match current generation")
	}
}

func TestEnumeratedGenerationAdoptsQueuedAddWithoutMovingSequenceBackward(t *testing.T) {
	enumerated := deviceGeneration{devPath: "/devices/current/event4", sequenceFloor: 30}
	observed := deviceGeneration{devPath: enumerated.devPath, sequenceFloor: 25, observedAdd: true}
	adopted := adoptObservedAdd(enumerated, observed)
	if !adopted.observedAdd || adopted.sequenceFloor != 30 {
		t.Fatalf("got %#v", adopted)
	}
}

func TestSameDeviceNode(t *testing.T) {
	opened := syscall.Stat_t{Dev: 8, Ino: 42, Rdev: 13}
	if !sameDeviceNode(opened, opened) {
		t.Fatal("identical node identities did not match")
	}

	tests := []struct {
		name    string
		current syscall.Stat_t
	}{
		{name: "filesystem", current: syscall.Stat_t{Dev: 9, Ino: 42, Rdev: 13}},
		{name: "inode", current: syscall.Stat_t{Dev: 8, Ino: 43, Rdev: 13}},
		{name: "device number", current: syscall.Stat_t{Dev: 8, Ino: 42, Rdev: 14}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if sameDeviceNode(opened, test.current) {
				t.Fatal("different node identities matched")
			}
		})
	}
}

func TestValidateNodeIdentityRejectsReplacedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event1")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)

	var opened syscall.Stat_t
	if err := syscall.Fstat(fd, &opened); err != nil {
		t.Fatal(err)
	}
	if err := validateNodeIdentity(path, opened); err != nil {
		t.Fatalf("unchanged pathname rejected: %v", err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateNodeIdentity(path, opened); !errors.Is(err, errEventNodeChanged) {
		t.Fatalf("replaced pathname returned %v, want errEventNodeChanged", err)
	}
}

func TestUdevPropertiesRejectsInvalidBounds(t *testing.T) {
	message := make([]byte, udevHeaderSize)
	copy(message, []byte("libudev\x00"))
	binary.BigEndian.PutUint32(message[8:12], udevMagic)
	binary.NativeEndian.PutUint32(message[16:20], udevHeaderSize)
	binary.NativeEndian.PutUint32(message[20:24], 100)
	if _, ok := udevProperties(message); ok {
		t.Fatal("accepted properties extending beyond datagram")
	}
}

func TestRequireRootCredentials(t *testing.T) {
	if err := requireRootCredentials(syscall.UnixCredentials(&syscall.Ucred{Uid: 0})); err != nil {
		t.Fatalf("root credentials rejected: %v", err)
	}
	if err := requireRootCredentials(syscall.UnixCredentials(&syscall.Ucred{Uid: 1000})); err == nil {
		t.Fatal("non-root credentials accepted")
	}
	if err := requireRootCredentials(nil); err == nil {
		t.Fatal("missing credentials accepted")
	}
}
