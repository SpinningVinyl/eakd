# EAK

EAK is an environment-agnostic, Linux keyboard sequence dispatcher. `eakd` is
the privileged input broker; `eakc` is a per-user client that receives opaque
action IDs and runs the commands configured for that user. It never receives
raw keyboard events.

`eakd` grabs idle physical keyboard evdev nodes, exposes one virtual keyboard
through uinput, forwards ordinary input, and consumes configured prefix
sequences such as `Logo+T, 1`. Udev notifications discover hotplugged devices
immediately. If a newly discovered keyboard has held keys, its exclusive
handoff is postponed until all keys are released.

The compositor is authoritative for Caps Lock, Num Lock, and Scroll Lock.
`eakd` mirrors the compositor's `EV_LED` feedback to every connected keyboard
that exposes the corresponding LED; it never infers lock state from raw keys.

## Build and test

```sh
make build
make test
make vet
```

The binaries are written to `bin/eakd` and `bin/eakc`. The broker's evdev and
uinput implementation has been audited for Linux amd64 and arm64 ioctl
encoding and refuses to start on other architectures.

## Configuration

Copy `configs/eakd.example.json` to `/etc/eak/eakd.json`, make it owned by
root, and replace `allowed_uids` with the UID that will run `eakc`:

```sh
sudo install -D -o root -g root -m 0644 configs/eakd.example.json /etc/eak/eakd.json
sudo /usr/libexec/eakd -config /etc/eak/eakd.json -check
```

`CTRL`, `SHIFT`, `ALT`, and `LOGO` match either side. Letters, digits, F1-F12,
common named Linux keys, and `CODE_<decimal Linux keycode>` are accepted.
Every prefix must contain a modifier. Prefixes that are subsets of one another
are rejected because they cannot be resolved without another timeout layer.
The three lock keys may be used in broker sequences. If a sequence consumes
one, the compositor does not see it and therefore does not change lock state.

The broker configuration must be root-owned and not group- or world-writable.
`-allow-insecure-config` exists only for local development.

Copy the client example to the configuration directory of every participating
desktop user:

```sh
install -D -m 0600 configs/eakc.example.json ~/.config/eak/eakc.json
eakc -check
```

Client actions have one of two types:

- `exec` takes a non-empty `command` array and executes it directly. Arguments
  are never interpreted by a shell.
- `shell` passes `script` to `/bin/sh -c`. Use this only when pipelines,
  expansion, redirection, or other shell syntax is required.

Both types accept an optional absolute `working_directory`. Command output is
sent to eakc's stdout and stderr, which normally means the user journal. At
most `max_parallel` commands run simultaneously; further received actions wait
in the bounded `queue_size` channel. Unknown action IDs are logged and ignored.
The client reconnects automatically while eakd is unavailable or restarting.

The eakc configuration executes code with the user's privileges. It must be a
regular file owned by that user and must not be group- or world-writable.
`-allow-insecure-config` is available only for development.

## Installation outline

1. Install `bin/eakd` as `/usr/libexec/eakd`.
2. Install `packaging/eakd.sysusers` under `/usr/lib/sysusers.d/eakd.conf` and
   run `systemd-sysusers`.
3. Install `packaging/eakd.modules-load` under
   `/usr/lib/modules-load.d/eakd.conf` so `/dev/uinput` exists after boot.
4. Install `packaging/99-eak-input.rules` under `/usr/lib/udev/rules.d/`, reload
   udev rules, and retrigger input devices.
5. Install the configuration under `/etc/eak/eakd.json`.
6. Install `packaging/eakd.service` under `/usr/lib/systemd/system/` and enable
   it.

Do not add desktop users to the `input` group. Membership permits unrestricted
keyboard monitoring. The dedicated `eakd` account should be the only
non-root member.

The action socket uses newline-delimited JSON:

```json
{"action":"terminal.one"}
```

It authenticates clients with `SO_PEERCRED` and accepts only UIDs listed in the
root-owned configuration. It never sends raw key events and never accepts or
executes command strings.

Install `packaging/eakc.service` as a user unit, then enable it for the desktop
user with `systemctl --user enable --now eakc.service`. The unit intentionally
joins `graphical-session.target` so launched programs inherit the session
environment. It intentionally does not depend on eakd startup ordering because
eakc reconnects with bounded exponential backoff.

## Current scope

The implementation assumes one Linux seat. It combines all discovered typing
keyboards into one virtual seat-0 keyboard. Multi-seat routing is not
implemented yet.

See `docs/eakd-design.md` for the state machine, syscall-level design, failure
behavior, and source map.
