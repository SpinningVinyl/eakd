// SPDX-License-Identifier: GPL-2.0-or-later

package configfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenValidatesOpenedDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path, uint32(os.Geteuid()), false)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("read %q from replaced pathname, want original descriptor contents", data)
	}
}

func TestOpenRejectsSymlinkInSecureMode(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	link := filepath.Join(directory, "config.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	file, err := Open(link, uint32(os.Geteuid()), false)
	if file != nil {
		file.Close()
	}
	if !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("secure open returned %v, want ELOOP for a symlink", err)
	}
}

func TestOpenRejectsWritableConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path, uint32(os.Geteuid()), false)
	if file != nil {
		file.Close()
	}
	if err == nil {
		t.Fatal("secure open accepted a group-writable configuration")
	}
}

func TestOpenRejectsWrongOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path, uint32(os.Geteuid())+1, false)
	if file != nil {
		file.Close()
	}
	if err == nil {
		t.Fatal("secure open accepted a configuration owned by another UID")
	}
}

func TestOpenRejectsFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path, uint32(os.Geteuid()), false)
	if file != nil {
		file.Close()
	}
	if err == nil {
		t.Fatal("secure open accepted a FIFO")
	}
}

func TestOpenAllowsSymlinkInDevelopmentMode(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	link := filepath.Join(directory, "config.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	file, err := Open(link, uint32(os.Geteuid()), true)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}
