PREFIX ?= /usr
BINDIR ?= $(PREFIX)/bin
LIBEXECDIR ?= $(PREFIX)/libexec
SYSCONFDIR ?= /etc
SYSTEMD_SYSTEM_UNITDIR ?= $(PREFIX)/lib/systemd/system
SYSTEMD_USER_UNITDIR ?= $(PREFIX)/lib/systemd/user
SYSUSERSDIR ?= $(PREFIX)/lib/sysusers.d
MODULESLOADDIR ?= $(PREFIX)/lib/modules-load.d
UDEVRULESDIR ?= $(PREFIX)/lib/udev/rules.d
DOCDIR ?= $(PREFIX)/share/doc/eak

INSTALL ?= install
SYSTEMD_SYSUSERS ?= systemd-sysusers
SYSTEMCTL ?= systemctl
UDEVADM ?= udevadm
MODPROBE ?= modprobe

VERSION ?= $(shell git -c safe.directory="$(CURDIR)" describe --tags --always --dirty 2>/dev/null || printf unknown)
VERSION_LDFLAGS = -X eak/internal/buildinfo.Version=$(VERSION)

.PHONY: build test vet install clean

build:
	mkdir -p bin
	go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAGS)" -o bin/eakd ./cmd/eakd
	go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAGS)" -o bin/eakc ./cmd/eakc

test:
	go test ./...

vet:
	go vet -buildvcs=false ./...

install:
	@test -x bin/eakd -a -x bin/eakc || { \
		echo "Binaries not found; run 'make build' before 'make install'." >&2; \
		exit 1; \
	}
	$(INSTALL) -d -m 0755 "$(DESTDIR)$(LIBEXECDIR)" "$(DESTDIR)$(BINDIR)"
	$(INSTALL) -m 0755 bin/eakd "$(DESTDIR)$(LIBEXECDIR)/eakd"
	$(INSTALL) -m 0755 bin/eakc "$(DESTDIR)$(BINDIR)/eakc"
	$(INSTALL) -D -m 0644 packaging/eakd.service "$(DESTDIR)$(SYSTEMD_SYSTEM_UNITDIR)/eakd.service"
	$(INSTALL) -D -m 0644 packaging/eakc.service "$(DESTDIR)$(SYSTEMD_USER_UNITDIR)/eakc.service"
	$(INSTALL) -D -m 0644 packaging/eakd.sysusers "$(DESTDIR)$(SYSUSERSDIR)/eakd.conf"
	$(INSTALL) -D -m 0644 packaging/eakd.modules-load "$(DESTDIR)$(MODULESLOADDIR)/eakd.conf"
	$(INSTALL) -D -m 0644 packaging/72-eak-input.rules "$(DESTDIR)$(UDEVRULESDIR)/72-eak-input.rules"
	$(INSTALL) -D -m 0644 configs/eakd.example.json "$(DESTDIR)$(DOCDIR)/eakd.example.json"
	$(INSTALL) -D -m 0644 configs/eakc.example.json "$(DESTDIR)$(DOCDIR)/eakc.example.json"
	@if test -e "$(DESTDIR)$(SYSCONFDIR)/eak/eakd.json"; then \
		echo "Preserving existing $(DESTDIR)$(SYSCONFDIR)/eak/eakd.json"; \
	else \
		$(INSTALL) -D -m 0644 configs/eakd.example.json "$(DESTDIR)$(SYSCONFDIR)/eak/eakd.json"; \
	fi
ifeq ($(strip $(DESTDIR)),)
	$(SYSTEMD_SYSUSERS) "$(SYSUSERSDIR)/eakd.conf"
	$(UDEVADM) control --reload
	$(MODPROBE) uinput
	$(UDEVADM) trigger --action=change --subsystem-match=input --settle
	$(UDEVADM) trigger --action=change --sysname-match=uinput --settle
	$(SYSTEMCTL) daemon-reload
endif

clean:
	rm -rf bin
