// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package linuxinput

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	udevGroup      = 2
	udevMagic      = 0xfeedcafe
	udevHeaderSize = 40
	udevBufferSize = 64 * 1024
)

var errKernelUeventSequence = errors.New("kernel uevent sequence unavailable")

type deviceEvent struct {
	action  string
	path    string
	devPath string
	seqNum  uint64
}

type deviceGeneration struct {
	devPath       string
	sequenceFloor uint64
	observedAdd   bool
}

// openUdevMonitor subscribes to post-rule udev events. At that point device
// ownership and permissions have been applied, and there is only one event
// stream whose SEQNUM can order successive generations of a reused event node.
func openUdevMonitor() (int, error) {
	fd, err := syscall.Socket(
		syscall.AF_NETLINK,
		syscall.SOCK_RAW|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC,
		syscall.NETLINK_KOBJECT_UEVENT,
	)
	if err != nil {
		return -1, fmt.Errorf("socket NETLINK_KOBJECT_UEVENT: %w", err)
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1); err != nil {
		syscall.Close(fd)
		return -1, fmt.Errorf("enable udev sender credentials: %w", err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
		Groups: udevGroup,
	}); err != nil {
		syscall.Close(fd)
		return -1, fmt.Errorf("bind NETLINK_KOBJECT_UEVENT: %w", err)
	}
	return fd, nil
}

func readDeviceEvents(fd int, buffer []byte) ([]deviceEvent, error) {
	var result []deviceEvent
	control := make([]byte, syscall.CmsgSpace(syscall.SizeofUcred))
	for {
		count, controlCount, flags, _, err := syscall.Recvmsg(fd, buffer, control, syscall.MSG_DONTWAIT)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				return result, nil
			}
			return result, err
		}
		if flags&(syscall.MSG_TRUNC|syscall.MSG_CTRUNC) != 0 {
			// Ignore incomplete datagrams rather than acting on partial
			// properties; normal input uevents are tiny.
			continue
		}
		if err := requireRootCredentials(control[:controlCount]); err != nil {
			continue
		}
		if event, ok := parseDeviceEvent(buffer[:count]); ok {
			result = append(result, event)
		}
	}
}

func requireRootCredentials(control []byte) error {
	messages, err := syscall.ParseSocketControlMessage(control)
	if err != nil {
		return err
	}
	for _, message := range messages {
		credentials, credErr := syscall.ParseUnixCredentials(&message)
		if credErr == nil {
			if credentials.Uid != 0 {
				return syscall.EPERM
			}
			return nil
		}
	}
	return errors.New("udev message has no sender credentials")
}

func parseDeviceEvent(message []byte) (deviceEvent, bool) {
	properties, ok := udevProperties(message)
	if !ok {
		return deviceEvent{}, false
	}
	values := make(map[string]string)
	for _, field := range bytes.Split(properties, []byte{0}) {
		key, value, found := bytes.Cut(field, []byte{'='})
		if found {
			values[string(key)] = string(value)
		}
	}
	if values["SUBSYSTEM"] != "input" {
		return deviceEvent{}, false
	}
	action := values["ACTION"]
	if action != "add" && action != "remove" {
		return deviceEvent{}, false
	}
	devname := values["DEVNAME"]
	if strings.Contains(devname, "/../") {
		return deviceEvent{}, false
	}
	path := devname
	if !filepath.IsAbs(path) {
		if !strings.HasPrefix(path, "input/") {
			return deviceEvent{}, false
		}
		path = "/dev/" + path
	}
	path = filepath.Clean(path)
	if filepath.Dir(path) != "/dev/input" || !isEventNode(filepath.Base(path)) {
		return deviceEvent{}, false
	}
	devPath := values["DEVPATH"]
	if !strings.HasPrefix(devPath, "/devices/") || strings.Contains(devPath, "/../") {
		return deviceEvent{}, false
	}
	seqNum, err := strconv.ParseUint(values["SEQNUM"], 10, 64)
	if err != nil || seqNum == 0 {
		return deviceEvent{}, false
	}
	return deviceEvent{action: action, path: path, devPath: devPath, seqNum: seqNum}, true
}

func acceptEventSequence(latest map[string]uint64, event deviceEvent) bool {
	if event.seqNum <= latest[event.path] {
		return false
	}
	latest[event.path] = event.seqNum
	return true
}

func generationFromEvent(event deviceEvent) deviceGeneration {
	return deviceGeneration{devPath: event.devPath, sequenceFloor: event.seqNum, observedAdd: true}
}

func adoptObservedAdd(current, observed deviceGeneration) deviceGeneration {
	if observed.sequenceFloor > current.sequenceFloor {
		current.sequenceFloor = observed.sequenceFloor
	}
	current.observedAdd = true
	return current
}

func generationMatchesRemoval(generation deviceGeneration, event deviceEvent) bool {
	return generation.devPath == event.devPath && generation.sequenceFloor < event.seqNum
}

func kernelUeventSequence() (uint64, error) {
	data, err := os.ReadFile("/sys/kernel/uevent_seqnum")
	if err != nil {
		return 0, fmt.Errorf("%w: read /sys/kernel/uevent_seqnum: %v", errKernelUeventSequence, err)
	}
	sequence, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: parse value %q: %v", errKernelUeventSequence, strings.TrimSpace(string(data)), err)
	}
	if sequence == 0 {
		return 0, fmt.Errorf("%w: value is zero", errKernelUeventSequence)
	}
	return sequence, nil
}

func sysfsDeviceGeneration(path string) (deviceGeneration, error) {
	name := filepath.Base(path)
	if filepath.Dir(path) != "/dev/input" || !isEventNode(name) {
		return deviceGeneration{}, fmt.Errorf("invalid event device path %q", path)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join("/sys/class/input", name))
	if err != nil {
		return deviceGeneration{}, fmt.Errorf("resolve sysfs identity for %s: %w", path, err)
	}
	if !strings.HasPrefix(resolved, "/sys/devices/") {
		return deviceGeneration{}, fmt.Errorf("unexpected sysfs identity for %s: %s", path, resolved)
	}
	return deviceGeneration{devPath: strings.TrimPrefix(resolved, "/sys")}, nil
}

func isEventNode(name string) bool {
	suffix := strings.TrimPrefix(name, "event")
	if suffix == "" || suffix == name {
		return false
	}
	for _, character := range suffix {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func udevProperties(message []byte) ([]byte, bool) {
	if !bytes.HasPrefix(message, []byte("libudev\x00")) {
		return message, true
	}
	if len(message) < udevHeaderSize || binary.BigEndian.Uint32(message[8:12]) != udevMagic {
		return nil, false
	}
	// systemd stores the magic in network order for socket filters, but the
	// offset and length are native-endian fields in its private header.
	offset := uint64(binary.NativeEndian.Uint32(message[16:20]))
	length := uint64(binary.NativeEndian.Uint32(message[20:24]))
	end := offset + length
	if offset < udevHeaderSize || end < offset || end > uint64(len(message)) {
		return nil, false
	}
	return message[offset:end], true
}

func addEpollFD(epfd, fd int) error {
	event := syscall.EpollEvent{
		Events: syscall.EPOLLIN | syscall.EPOLLERR | syscall.EPOLLHUP,
		Fd:     int32(fd),
	}
	return syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, fd, &event)
}
