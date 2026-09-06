// SPDX-License-Identifier: GPL-2.0-or-later

package engine

import (
	"slices"
	"time"

	"eak/internal/config"
	"eak/internal/keycode"
)

func newCandidate(possible []int, borrowed map[keycode.Logical]*press) *candidate {
	c := &candidate{possible: possible, matched: -1, members: make(map[keycode.Logical]*press), held: make(map[*press]bool)}
	for key, p := range borrowed {
		if p.down {
			c.members[key] = p
			c.held[p] = true
		}
	}
	return c
}

func (e *Engine) startSource(p *press, now time.Time) bool {
	var possible []int
	borrowed := make(map[keycode.Logical]*press)
	if !keycode.IsLogicalModifier(p.logical) {
		for held := range e.reuse {
			borrowed[held.logical] = held
		}
		if len(borrowed) == 0 {
			return false
		}
	}
	for i, source := range e.cfg.Prefixes {
		if !slices.Contains(source.Keys, p.logical) {
			continue
		}
		if len(borrowed) != 0 {
			if source.Mode == config.Action {
				continue
			}
			matches := true
			for key := range borrowed {
				matches = matches && slices.Contains(source.Keys, key)
			}
			for _, key := range source.Keys {
				if keycode.IsLogicalModifier(key) {
					matches = matches && borrowed[key] != nil
				}
			}
			if !matches {
				continue
			}
		}
		possible = append(possible, i)
	}
	if len(possible) == 0 {
		return false
	}
	e.seq = newCandidate(possible, borrowed)
	e.mode, e.deadline = modePrefixCandidate, now.Add(e.cfg.CandidateTimeout)
	if len(borrowed) == 0 && slices.Contains(e.cfg.ReservedModifiers, p.logical) {
		e.mode, e.deadline = modeReserved, time.Time{}
	}
	return true
}

func (e *Engine) startBinding() {
	var possible []int
	for i, binding := range e.cfg.Prefixes[e.activePrefix].Bindings {
		matches := true
		for key := range e.carry {
			matches = matches && slices.Contains(binding.Keys, key)
		}
		if matches {
			possible = append(possible, i)
		}
	}
	e.seq = newCandidate(possible, e.carry)
	e.mode = modeBindingCandidate
}

func (e *Engine) chord(index int) []keycode.Logical {
	if e.mode == modeBindingCandidate {
		return e.cfg.Prefixes[e.activePrefix].Bindings[index].Keys
	}
	return e.cfg.Prefixes[index].Keys
}

// advance observes exact lifetime transitions, never edits physical state.
func (e *Engine) advance(tr transition, now time.Time) bool {
	c, p := e.seq, tr.press
	if c == nil {
		return true
	}
	member := c.members[p.logical] == p
	if p.route == consumed && !member {
		return true
	}
	if e.mode == modeReserved && (tr.value == 0 && c.matched < 0 || p.route == forwarded) {
		e.fail()
		return false
	}
	if tr.value == 2 {
		return true
	}
	if tr.value == 1 {
		if p.route != undecided || tr.countAfter != 1 {
			e.fail()
			return false
		}
		c.owned = append(c.owned, p)
		c.members[p.logical], c.held[p] = p, true
		if !keycode.IsLogicalModifier(p.logical) {
			c.driver = p
		}
		c.possible = slices.DeleteFunc(c.possible, func(index int) bool { return !slices.Contains(e.chord(index), p.logical) })
		if len(c.possible) == 0 {
			e.fail()
			return false
		}
		if e.mode == modeReserved && !slices.Contains(e.cfg.ReservedModifiers, p.logical) {
			e.mode, e.deadline = modePrefixCandidate, now.Add(e.cfg.CandidateTimeout)
		}
		if c.matched < 0 || e.mode == modeBindingCandidate {
			for _, index := range c.possible {
				all := true
				for _, key := range e.chord(index) {
					all = all && c.held[c.members[key]]
				}
				if all {
					c.matched = index
					c.matchMembers = nil
					for _, key := range e.chord(index) {
						c.matchMembers = append(c.matchMembers, c.members[key])
					}
					break
				}
			}
		}
	} else if tr.value == 0 {
		if member {
			c.held[p] = false
			if slices.Contains(c.matchMembers, p) {
				c.broken = true
			}
		}
		if member || tr.countAfter == 0 {
			if c.matched < 0 || !member {
				e.fail()
				return false
			}
		}
	}
	return true
}

func (e *Engine) complete(now time.Time) {
	c := e.seq
	if c == nil || c.matched < 0 {
		return
	}
	if e.mode == modeBindingCandidate {
		for _, p := range c.members {
			if c.held[p] {
				return
			}
		}
		action := e.cfg.Prefixes[e.activePrefix].Bindings[c.matched].Action
		e.resolve(consumed)
		e.idle()
		e.queueOutput(Output{Kind: EmitAction, Action: action})
		return
	}
	source := e.cfg.Prefixes[c.matched]
	if source.Mode != config.Hold {
		for key, p := range c.members {
			if !keycode.IsLogicalModifier(key) && c.held[p] {
				return
			}
		}
	}
	e.resolve(consumed)
	if source.Mode == config.Action {
		e.mode, e.activePrefix, e.seq = modeAwaitBinding, c.matched, nil
		e.carry = make(map[keycode.Logical]*press)
		for key, p := range c.members {
			if keycode.IsLogicalModifier(key) && p.down {
				e.carry[key] = p
			}
		}
		e.deadline = now.Add(e.cfg.SequenceTimeout)
		return
	}
	for key, p := range c.members {
		if keycode.IsLogicalModifier(key) && p.down {
			e.reuse[p] = true
		}
	}
	e.idle()
	if source.Mode == config.Hold {
		e.activate(c, source.Target)
	} else {
		e.queueOutput(keyOutput("eakd-remap", source.Target, 1))
		e.queueOutput(keyOutput("eakd-remap", source.Target, 0))
	}
}
