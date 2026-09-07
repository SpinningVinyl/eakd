## Implement single-key remaps

Add the ability to remap a single key to a different key (e.g. Caps Lock -> Ctrl)

Requires support for modifier targets as well as single-key sources.

## Implement separate mappings for tap and hold

This way Caps Lock can emit Esc on tap, but act as Ctrl on hold.

Tap resolves on early release; hold resolves on timeout or another keypress, before forwarding that key.
