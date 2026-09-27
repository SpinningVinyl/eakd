// SPDX-License-Identifier: GPL-2.0-or-later

package engine

import (
	"time"

	"eak/internal/config"
	"eak/internal/input"
)

// Preserve the old regression fixtures' expected frames/actions while the
// production API moves to ordered operations. New tests use the API directly.
type observedResult struct {
	Forward []input.Frame
	Actions []string
}

type testEngine struct{ *Engine }

func newTestEngine(cfg config.Config) *testEngine { return &testEngine{New(cfg)} }

func observe(result Result) observedResult {
	var r observedResult
	for _, out := range result.Output {
		switch out.Kind {
		case ForwardFrame:
			r.Forward = append(r.Forward, out.Frame)
		case EmitAction:
			r.Actions = append(r.Actions, out.Action)
		default:
			panic("reconciliation requires an ordered test consumer")
		}
	}
	return r
}

func (e *testEngine) HandleFrame(f input.Frame, now time.Time) observedResult {
	return observe(e.Engine.HandleFrame(f, now))
}

func (e *testEngine) HandleTimeout(now time.Time) observedResult {
	return observe(e.Engine.HandleTimeout(now))
}
