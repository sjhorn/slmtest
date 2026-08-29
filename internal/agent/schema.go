// Package agent talks to the small language model that drives the test.
//
// Protocol shape (one JSON object per turn, in both directions):
//
//	Runner -> model: a system prompt (once) + a user message containing the
//	current step's Goal/Hint/Expect, the recent terminal output, and the
//	turn/step budget remaining.
//
//	Model -> runner: exactly one Action object, and nothing else. The
//	runner rejects/retries a turn if the model's reply is not valid JSON
//	matching this schema — small models drift into prose, so the harness
//	must enforce the contract rather than assume it.
package agent

import "encoding/json"

// ActionType enumerates the only moves the model is allowed to make.
// Keeping this list small is deliberate: every extra action type is
// another way for a small model to pick the wrong one.
type ActionType string

const (
	// ActionRunCommand sends Command to the PTY followed by Enter (unless
	// PressEnter is explicitly false), then waits WaitMS before the next
	// output snapshot is taken.
	ActionRunCommand ActionType = "run_command"

	// ActionSendKeys sends raw keystrokes with no implicit newline — for
	// interactive programs (vim, a REPL, a y/n prompt) where the model
	// needs to send a control character or a partial line.
	ActionSendKeys ActionType = "send_keys"

	// ActionWait takes no PTY action; it just waits WaitMS and re-reads
	// output. Used when a command is still running (e.g. a build, a
	// package install) and the model wants to check again shortly.
	ActionWait ActionType = "wait"

	// ActionFinishStep ends the current step with StepResult ("pass" or
	// "fail") and Reason. This is the only way a step ends — the runner
	// never infers pass/fail on its own from exit codes alone, though it
	// surfaces exit codes to the model to reason over.
	ActionFinishStep ActionType = "finish_step"

	// ActionAbortTest ends the entire test run immediately (e.g. the
	// environment is unusable — wrong container, PTY died, disk full).
	// Distinct from finish_step(fail): abort means "this test cannot
	// continue at all," not "this step failed."
	ActionAbortTest ActionType = "abort_test"
)

// StepResult is the verdict passed with ActionFinishStep.
type StepResult string

const (
	ResultPass StepResult = "pass"
	ResultFail StepResult = "fail"
)

// Action is the strict JSON contract the model must reply with each turn.
type Action struct {
	// Thought is short free-text reasoning. It is shown back to the user
	// in logs/transcripts but is NOT re-fed into the model's own context
	// on the next turn (only the resulting PTY output is) — this keeps
	// the context small and keeps the model reasoning from prior
	// commitments rather than re-reading its own chain of thought.
	Thought string `json:"thought,omitempty"`

	Action ActionType `json:"action"`

	// --- fields for run_command / send_keys ---
	Command    string `json:"command,omitempty"`
	PressEnter *bool  `json:"press_enter,omitempty"` // defaults to true for run_command, false for send_keys
	WaitMS     int    `json:"wait_ms,omitempty"`     // defaults to 1500 if unset

	// --- fields for finish_step / abort_test ---
	StepResult StepResult `json:"step_result,omitempty"`
	Reason     string     `json:"reason,omitempty"`
}

// Validate checks structural invariants beyond what JSON unmarshalling
// already guarantees. Called by the runner immediately after parsing;
// a validation failure is fed back to the model as an error turn rather
// than crashing the run.
func (a Action) Validate() error {
	switch a.Action {
	case ActionRunCommand, ActionSendKeys:
		if a.Command == "" {
			return errMissingField(a.Action, "command")
		}
	case ActionWait:
		// no required fields
	case ActionFinishStep:
		if a.StepResult != ResultPass && a.StepResult != ResultFail {
			return errBadField(a.Action, "step_result", `must be "pass" or "fail"`)
		}
		if a.Reason == "" {
			return errMissingField(a.Action, "reason")
		}
	case ActionAbortTest:
		if a.Reason == "" {
			return errMissingField(a.Action, "reason")
		}
	default:
		return errUnknownAction(a.Action)
	}
	return nil
}

func errMissingField(a ActionType, field string) error {
	return &SchemaError{Msg: string(a) + " requires a non-empty \"" + field + "\" field"}
}
func errBadField(a ActionType, field, rule string) error {
	return &SchemaError{Msg: string(a) + " field \"" + field + "\" " + rule}
}
func errUnknownAction(a ActionType) error {
	return &SchemaError{Msg: "unknown action type " + string(a) +
		"; must be one of: run_command, send_keys, wait, finish_step, abort_test"}
}

// SchemaError is returned for any malformed model reply. The runner sends
// its Error() string back to the model verbatim as the next user turn,
// so keep messages short and actionable.
type SchemaError struct{ Msg string }

func (e *SchemaError) Error() string { return e.Msg }

// ReplayJSON renders the action as the assistant message to put back into
// the model's own history for the next turn. It deliberately drops
// Thought (see the field's doc comment): the model should reason from the
// terminal state it can see, not from its own earlier chain of thought.
// It also normalizes away any code fence the model wrapped its reply in,
// so the history shows the contract's exact shape rather than modelling
// the fence as acceptable output.
func (a Action) ReplayJSON() string {
	a.Thought = ""
	b, err := json.Marshal(a)
	if err != nil {
		// Action is a plain struct of marshalable fields, so this cannot
		// fail in practice; degrade to the empty object rather than
		// panicking mid-run.
		return "{}"
	}
	return string(b)
}

// ParseAction unmarshals and validates one model reply. It also tolerates
// the common small-model failure mode of wrapping JSON in a markdown code
// fence (```json ... ```), stripping it before parsing.
func ParseAction(raw string) (Action, error) {
	clean := stripCodeFence(raw)
	var a Action
	if err := json.Unmarshal([]byte(clean), &a); err != nil {
		return Action{}, &SchemaError{Msg: "reply was not valid JSON: " + err.Error()}
	}
	if err := a.Validate(); err != nil {
		return Action{}, err
	}
	return a, nil
}
