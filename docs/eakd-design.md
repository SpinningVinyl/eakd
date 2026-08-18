# eakd implementation guide

## Process boundary

`eakd` is a system service running as the dedicated `eakd` user. That account
has read access to `/dev/input/event*` and write access to `/dev/uinput` through
the `input` group. Desktop users do not receive either permission.

The root-owned file `/etc/eak/eakd.json` maps key sequences to opaque action
IDs. The Unix socket maps no commands and accepts no configuration. This is
deliberate: allowing a desktop process to register arbitrary single keys would
turn the broker back into a keylogging oracle.

## Startup order

1. `cmd/eakd/main_linux.go` opens the JSON file without following a final
   symlink, validates ownership and permissions with `fstat(2)` on that
   descriptor, then parses the same descriptor.
2. `internal/action` binds the action socket and enables peer authentication.
3. `internal/linuxinput/uinput_linux.go` creates the virtual keyboard and waits
   for its sysfs event node to appear.
4. `internal/linuxinput/manager_linux.go` enumerates existing keyboards,
   subscribes to input-device udev events, and hands idle devices to the
   exclusive forwarding path.
5. The main loop serializes matching, virtual output, resynchronization and
   action publication.

Creating the virtual device before grabbing physical devices avoids a period
where no keyboard is visible to the active display or input stack.

The supplied unit uses `Type=notify` and orders `eakd` before
`display-manager.service`. The daemon sends `READY=1` only after the action
socket has been created with its final permissions, the virtual keyboard
exists, the udev monitor is subscribed, and initial enumeration is complete.
A keyboard with held keys remains on its original input path and may still be
awaiting handoff when readiness is announced.

The manager subscribes to netlink before initial enumeration so a hotplug event
racing with the glob cannot be missed. Enumerated nodes are identified by their
resolved sysfs `DEVPATH` and a per-device kernel uevent sequence floor captured
after opening the node. `fstat(2)` records the opened descriptor's filesystem
identity; after resolving `DEVPATH` and reading the sequence floor, `stat(2)`
must report the same filesystem, inode, and device number for the pathname.
If it does not, the descriptor is closed and queued udev lifecycle events
identify the replacement. Hotplug and retry opens perform the same identity
check and must also match their expected `DEVPATH`. A candidate is opened
non-exclusively and queried with `EVIOCGKEY`.
Failure to read or parse a sequence floor aborts startup before readiness is
reported, because continuing would leave an existing keyboard unmanaged.
While any key is held, the manager leaves it ungrabbed, allowing the entire
press/release sequence to remain on the physical device as seen by the active
display or input stack. Once it is idle, the manager discards its private
pre-grab event queue, verifies the state is still idle, calls `EVIOCGRAB`, and
registers the descriptor with epoll. Failed state queries or acquisition steps
close and defensively ungrab the descriptor and are retried.

## Linux system calls and ioctls

### `open(2)`

Physical `/dev/input/eventN` nodes are opened with `O_RDWR | O_NONBLOCK |
O_CLOEXEC`. Read access is used for events, state queries and `EVIOCGRAB`;
write access is required to update physical LEDs. Nonblocking descriptors
allow one epoll loop to drain every keyboard without one thread per device.
`O_CLOEXEC` prevents descriptors from leaking if process execution is ever
added accidentally.

`/dev/uinput` is opened with `O_RDWR | O_NONBLOCK | O_CLOEXEC`. Writes inject
input; reads drain device-output events from the active display or input stack,
such as LED updates. A transient `EAGAIN` on the critical write path is retried
rather than dropping an event.

### `ioctl(2)` on evdev

Linux device operations not expressible as ordinary reads use `ioctl`:

- `EVIOCGNAME` retrieves the kernel device name. It excludes `eakd virtual
  keyboard`, preventing a feedback loop.
- `EVIOCGBIT(EV_KEY)` retrieves the supported-key bitmap. A node is treated as
  a typing keyboard when it exposes either `KEY_SPACE` or `KEY_ENTER` together
  with either `KEY_A` and `KEY_Z`, or `KEY_P` and `KEY_L`. Accepting either
  letter pair and either common non-letter key accommodates the separate halves
  of ergonomic keyboards while excluding mouse-button and consumer-control
  interfaces. A standalone numpad is also accepted when it exposes `KEY_KP0`
  through `KEY_KP9` together with `KEY_KPENTER`. A successful query without
  either capability set is a permanent rejection, while a query error closes
  the descriptor and enters the bounded reacquisition path.
- `EVIOCGRAB(1)` makes this file descriptor the exclusive evdev recipient.
  Other evdev clients stop receiving physical events. `EVIOCGRAB(0)` or closing
  the descriptor releases the grab.
