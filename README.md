# EAKD/EAKC

## TL;DR

EAKD/EAKC is a solution that allows you to launch programs or
execute shell scripts via Emacs-style key sequences. For example, it is
possible to configure sequences such as `Logo+T, 1`, which means "press
`T` while holding `Logo`, then release `Logo` and press and release `1`".
It also supports two-chord Emacs-style sequences with a continuously held
modifier such as `Logo+T, Logo+1`, which means "press and release `T`
and then `1` while holding `Logo`, then release `Logo`".

Please take note that *only* Emacs-style prefixed sequences are in scope for
this project, since one-shot hotkey combinations are well supported by pretty
much every desktop environment and Wayland compositor under the Sun.

The server, called EAKD, is an input broker that runs with elevated privileges;
the client, called EAKC, is the user-side client that launches programs and
executes shell scripts. Great care has been taken in making sure that the
EAKD/EAKC combo is reasonably secure. Please see SECURITY.md for details.

## Longer version

EAKD/EAKC is an environment-agnostic solution for managing hotkeys in Linux.
`eakd` serves as the privileged input broker; `eakc` is the user-side client.
Both need to be installed and running for mapped actions to execute.

`eakd` grabs eligible idle physical keyboard evdev nodes and exposes
a virtual keyboard through uinput. The virtual keyboard is used to forward
ordinary input to the userspace input stack. It consumes configured Emacs-style
key sequences such as `Logo+T, 1`. These sequences generate actions that are
sent to connected authorised clients. The client executes programs or runs
shell scripts configured for specific action IDs. It never receives raw
keyboard events from `eakd` to make sure that it doesn't accidentally turn
into a universal keylogger.

## Basic flow

```mermaid
flowchart LR
    K["Physical keyboard<br/>evdev input"]

    subgraph D["eakd — privileged input broker"]
        M{"Sequence matcher"}
        F["Input forwarder"]
        P["Action publisher"]
        U["Virtual keyboard<br/>uinput"]
    end

    S["Active userspace input stack<br/>Wayland compositor, Xorg, or console"]

    subgraph C["eakc — per-user client"]
        R["Receive opaque action ID"]
        L["Look up configured action"]
        E["Execute program<br/>or shell script"]
    end

    K -->|"Raw keyboard events"| M
    M -->|"Ordinary or unmatched input"| F
    F --> U
    U -->|"Forwarded keyboard input"| S

    M -->|"Matched preconfigured<br/>hotkey sequence"| P
    P -->|"Action ID only<br/>authenticated Unix socket"| R
    R --> L
    L --> E

    N["Raw keyboard events are<br/>never sent to eakc"]
    R -.-> N
```

## Build and install

```sh
make test
make vet
make build
sudo make install
```

The installation step installs the binaries, the system and user systemd units, 
the sysusers and modules-load configurations, the udev rule, and example 
configurations. It also creates the `eakd` system account, loads uinput, reloads 
the udev rules, and reloads the systemd manager. It does not enable or start 
either service. An existing `/etc/eak/eakd.json` is preserved.

The default installation prefix is `/usr`. Packagers can stage the installation
with `DESTDIR`; host-side setup commands are skipped when `DESTDIR` is
non-empty. For example:

```sh
make build
make install DESTDIR=/tmp/eak-package-root PREFIX=/usr
```

The broker's evdev and uinput implementation only supports Linux on amd64
and arm64.

Builds made through the Makefile derive the version from
`git describe --tags --always --dirty`. Packagers building outside a Git checkout
can set `VERSION` explicitly, for example `make VERSION=v1.0.0`.

## Distribution

`eakd` has access to all keyboard input and therefore carries potentially serious
security implications. This repository does not provide binary downloads.
Users are encouraged to inspect the source code and the Makefile, then build
`eakd` and `eakc` themselves using the instructions above.

## Configuration

On the first installation, `make install` creates `/etc/eak/eakd.json` from the
example configuration. Replace `allowed_uids` with the UID that will run
`eakc`, adjust the bindings, validate the file, and enable the broker:

```sh
sudo /usr/libexec/eakd --config /etc/eak/eakd.json --check
sudo systemctl enable --now eakd.service
```

