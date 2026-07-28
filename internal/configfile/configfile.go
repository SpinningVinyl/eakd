package configfile

import (
	"fmt"
	"os"
	"syscall"
)

// Open opens a configuration file and, unless explicitly disabled for
// development, validates the exact opened descriptor before returning it.
func Open(path string, expectedUID uint32, allowInsecure bool) (*os.File, error) {
	if allowInsecure {
		return os.Open(path)
	}

	flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("create file for descriptor")
	}
	fail := func(err error) (*os.File, error) {
		_ = file.Close()
		return nil, err
	}

	info, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("inspect opened file: %w", err))
	}
	if !info.Mode().IsRegular() {
		return fail(fmt.Errorf("configuration %q is not a regular file", path))
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fail(fmt.Errorf("cannot inspect configuration ownership"))
	}
	if stat.Uid != expectedUID {
		return fail(fmt.Errorf("configuration %q must be owned by uid %d", path, expectedUID))
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fail(fmt.Errorf("configuration %q must not be group- or world-writable", path))
	}
	return file, nil
}
