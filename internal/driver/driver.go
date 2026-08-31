// Package driver defines the interface the runner uses to talk to
// whatever UI surface a test is actually driving — a real PTY today,
// eventually a browser, a desktop app, or other device classes. The
// runner (turn loop, spec format, SLM prompting) depends only on this
// interface, never on a concrete driver, so adding a new UI surface
// never touches runner logic.
//
// Two layers make up a driver's contract:
//
//   - Observation is fully generic: every driver reports its current
//     state as opaque text. The runner never distinguishes "this came
//     from a byte-diff" from "this is a fresh snapshot" — both are
//     legitimate under Observe, and a driver is free to pick whichever
//     matches its UI paradigm (see ptydriver's diff-since-last-read vs.
//     a hypothetical browser driver's full-accessibility-tree snapshot).
//   - The action vocabulary is layered, not uniform: a small set of
//     shared interaction primitives (see primitives.go) that any driver
//     whose device class has that kind of input can adopt verbatim,
//     plus room for genuinely bespoke actions a driver defines itself
//     (e.g. the terminal driver's run_command/send_keys). See
//     primitives.go's doc comment for why this is a deliberate middle
//     layer rather than either extreme.
package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Observation is what a driver reports back after an action or a wait:
// driver-rendered text for the LLM prompt, opaque to the runner.
type Observation struct {
	Text string
	// Truncated notes that the driver itself cut this text down (as
	// opposed to the runner's own truncateOutput, which operates on
	// whatever Text a driver returns regardless of this flag).
	Truncated bool
}

// ActionType names one action a driver accepts via Dispatch. Values are
// the same strings used in the model-facing JSON contract.
type ActionType string

// ActionSpec describes one action a driver offers, for both the prose
// system-prompt path (Description) and native-tools mode (ParamSchema
// as a JSON Schema, per the OpenAI tools shape).
type ActionSpec struct {
	Type        ActionType
	Description string
	ParamSchema json.RawMessage
}

// Driver is the runner's entire call surface on a UI-driving backend.
type Driver interface {
	// Name identifies the driver, e.g. "tui". Used in spec frontmatter,
	// the -driver flag, and diagnostics.
	Name() string

	// Actions lists the actions this driver accepts via Dispatch, beyond
	// the three core actions (wait/finish_step/abort_test) the runner
	// handles itself. Typically a mix of shared primitives (see
	// primitives.go) and driver-owned bespoke actions.
	Actions() []ActionSpec

	// PromptFragment is driver-authored prose describing this driver's
	// actions/semantics, composed into the system prompt alongside the
	// runner's own driver-agnostic core fragment. See runner's
	// systemPromptCore for what it does NOT need to repeat.
	PromptFragment() string

	// Dispatch executes one action (by name, with driver-defined params)
	// and returns the driver's resulting observation. action is always
	// one Actions() advertised; params is whatever that action's
	// ParamSchema describes.
	Dispatch(ctx context.Context, action ActionType, params json.RawMessage) (Observation, error)

	// Observe waits (or returns immediately if wait is 0) and then
	// reports the driver's current observation — used for the core
	// "wait" action, and internally by drivers that combine a dispatched
	// action with a settle period.
	Observe(ctx context.Context, wait time.Duration) (Observation, error)

	// Alive reports whether the driven session is still usable.
	Alive() bool

	// Close releases whatever resources this driver holds.
	Close() error
}

// Resizable is an optional interface a driver implements when its
// device class has a resizable viewport (a terminal; not a phone
// screen). The runner type-asserts for it only when a spec's Size is
// set, so it is a narrow escape hatch, not a precedent for more
// optional interfaces — most driver-specific behavior belongs in
// Actions()/Dispatch() instead.
type Resizable interface {
	Resize(rows, cols int) error
}

// Config is what a Factory receives to construct a Driver instance.
type Config struct {
	// Argv is the resolved launch command for process-based drivers
	// (e.g. a shell, possibly wrapped in a sandbox or exec prefix).
	// Ignored by drivers that don't launch a local process.
	Argv []string
	// Env is the process environment for process-based drivers. nil
	// means "inherit the parent's".
	Env []string
	// Options holds driver-namespaced frontmatter keys with their
	// "<driver>_" prefix already stripped, e.g. "tui_shell: /bin/bash"
	// arrives here as Options["shell"] = "/bin/bash".
	Options map[string]string
}

// Factory constructs a Driver from Config.
type Factory func(ctx context.Context, cfg Config) (Driver, error)

var registry = map[string]Factory{}

// Register makes a driver factory available under name, e.g. "tui".
// Drivers register themselves from an init() func so the caller only
// needs to blank-import the package.
func Register(name string, f Factory) {
	registry[name] = f
}

// Get looks up a registered driver factory by name.
func Get(name string) (Factory, bool) {
	f, ok := registry[name]
	return f, ok
}

// Names lists every currently-registered driver name, for diagnostics
// (e.g. an error message when -driver names something unregistered).
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}

// ErrUnknownDriver is returned (wrapped with the requested name) when
// Get fails and the caller wants a ready-made error.
func ErrUnknownDriver(name string) error {
	return fmt.Errorf("unknown driver %q (registered: %v)", name, Names())
}

// UnsupportedActionError is what a driver's Dispatch returns for an
// action name it doesn't offer. It is a distinct type (not a plain
// fmt.Errorf) so the runner can tell "the model picked an action this
// driver doesn't have" — a small-model mistake worth feeding back for a
// retry, the same way a JSON parse error is — apart from a genuine
// driver/process failure, which should abort the run. See
// internal/runner's dispatch error handling.
type UnsupportedActionError struct {
	DriverName string
	Action     ActionType
}

func (e *UnsupportedActionError) Error() string {
	return fmt.Sprintf("action %q is not supported by the %q driver", e.Action, e.DriverName)
}

// NewUnsupportedActionError builds an UnsupportedActionError. Drivers
// call this from Dispatch's default case rather than a plain
// fmt.Errorf, so the runner can recognize it as recoverable.
func NewUnsupportedActionError(driverName string, action ActionType) error {
	return &UnsupportedActionError{DriverName: driverName, Action: action}
}

// BadParamsError is what a driver's Dispatch returns when it recognizes
// the action but the params for it are missing or invalid — a required
// field left empty, most commonly. Like UnsupportedActionError, it is a
// distinct type so the runner can recognize it as a recoverable
// small-model mistake and feed it back for a retry, rather than
// aborting the run.
//
// Found live: a model sent {"action":"navigate","url":"..."} — a flat
// "url" field instead of "params":{"url":"..."} as the system prompt
// specifies. agent.Action has no top-level "url" field, so JSON
// unmarshaling silently dropped it; Params stayed nil; navigate ran
// with an empty URL. Before this type existed, a driver that resolved
// "" leniently (e.g. treating it as "stay on the current page") turned
// a model's mistake into a silent no-op with no error at all — the
// model never learned its params were ignored. A driver should instead
// validate required params and return this, loudly, so the mistake is
// recoverable the same way an unsupported action name is.
type BadParamsError struct {
	DriverName string
	Action     ActionType
	Msg        string
}

func (e *BadParamsError) Error() string {
	return fmt.Sprintf("invalid params for action %q on driver %q: %s", e.Action, e.DriverName, e.Msg)
}

// NewBadParamsError builds a BadParamsError.
func NewBadParamsError(driverName string, action ActionType, msg string) error {
	return &BadParamsError{DriverName: driverName, Action: action, Msg: msg}
}