- `EVIOCGKEY` retrieves the authoritative pressed-key bitmap. It is used at
  startup, while waiting for an idle hotplug handoff, and during manual
  `SYN_DROPPED` recovery.
- `EVIOCGBIT(EV_LED)` discovers which of the three standard LEDs each physical
  keyboard implements.

`sys_linux.go` builds ioctl request numbers with Linux's `_IOC` bit layout and
invokes `SYS_IOCTL` directly. The daemon refuses non-amd64/arm64 architectures
because ioctl layouts must be audited per architecture.

### `epoll_create1(2)`, `epoll_ctl(2)`, and `epoll_wait(2)`

The device manager creates one close-on-exec epoll instance. The udev netlink
descriptor, the virtual keyboard feedback descriptor, every grabbed keyboard,
and a manager-owned cancellation pipe are registered for `EPOLLIN`, `EPOLLERR`,
and `EPOLLHUP`. `epoll_wait` wakes immediately for hotplug, compositor LED
feedback, physical input, or cancellation. With no candidate or retry work
pending, it waits indefinitely. While a held-key candidate or failed
acquisition is pending, the timeout is derived from the earliest actual
maintenance deadline. Hangup, removal, or device errors release a keyboard and
cause its held virtual keys to be released.

### udev netlink discovery

The manager opens an `AF_NETLINK`/`NETLINK_KOBJECT_UEVENT` socket with
`SOCK_NONBLOCK | SOCK_CLOEXEC` and subscribes only to the post-rule udev
multicast group, after ownership and permissions have been applied. Only `add`
and `remove` events containing a valid `DEVPATH`, positive `SEQNUM`, and
strictly validated `/dev/input/event<digits>` path are accepted. Events older
than the latest sequence seen for a path are ignored. Active devices, held-key
candidates, and retry records all carry the corresponding generation; a
removal can affect them only when its `DEVPATH` matches and its sequence is
newer. Opening or querying a node can still race device-node creation, so
transient acquisition failures are retried after 100 ms without returning to
directory polling. Each device generation is allowed at most five
reacquisition attempts after its original acquisition fails, and a matching
removal resets that budget. `SO_PASSCRED` attaches kernel-authenticated sender
credentials to each datagram; messages without UID 0 credentials are ignored.

### `read(2)` and evdev frames

`read` returns an integral number of `struct input_event` records. The kernel
timestamp is decoded but discarded. `EV_KEY` value 1 is a press, 0 a release,
and 2 an autorepeat. Events are accumulated through `EV_SYN/SYN_REPORT` so
scan codes and key transitions remain in their original atomic frame.

### Manual `SYN_DROPPED` recovery

Each open event node has its own kernel queue. If that queue overflows, the
kernel emits `SYN_DROPPED`, meaning intervening events are unreliable.

The device manager then:

1. Discards its incomplete frame.
2. Ignores every event through the next `SYN_REPORT`.
3. Calls `EVIOCGKEY` to retrieve current physical state.
4. Sends a resynchronization message to the main loop.

The matcher replaces that device's state and cancels any ambiguous buffered
sequence. The forwarder compares the authoritative bitmap with the state it
previously emitted and synthesizes missing releases before missing presses,
followed by one `SYN_REPORT`. This is what prevents a lost release from leaving
a virtual modifier permanently held.

### uinput ioctls and `write(2)`

The virtual keyboard is configured with:

- `UI_SET_EVBIT(EV_KEY)` and `UI_SET_KEYBIT` for the complete Linux key range.
- `UI_SET_EVBIT(EV_MSC)` and `UI_SET_MSCBIT(MSC_SCAN)` for hardware scan codes.
- `UI_SET_EVBIT(EV_LED)` and `UI_SET_LEDBIT` for Num, Caps and Scroll feedback
  from the active display or input stack.
- `UI_DEV_SETUP` for its name and stable virtual identity.
- `UI_DEV_CREATE` to register it with the kernel input subsystem.

Ordinary frames are injected by writing `struct input_event` records followed
by `EV_SYN/SYN_REPORT`. `UI_DEV_DESTROY` removes the virtual keyboard during
clean shutdown. Closing the uinput descriptor also destroys it after a crash.
Device-output events are drained from the same read/write uinput descriptor by
the device manager's epoll loop. `EV_LED` output events are the sole source of
lock state. uinput does not frame device-output callbacks with `SYN_REPORT`, so
the feedback handler accepts every LED change returned by `read(2)` and applies
it without waiting for a synchronization event.

### Unix socket and `SO_PEERCRED`

