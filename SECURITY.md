## EAKD/EAKC: important security considerations

This project is very small, but it runs with elevated privileges and has access to all keyboard input, which carries significant security implications for end users. The goal of this document is to provide some context regarding decisions which have been made during the course of the development, as well as to assist the users in setting EAKD/EAKC up.

### Security boundaries

Giving all applications on the system full access to keyboard input is pretty bad for security. In practice, that would mean that any app run with user privileges could potentially be a keylogger. The principle of least privilege dictates that applications should be limited to receiving input events only when their window is in focus, or when an application is otherwise active (for example, in a user-facing terminal).

In X11, applications receive translated keyboard events from the display server. An X11 application running in an X11-based environment can obtain full access to keyboard events, even when it's running in the background. Wayland, in contrast, restricts global access to keyboard events for security reasons. The only reasonable way to obtain access to all keyboard input is to read raw input streams from `/dev/input/event*` nodes.

Usually, permissions on `/dev/input/event*` are set to `root:input` with `660` value, which means that only the superuser and members of the `input` group can read them. `systemd-logind` then uses the `uaccess` tag via `udev` to dynamically assign ACL permissions to the active graphical session user. Note that adding your user to the `input` group is a very bad idea, because it would effectively undo any security separation and give unprivileged processes run by your user access to all keyboard events.

Since EAK, which is essentially a keyboard-driven command executor/application launcher, needs access to raw input streams before they are processed by the userspace input stack, it has to use the client/server architecture. The server part, called EAKD (short for Environment-Agnostic Keychord Daemon), runs as a separate user that is in the `input` group, which makes it possible for it to read keyboard events. It creates a virtual keyboard using the kernel's `uinput` interface and uses it to mirror all ordinary input. To be able to do that, it uses a udev rule (supplied in `72-eak-input.rules`) to give users in the `input` group the ability to inject synthetic input events. The supplied rule sets the `input` group permissions after the standard `70-uaccess.rules` matching rules and before `73-seat-late.rules` applies device ACL. This choice was made for compatibility reasons, because altering input permissions further down the line can potentially interfere with other software that needs access to input events (such as OpenTabletDriver, accessibility helpers, gamepad mappers...)

However, it must be noted that different Linux distributions set up default permissions differently, and udev rules also differ across the board. Distribution or local rules may tag input devices for `uaccess`, or otherwise grant device ACLs to the logged-in user, independently of the group and mode set up by `eakd`'s rule. Users are encouraged to research their distribution's input-device policy and inspect the effective udev rules and ACLs on `/dev/input/event*` before relying on `eakd`'s access boundary. At the time of writing (in August 2026), the udev rule supplied with `eakd` has been tested on openSUSE Tumbleweed and Debian Trixie. These two distributions should serve as the compatibility baseline for the project.

Once started, `eakd` creates a standard Unix socket that the clients connect to. Only UIDs explicitly listed in the configuration file are allowed to establish a connection with the daemon process. The UIDs are checked using the established `SO_PEERCRED` mechanism. Only one connection per authorised UID is allowed. The client part of EAK, called EAKC (short for Environment-Agnostic Keychord Client), does not receive any information about raw keyboard events from `eakd`, it receives only configured action IDs. The communication between the two processes is intentionally as opaque as possible to prevent unrestricted input sniffing. The client and the server configuration files are completely separate, so the server process cannot possibly know what commands would be triggered by the configured action IDs.

To further enhance the security boundary, `eakd` requires that its configuration is writable only by `root`, and `eakc` requires that its configuration is writable only by the account under which the client process is run. Both processes refuse to start if their respective configuration files are group- or world-writable.

### Third-party dependencies

The project does not use any third-party dependencies. It relies only on the Go runtime and the language's standard library.

### Why no binaries?

Given the security-sensitive nature of the project, it was decided early on that I would not provide pre-compiled binaries. Users are encouraged to inspect the source code of EAKD/EAKC and other supplied files (`Makefile`, `systemd` unit files, udev rule). If they find that the project satisfies their security requirements, they can build it themselves, preferably using the latest supported version of the Go toolchain. The supplied `Makefile` makes the process of building and installing EAKD/EAKC entirely painless.

### Reporting and advisories

Any security findings should be reported in the project's [Security Advisories section](https://github.com/SpinningVinyl/eakd/security/advisories).
