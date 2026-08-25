// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package linuxinput

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"eak/internal/input"
	"eak/internal/keycode"
)

const (
	uinputMaxName = 80
	busVirtual    = 0x06
)

type inputID struct {
	BusType uint16
	Vendor  uint16
	Product uint16
	Version uint16
}

type uinputSetup struct {
	ID           inputID
	Name         [uinputMaxName]byte
	FFEffectsMax uint32
}

type VirtualKeyboard struct {
	fd   int
	name string
}

func CreateVirtualKeyboard(name string) (*VirtualKeyboard, error) {
	fd, err := syscall.Open("/dev/uinput", syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/uinput: %w", err)
	}
	fail := func(err error) (*VirtualKeyboard, error) {
		syscall.Close(fd)
		return nil, err
	}

	intSize := unsafe.Sizeof(int32(0))
	if err := ioctlInt(fd, iow(uinputBase, 100, intSize), input.EVKey); err != nil {
		return fail(fmt.Errorf("UI_SET_EVBIT EV_KEY: %w", err))
	}
	for code := int32(0); code <= keycode.KeyMax; code++ {
		if err := ioctlInt(fd, iow(uinputBase, 101, intSize), code); err != nil {
			return fail(fmt.Errorf("UI_SET_KEYBIT %d: %w", code, err))
		}
	}
	if err := ioctlInt(fd, iow(uinputBase, 100, intSize), input.EVMsc); err != nil {
		return fail(fmt.Errorf("UI_SET_EVBIT EV_MSC: %w", err))
	}
	if err := ioctlInt(fd, iow(uinputBase, 104, intSize), input.MscScan); err != nil {
		return fail(fmt.Errorf("UI_SET_MSCBIT MSC_SCAN: %w", err))
	}
	if err := ioctlInt(fd, iow(uinputBase, 100, intSize), input.EVLed); err != nil {
		return fail(fmt.Errorf("UI_SET_EVBIT EV_LED: %w", err))
	}
	for code := int32(input.LEDNumLock); code <= input.LEDScrollLock; code++ {
		if err := ioctlInt(fd, iow(uinputBase, 105, intSize), code); err != nil {
			return fail(fmt.Errorf("UI_SET_LEDBIT %d: %w", code, err))
		}
	}

	setup := uinputSetup{ID: inputID{BusType: busVirtual, Vendor: 0x4541, Product: 0x4b44, Version: 1}}
	copy(setup.Name[:], name)
	if err := ioctl(fd, iow(uinputBase, 3, unsafe.Sizeof(setup)), unsafe.Pointer(&setup)); err != nil {
		return fail(fmt.Errorf("UI_DEV_SETUP: %w", err))
	}
	if err := ioctlNoArg(fd, io(uinputBase, 1)); err != nil {
		return fail(fmt.Errorf("UI_DEV_CREATE: %w", err))
	}
	keyboard := &VirtualKeyboard{fd: fd, name: name}
	if err := keyboard.waitUntilVisible(3 * time.Second); err != nil {
		keyboard.Close()
		return nil, err
	}
	// The event node exists at this point. Give the compositor's udev monitor
	// one short scheduling window before physical keyboards are grabbed.
	time.Sleep(100 * time.Millisecond)
	return keyboard, nil
}

func (v *VirtualKeyboard) Write(event input.Event) error {
	if err := writeEventFD(v.fd, event); err != nil {
		return fmt.Errorf("write uinput event: %w", err)
	}
	return nil
}

func (v *VirtualKeyboard) Close() error {
	if v.fd < 0 {
		return nil
	}
	destroyErr := ioctlNoArg(v.fd, io(uinputBase, 2))
	closeErr := syscall.Close(v.fd)
	v.fd = -1
	if destroyErr != nil {
		return fmt.Errorf("UI_DEV_DESTROY: %w", destroyErr)
	}
	return closeErr
}

func (v *VirtualKeyboard) waitUntilVisible(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		paths, _ := filepath.Glob("/sys/class/input/event*/device/name")
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err == nil && string(data) == v.name+"\n" {
				return nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("virtual keyboard %q did not appear in sysfs", v.name)
}