Go's `net.ListenUnix` wraps `socket`, `bind`, `listen`, and `accept`. After
accepting, `SO_PEERCRED` returns credentials assigned by the kernel to that
connection; the client cannot forge them in message data. Only configured UIDs
are retained, with at most one active connection per authorized UID. Each
client has a bounded queue, so a stalled client is dropped instead of blocking
keyboard forwarding. A passive read detects disconnects promptly; because the
protocol is daemon-to-client only, receiving client data also closes the
connection.

## Matching and buffering

`internal/engine` has physical state independent of virtual output state.
That separation permits a consumed prefix to affect matching without ever
reaching the active display or input stack.

The states are:

```text
IDLE
  modifier that can start a prefix
    -> PREFIX_CANDIDATE (buffer complete frames)

PREFIX_CANDIDATE
  complete chord pressed and released
    -> consume buffer -> AWAIT_BINDING
  impossible chord or timeout
    -> replay buffer -> IDLE

AWAIT_BINDING
  first continuation press
    -> BINDING_CANDIDATE
  timeout
    -> IDLE (recognized prefix remains consumed)

BINDING_CANDIDATE
  complete configured chord pressed and released
    -> consume buffer -> publish action -> IDLE
  impossible chord or timeout
    -> replay continuation buffer -> IDLE
```

Only modifier keys can initiate prefix candidacy. Without that restriction,
configuring `LOGO+T` would delay every ordinary `T` while the daemon waited to
see whether Logo followed it.

## Modifier reference counts

`internal/linuxinput/forwarder_linux.go` tracks keys forwarded from each
physical device. For the eight standard left/right Ctrl, Shift, Alt and Logo
codes it also maintains a count across devices:

- The first physical press emits one virtual press.
- Additional presses of the same modifier code update the count but emit
  nothing.
- Releases emit nothing while the count remains nonzero.
- The last release emits the virtual release.

Thus releasing Left Shift on one keyboard cannot release virtual Left Shift
while another physical keyboard still holds it.

## Global lock and LED state

`internal/linuxinput/locks_linux.go` owns three process-wide values plus a
per-value validity bit. All are unknown at startup, so the daemon does not
force physical LEDs off before feedback from the active display or input stack
arrives. Raw
`KEY_CAPSLOCK`, `KEY_NUMLOCK`, and `KEY_SCROLLLOCK` events receive no special
treatment: they are forwarded, buffered, or consumed by the normal matcher.

The virtual keyboard advertises the three standard LED outputs. Its active
consumer applies the current keymap and decides whether an input event changes
a lock, then writes the resulting `EV_LED` state back to the virtual device. In
a graphical session that consumer is typically a Wayland compositor or the
Xorg server; the Linux virtual console can fulfill the same role outside a
graphical session. Thus a Caps Lock key remapped to Control does not
accidentally toggle the Caps LED.

The manager collects changes while draining the nonblocking virtual descriptor,
adopts supported events as the global state on its single epoll-owner thread,
then writes supported `EV_LED` values to every physical keyboard followed by
one `SYN_REPORT`. A newly connected keyboard is initialized only for states the
active consumer has provided; unknown LEDs are left untouched.

Lock keys are valid in prefixes and continuations. Consuming one deliberately
prevents it from reaching the active display or input stack, so it cannot
change that stack's lock state. This keeps remapping and sequence behavior
under the same authority.

## Failure behavior

Any uinput write error is fatal. The main function cancels the manager, which
ungrabs and closes every physical descriptor; normal physical input then
resumes. Systemd restarts the daemon after one second. The daemon never tries
to continue with a partially functioning virtual output path.

Every manager-to-main-loop channel send also selects on context cancellation.
If the bounded message queue is full during shutdown, cancellation abandons
the pending send so the manager can run its descriptor cleanup and terminate.

The manager's cancellation pipe wakes its indefinite epoll wait immediately.
Its callback is stopped or joined before the pipe descriptors are closed, so a
late cancellation write cannot target a recycled descriptor. The virtual
keyboard remains owned by the main run function and is closed only after the
manager has released all physical grabs.

The action socket is not on the input critical path: publication uses bounded
queues and disconnects slow consumers.

## User action client

`cmd/eakc` loads a user-owned configuration and connects to the action socket.
It receives only newline-delimited opaque action IDs. Socket absence, daemon
restart, and EOF initiate reconnect with exponential backoff from 100 ms to
five seconds.

`internal/executor` maps configured IDs to typed actions. An `exec` action is
started directly from its argument vector; a `shell` action explicitly invokes
`/bin/sh -c`. A bounded worker pool limits concurrent processes. The socket
reader feeds a bounded local queue, so command latency cannot immediately
block daemon publication. Unknown IDs and command failures are logged without
terminating the client. On shutdown, command contexts are cancelled and the
client waits for workers to exit.
