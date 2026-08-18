# Pre-release TODO

## 1. Enforce sequence deadlines inside the engine — Resolved

`Engine.HandleFrame` now expires stale matching state before processing a newly arrived frame. Timer events remain in the main loop for prompt resolution when no input arrives, but correctness no longer depends on whether the loop selects a ready timer or input channel first.

## 2. Harden the Unix socket lifecycle — Resolved

`Server.Serve` now checks that the provided path is a dead socket before deleting it.

## 3. Bound action IDs in the shared protocol — Resolved

`internal/action/protocol.go` now defines a shared 1,024-byte action-ID limit and validator (that also rejects blank strings).

## 4. Terminate complete action process trees

`exec.CommandContext` reliably terminates the immediate command when eakc shuts down, but child processes created by shell scripts or executed programs may survive. Possible solution: start each action in its own Linux process group and make cancellation terminate that group, with an escalation from a graceful signal to forced termination if appropriate.

## 5. Decompose the Linux device manager — Resolved

`Manager.Run` now delegates device discovery and event processing to a private `managerState` type that owns the generation, candidate, retry, and active-device collections. The giant `Run()` loop with multiple closures was also broken down into several methods.

## 6. (withdrawn)

## 7. Expand executor and action-server tests

`internal/executor` has no direct tests, and the action server coverage is pretty minimal.

## 8. Replace polling and fixed startup delays where practical - Resolved (mostly)

Virtual keyboard feedback is now handled by the manager's epoll loop instead of a 25-millisecond timer. Device visibility is still found through repeated sysfs globbing, and startup still includes a fixed 100-millisecond sleep intended to give consumers time to observe the virtual device, which does not seem to be an issue in a configuration with a single server and a single client running on the same PC.

## 9. Canonicalize chords in proportion to their size — Resolved

`chordSignature` now clones and sorts the keys actually present, then serializes the sorted values. 

## 10. Make the README.md more readable

The README is currently poorly organised and difficult to read. Also it needs a better section on configuration (e.g. the example config for `eakc` doesn't demonstrate `working_directory`).
