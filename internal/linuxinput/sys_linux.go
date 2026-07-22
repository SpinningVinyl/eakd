//go:build linux

package linuxinput

import (
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"eak/internal/input"
	"eak/internal/keycode"
)

const (
	evdevBase  = uintptr('E')
	uinputBase = uintptr('U')

	iocNRBits    = 8
	iocTypeBits  = 8
	iocSizeBits  = 14
	iocNRShift   = 0
	iocTypeShift = iocNRShift + iocNRBits
	iocSizeShift = iocTypeShift + iocTypeBits
	iocDirShift  = iocSizeShift + iocSizeBits

	iocNone  = 0
	iocWrite = 1
	iocRead  = 2

	keyBitmapBytes  = (keycode.KeyMax + 8) / 8
	eventBufferSize = 64
)

// The _IOC layout below is Linux's asm-generic ioctl encoding, used by the
// architectures supported by this project (amd64 and arm64).
func ioc(direction, kind, number, size uintptr) uintptr {
	return direction<<iocDirShift |
		kind<<iocTypeShift |
		number<<iocNRShift |
		size<<iocSizeShift
}

func io(kind, number uintptr) uintptr {
	return ioc(iocNone, kind, number, 0)
}

func iow(kind, number, size uintptr) uintptr {
	return ioc(iocWrite, kind, number, size)
}

func ior(kind, number, size uintptr) uintptr {
	return ioc(iocRead, kind, number, size)
}

func eviocgbit(eventType, size uintptr) uintptr {
	return ior(evdevBase, 0x20+eventType, size)
}

func eviocgkey(size uintptr) uintptr {
	return ior(evdevBase, 0x18, size)
}

func eviocgname(size uintptr) uintptr {
	return ior(evdevBase, 0x06, size)
}

func ioctl(fd int, request uintptr, argument unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(argument))
	if errno != 0 {
		return errno
	}
	return nil
}

func ioctlInt(fd int, request uintptr, value int32) error {
	// The uinput bit-setting ioctls and EVIOCGRAB take their integer as the
	// ioctl's scalar third argument. Despite being encoded with _IOW(..., int),
	// they do not take a pointer to an int.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}

func ioctlNoArg(fd int, request uintptr) error {
	return ioctl(fd, request, nil)
}

type kernelInputEvent struct {
	Time  syscall.Timeval
	Type  uint16
	Code  uint16
	Value int32
}

var kernelEventSize = int(unsafe.Sizeof(kernelInputEvent{}))

func decodeEvents(data []byte) ([]input.Event, error) {
	if len(data)%kernelEventSize != 0 {
		return nil, fmt.Errorf("short evdev record: got %d bytes, event size is %d", len(data), kernelEventSize)
	}
	result := make([]input.Event, 0, len(data)/kernelEventSize)
	for len(data) != 0 {
		var raw kernelInputEvent
		if err := binary.Read(bytesReader(data[:kernelEventSize]), binary.NativeEndian, &raw); err != nil {
			return nil, err
		}
		result = append(result, input.Event{Type: raw.Type, Code: raw.Code, Value: raw.Value})
		data = data[kernelEventSize:]
	}
	return result, nil
}

// byteSliceReader avoids importing bytes and allocating a Reader for every
// kernel event batch.
type byteSliceReader []byte

func bytesReader(data []byte) *byteSliceReader {
	r := byteSliceReader(data)
	return &r
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if len(*r) == 0 {
		return 0, errors.New("end of input event")
	}
	n := copy(p, *r)
	*r = (*r)[n:]
	return n, nil
}

func encodeEvent(event input.Event) []byte {
	raw := kernelInputEvent{Type: event.Type, Code: event.Code, Value: event.Value}
	return unsafe.Slice((*byte)(unsafe.Pointer(&raw)), kernelEventSize)
}

func bitIsSet(bits []byte, bit uint16) bool {
	index := int(bit / 8)
	return index < len(bits) && bits[index]&(1<<uint(bit%8)) != 0
}

func deviceName(fd int) (string, error) {
	buffer := make([]byte, 256)
	if err := ioctl(fd, eviocgname(uintptr(len(buffer))), unsafe.Pointer(&buffer[0])); err != nil {
		return "", err
	}
	for i, b := range buffer {
		if b == 0 {
			return string(buffer[:i]), nil
		}
	}
	return string(buffer), nil
}

func keyCapabilities(fd int) ([]byte, error) {
	bits := make([]byte, keyBitmapBytes)
	if err := ioctl(fd, eviocgbit(input.EVKey, uintptr(len(bits))), unsafe.Pointer(&bits[0])); err != nil {
		return nil, err
	}
	return bits, nil
}

func ledCapabilities(fd int) ([]byte, error) {
	bits := make([]byte, 1)
	if err := ioctl(fd, eviocgbit(input.EVLed, uintptr(len(bits))), unsafe.Pointer(&bits[0])); err != nil {
		return nil, err
	}
	return bits, nil
}

func writeEventFD(fd int, event input.Event) error {
	data := encodeEvent(event)
	retries := 0
	for len(data) != 0 {
		written, err := syscall.Write(fd, data)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				retries++
				if retries >= 100 {
					return fmt.Errorf("input event output remained unavailable for 100ms: %w", err)
				}
				time.Sleep(time.Millisecond)
				continue
			}
			return err
		}
		if written == 0 {
			return errors.New("zero-length input event write")
		}
		data = data[written:]
	}
	return nil
}

func currentKeys(fd int) (map[uint16]bool, error) {
	bits := make([]byte, keyBitmapBytes)
	if err := ioctl(fd, eviocgkey(uintptr(len(bits))), unsafe.Pointer(&bits[0])); err != nil {
		return nil, err
	}
	pressed := make(map[uint16]bool)
	for code := uint16(0); code <= keycode.KeyMax; code++ {
		if bitIsSet(bits, code) {
			pressed[code] = true
		}
	}
	return pressed, nil
}

// discardQueuedEvents drains this evdev client's private queue. It is used
// before a handoff because events queued while the device was not grabbed were
// already visible to the compositor and must not be replayed through uinput.
func discardQueuedEvents(fd int) error {
	buffer := make([]byte, kernelEventSize*eventBufferSize)
	for {
		count, err := syscall.Read(fd, buffer)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				return nil
			}
			return err
		}
		if count == 0 {
			return syscall.ENODEV
		}
	}
}

func looksLikeKeyboard(capabilities []byte) bool {
	return bitIsSet(capabilities, keycode.KeyA) &&
		bitIsSet(capabilities, keycode.KeyZ) &&
		bitIsSet(capabilities, keycode.KeySpace) &&
		bitIsSet(capabilities, keycode.KeyEnter)
}

func grab(fd int, enabled bool) error {
	value := int32(0)
	if enabled {
		value = 1
	}
	return ioctlInt(fd, iow(evdevBase, 0x90, unsafe.Sizeof(value)), value)
}