`CTRL`, `SHIFT`, `ALT`, and `LOGO` match either side. Letters, digits, F1-F12,
common named Linux keys, `KEY_KP*` names for numpad keys (also accepted
without the `KEY_` prefix, for example `KP1` and `KPENTER`), and
`CODE_<decimal Linux keycode>` are accepted.

Every prefix must contain a modifier. Prefixes that are subsets of one another
are rejected because they cannot be resolved without another timeout layer.
The three lock keys may be used in broker sequences, in that case they do not
reach the userspace input stack.

The broker configuration must be root-owned, must not be a symlink, and must not
be group- or world-writable. `--allow-insecure-config` exists only for local
development.

### Installing and configuring eakc

The executable, user unit, and example configuration are installed by
`sudo make install`. Each participating desktop user should copy and validate
their own configuration:

```sh
install -D -m 0600 /usr/share/doc/eak/eakc.example.json "$HOME/.config/eak/eakc.json"

/usr/bin/eakc --config "$HOME/.config/eak/eakc.json" --check
```

The installed paths are:

- Executable: `/usr/bin/eakc`
- Per-user configuration: `$HOME/.config/eak/eakc.json`
- Systemd user service: `/usr/lib/systemd/user/eakc.service`

`/usr/bin/eakc` uses `~/.config/eak/eakc.json` by default, so the explicit
`--config` argument above is only included to make the validation command
unambiguous. Each participating desktop user needs their own configuration.

The user must run the following commands to enable the client:

```sh
systemctl --user daemon-reload
systemctl --user enable --now eakc.service
```
To verify that the service started successfully, use the following command:

```sh
systemctl --user status eakc.service
```

The logs can be viewed with `journalctl`:

```sh
journalctl --user --unit=eakc.service --follow
```

After editing `~/.config/eak/eakc.json`, validate it and restart the service:

```sh
/usr/bin/eakc --config "$HOME/.config/eak/eakc.json" --check
systemctl --user restart eakc.service
```

To stop eakc and prevent it from starting in later sessions:

```sh
systemctl --user disable --now eakc.service
```

The unit joins `graphical-session.target` so launched programs inherit the
session environment.

Client actions have one of two types:

- `exec` takes a non-empty `command` array and executes it directly. Arguments
  are never interpreted by a shell.
- `shell` passes `script` to `/bin/sh -c`. Use this only when pipelines,
  expansion, redirection, or other shell syntax is required.

Both types accept an optional absolute `working_directory`. Command output is
sent to eakc's stdout and stderr, which normally means the user journal. At
most `max_parallel` commands run simultaneously; further received actions wait
in the bounded `queue_size` channel. Unknown action IDs are logged and ignored.

`eakc` executes code with the user's privileges. The config file must be owned 
by the user running `eakc`, must not be a symlink, and must not be group-
or world-writable. `--allow-insecure-config` is available only for development.

## System access

The provided `72-eak-input.rules` file sets the `input` group permissions after
the standard `70-uaccess.rules` matching rules and before `73-seat-late.rules`
applies device ACLs.

Different Linux distributions ship different udev rules. Distribution or local
rules may tag input devices for `uaccess` or otherwise grant device ACLs to the
logged-in user, independently of the group and mode set by `eakd`'s rule. Users
are encouraged to research their distribution's input-device policy and inspect
the effective udev rules and ACLs on `/dev/input/event*` before relying on 
`eakd`'s access boundary.

Do not add desktop users to the `input` group. Membership permits unrestricted
keyboard monitoring. The dedicated `eakd` account should be the only non-root
member.

The action socket uses newline-delimited JSON:

```json
{"action":"terminal.one"}
```

It authenticates clients with `SO_PEERCRED` and accepts only UIDs listed in the
root-owned configuration. Client connections are limited to one per UID. `eakd`
never sends raw key events and never accepts or executes command strings.

## Current scope

The implementation assumes one Linux seat. It combines all discovered typing
keyboards into one virtual seat-0 keyboard. Multi-seat routing is not
implemented.

See `docs/eakd-design.md` for the state machine, syscall-level design, failure
behavior, and source map.

## License

EAKD/EAKC is licensed under the GNU General Public License, version 2 or (at
your option) any later version (`GPL-2.0-or-later`). See [LICENSE](LICENSE) for
the full license text.
