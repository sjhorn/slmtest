// Package nulldriver is a tiny, dependency-free driver.Driver
// implementation used to test that the runner's dispatch and prompt
// composition are genuinely driver-agnostic, independent of any real
// terminal/browser/etc I/O. It plays the same role for internal/driver
// consumers that internal/agent's fakeSLM plays for the model side:
// deterministic, scripted, no real backend.
//
// It is registered under the name "null" so it can also be selected via
// spec frontmatter/-driver flag for harness-only smoke testing, but it
// is not intended to drive anything real — Dispatch/Observe just return
// whatever was scripted.
package nulldriver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sjhorn/slmtest/internal/driver"
)

func init() {
	driver.Register("null", New)
}

// Driver is a scripted driver.Driver. Observations is consumed in
// order: each call to Dispatch or Observe pops the next one. If the
// script runs out, subsequent calls return an error rather than
// silently repeating the last observation — an unexpected extra call is
// a bug worth surfacing loudly, mirroring internal/runner's fakeSLM.
type Driver struct {
	Observations []driver.Observation
	pos          int
	alive        bool
	closed       bool

	// Calls records every Dispatch invocation, for assertions in tests.
	Calls []Call
}

// Call records one Dispatch invocation.
type Call struct {
	Action driver.ActionType
	Params json.RawMessage
}

// New is nulldriver's driver.Factory. cfg is accepted for interface
// conformance but otherwise ignored — nulldriver has nothing to launch.
func New(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
	return &Driver{alive: true}, nil
}

// NewScripted builds a Driver pre-loaded with a sequence of
// observations, for direct use in tests without going through the
// registry.
func NewScripted(obs ...driver.Observation) *Driver {
	return &Driver{Observations: obs, alive: true}
}

var _ driver.Driver = (*Driver)(nil)

func (d *Driver) Name() string { return "null" }

// Actions offers one of every Layer 1 primitive plus a bespoke "noop",
// so tests can exercise the runner's generic dispatch path against
// every action shape a real driver might offer.
func (d *Driver) Actions() []driver.ActionSpec {
	return []driver.ActionSpec{
		driver.PrimitivePressKey,
		driver.PrimitiveDirectional,
		driver.PrimitiveClick,
		driver.PrimitiveTypeText,
		{Type: "noop", Description: "Does nothing; for harness testing only.", ParamSchema: json.RawMessage(`{"type":"object"}`)},
	}
}

func (d *Driver) PromptFragment() string {
	return "This is a scripted null driver used only for harness testing; its actions have no real effect."
}

func (d *Driver) next() (driver.Observation, error) {
	if d.pos >= len(d.Observations) {
		return driver.Observation{}, fmt.Errorf("nulldriver: script exhausted after %d observation(s)", d.pos)
	}
	obs := d.Observations[d.pos]
	d.pos++
	return obs, nil
}

func (d *Driver) Dispatch(ctx context.Context, action driver.ActionType, params json.RawMessage) (driver.Observation, error) {
	d.Calls = append(d.Calls, Call{Action: action, Params: params})
	return d.next()
}

func (d *Driver) Observe(ctx context.Context, wait time.Duration) (driver.Observation, error) {
	return d.next()
}

func (d *Driver) Alive() bool { return d.alive && !d.closed }

func (d *Driver) Close() error {
	d.closed = true
	return nil
}

// Kill marks the driver as no longer alive, for tests that need to
// exercise the runner's "shell process exited unexpectedly" path
// generically.
func (d *Driver) Kill() { d.alive = false }
