// Package runner drives one Test end-to-end: for each step, it repeatedly
// (a) shows the model the step goal + recent PTY output, (b) gets back one
// Action, (c) executes it against the PTY, until the model calls
// finish_step or the step's turn/time budget runs out.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/driver"
	"github.com/sjhorn/slmtest/internal/spec"
)

// StepOutcome is the recorded result of one step, for the final report.
type StepOutcome struct {
	Step       spec.Step
	Result     agent.StepResult
	Reason     string
	Turns      int
	Transcript []TurnLog
	// TimedOut/Aborted distinguish "the model gave a verdict" from
	// "the harness gave up on the model's behalf" — surfaced separately
	// in reports because they need different follow-up (fix the test vs.
	// fix the model/prompt).
	TimedOut bool
	Aborted  bool
}

// TurnLog captures one turn for the transcript/debug output.
type TurnLog struct {
	UserPrompt string
	RawReply   string
	Action     agent.Action
	PTYOutput  string
	Err        string
}

// Report is the full result of running a Test.
type Report struct {
	Test    *spec.Test
	Steps   []StepOutcome
	Passed  bool // true only if every step passed
	Aborted bool
}

// StepStatus is the single-word verdict shown for a step in both the
// human-readable and JSON reports. It collapses the several independent
// fields on StepOutcome into the one distinction a reader acts on, and
// exists so the two report formats can never drift apart.
type StepStatus string

const (
	StatusPass StepStatus = "pass"
	StatusFail StepStatus = "fail"
	// StatusTimeout means the harness gave up waiting, not that the model
	// judged the step failed — different follow-up (raise the budget or
	// fix a hung command, vs. fix the system under test).
	StatusTimeout StepStatus = "timeout"
	// StatusAbort means the run could not continue at all: a dead PTY or
	// an unusable SLM endpoint. It says nothing about the system under test.
	StatusAbort StepStatus = "abort"
)

// Status reports how this step ended. Order matters: an abort or a
// timeout is a harness-level outcome and outranks whatever Result happens
// to hold (which is unset for aborts).
func (s StepOutcome) Status() StepStatus {
	switch {
	case s.Aborted:
		return StatusAbort
	case s.TimedOut:
		return StatusTimeout
	case s.Result == agent.ResultPass:
		return StatusPass
	default:
		return StatusFail
	}
}

// --- JSON report ---
//
// The -json output is a CI contract, so it gets an explicit shape rather
// than whatever the Go structs happen to look like. Two deliberate
// differences from the in-memory Report: each step's spec fields are
// flattened into the outcome (the Go structs nest them), and the parsed
// Test is not echoed wholesale — its Steps would duplicate every step
// already present under "steps".

type jsonReport struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Passed      bool       `json:"passed"`
	Aborted     bool       `json:"aborted"`
	Steps       []jsonStep `json:"steps"`
}

type jsonStep struct {
	Index      int        `json:"index"`
	Title      string     `json:"title"`
	Goal       string     `json:"goal"`
	Hint       string     `json:"hint,omitempty"`
	Expect     string     `json:"expect"`
	Status     StepStatus `json:"status"`
	Reason     string     `json:"reason"`
	Turns      int        `json:"turns"`
	Transcript []jsonTurn `json:"transcript"`
}

type jsonTurn struct {
	UserPrompt string        `json:"user_prompt"`
	RawReply   string        `json:"raw_reply,omitempty"`
	Action     *agent.Action `json:"action,omitempty"`
	PTYOutput  string        `json:"pty_output,omitempty"`
	Err        string        `json:"error,omitempty"`
}

// MarshalJSON renders the report in the documented CI shape.
func (r *Report) MarshalJSON() ([]byte, error) {
	out := jsonReport{
		Passed:  r.Passed,
		Aborted: r.Aborted,
		Steps:   make([]jsonStep, 0, len(r.Steps)),
	}
	if r.Test != nil {
		out.Name = r.Test.Name
		out.Description = r.Test.Description
	}
	for _, s := range r.Steps {
		js := jsonStep{
			Index:      s.Step.Index,
			Title:      s.Step.Title,
			Goal:       s.Step.Goal,
			Hint:       s.Step.Hint,
			Expect:     s.Step.Expect,
			Status:     s.Status(),
			Reason:     s.Reason,
			Turns:      s.Turns,
			Transcript: make([]jsonTurn, 0, len(s.Transcript)),
		}
		for _, tl := range s.Transcript {
			jt := jsonTurn{
				UserPrompt: tl.UserPrompt,
				RawReply:   tl.RawReply,
				PTYOutput:  tl.PTYOutput,
				Err:        tl.Err,
			}
			// A turn whose reply failed to parse has no action; emitting a
			// zero-valued one would read as a real (empty) action.
			if tl.Action.Action != "" {
				action := tl.Action
				jt.Action = &action
			}
			js.Transcript = append(js.Transcript, jt)
		}
		out.Steps = append(out.Steps, js)
	}
	return json.Marshal(out)
}

// systemPromptCore is the driver-agnostic half of the system prompt: the
// JSON reply contract, the three core actions (wait/finish_step/
// abort_test, handled inline by the runner regardless of driver), and
// the pass/fail judgement discipline. It carries no UI-surface-specific
// language — that lives in each driver's own PromptFragment(), composed
// in by buildSystemPrompt. Splitting the prompt this way is the highest
// silent-regression risk in the driver-abstraction refactor, which is
// why it has a golden-string test (systemprompt_golden_test.go) guarding
// the exact composed text going forward.
const systemPromptCore = `You are operating a test harness to complete one step of a test script. You are not chatting with a user — every reply you send is parsed as a single JSON object and used to control the system under test directly.

Reply with EXACTLY one JSON object matching this schema, and nothing else (no prose, no markdown fence):

{
  "thought": "<optional, one short sentence>",
  "action": %s,
  "wait_ms": <int, optional, default 1500>,
  "step_result": "pass" | "fail",   // required for finish_step
  "reason": "<required for finish_step and abort_test>"
  ... plus whatever fields the chosen action's own parameters require, described below
}

Rules:
- wait: takes no action, just waits and shows you the current state again. Use when a previous action (a build, an install, a download, a page settling) is likely still in progress.
- finish_step: ends the current step. Use "pass" only if the Expect criterion is clearly satisfied by output you have actually seen. Use "fail" if you're confident it cannot be satisfied (command not found, wrong result, contradicts Expect) — don't guess "pass".
- abort_test: only if the environment itself is broken (process died, container unusable) — not for a step simply failing.
- A Hint is a suggestion, not a requirement. If it doesn't work, reason about why and try something else before failing the step.
- Judge only by output you can see in this conversation, never by assumption.
- Every action below other than run_command/send_keys takes its own fields nested inside a "params" object, e.g. {"action": "click", "params": {"target": "#submit"}} or {"action": "press_key", "params": {"key": "enter"}}. run_command/send_keys are the one exception — their fields are top-level ("command", "press_enter"), not nested.

%s

Action-specific rules:
%s`

// buildSystemPrompt composes the full system prompt for a run: the core
// fragment above, the action enum (driver actions + the three core
// actions), the driver's own PromptFragment(), and each offered action's
// own Description. Built once per run — see Run.
func buildSystemPrompt(drv driver.Driver) string {
	actions := drv.Actions()
	names := make([]string, 0, len(actions)+3)
	for _, a := range actions {
		names = append(names, string(a.Type))
	}
	names = append(names, "wait", "finish_step", "abort_test")

	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}

	var rules strings.Builder
	for _, a := range actions {
		fmt.Fprintf(&rules, "- %s: %s\n", a.Type, a.Description)
	}

	return fmt.Sprintf(systemPromptCore, strings.Join(quoted, " | "), drv.PromptFragment(), strings.TrimRight(rules.String(), "\n"))
}

// Options configures a run.
type Options struct {
	Shell         string
	StepTimeout   time.Duration // per-step wall clock budget; 0 = no limit
	CommandWaitMS int           // default wait after run_command if the model omits wait_ms
	// ContinueOnFail attempts every step even after one fails, instead of
	// stopping at the first failure. This is a property of the run, not of
	// the spec: CI usually wants the full picture, while someone iterating
	// locally wants the fast exit. An abort still ends the run either way.
	ContinueOnFail bool
	// ExecPrefix wraps the shell in a sandbox or a remote session:
	// {"docker","run","--rm","-it","ubuntu:24.04"} launches the spec's
	// shell inside that container instead of on the host. The harness
	// stays agnostic about which sandbox — anything that takes a command
	// as trailing arguments and gives it a terminal works.
	ExecPrefix []string
	Verbose    func(format string, args ...any)
	// DriverName selects which registered driver.Driver drives this run.
	// Empty means "use the spec's driver field", which itself defaults to
	// "tui" — so an empty DriverName reproduces today's only behavior.
	DriverName string
}

// defaultSize is the generic fallback terminal geometry applied when
// neither the spec nor the driver has an opinion. It matches
// ptydriver.DefaultRows/DefaultCols so the tui driver's behavior is
// unchanged; a driver with no Resizable concept simply never sees it.
var defaultSize = spec.Size{Rows: 40, Cols: 200}

func Run(ctx context.Context, t *spec.Test, client *agent.Client, opts Options) (*Report, error) {
	log := opts.Verbose
	if log == nil {
		log = func(string, ...any) {}
	}

	driverName := opts.DriverName
	if driverName == "" {
		driverName = t.Driver
	}
	if driverName == "" {
		driverName = "tui"
	}
	factory, ok := driver.Get(driverName)
	if !ok {
		return nil, driver.ErrUnknownDriver(driverName)
	}

	shell := opts.Shell
	if shell == "" {
		shell = t.Shell
	}
	// The exec prefix wraps the shell rather than replacing it, so the
	// spec's own `shell` field still decides what runs inside the sandbox.
	// Argv/Env only matter to process-based drivers (today, "tui"); a
	// future non-process driver simply ignores them.
	argv := append(append([]string{}, opts.ExecPrefix...), shell)
	drv, err := factory(ctx, driver.Config{
		Argv:    argv,
		Env:     shellEnv(t.Term),
		Options: t.DriverOptions,
	})
	if err != nil {
		return nil, fmt.Errorf("starting %s driver: %w", driverName, err)
	}
	defer drv.Close()

	// Apply the test-wide size once; per-step overrides are applied in
	// runStep and reverted to this on the next step that doesn't override.
	// Resize is the one optional escape hatch (driver.Resizable) — a
	// driver whose device class has no resizable viewport just skips it.
	testSize := t.Size
	if testSize.IsZero() {
		testSize = defaultSize
	}
	if err := resizeIfSupported(drv, testSize); err != nil {
		return nil, fmt.Errorf("setting terminal size: %w", err)
	}

	systemPrompt := buildSystemPrompt(drv)

	report := &Report{Test: t, Passed: true}

	// A short rolling summary of prior steps, threaded into each step's
	// first prompt. Per-step history is still reset (see runStep) — this
	// is the deliberate exception: a step like "restart the service you
	// configured earlier" is unanswerable without it, but replaying whole
	// prior transcripts would defeat the point of resetting at all.
	var priorOutcomes []string

	for _, step := range t.Steps {
		log("=== step %d: %s ===", step.Index, step.Title)

		// A step's Size applies to that step only; anything without one
		// runs at the test's size, so a single TUI step doesn't silently
		// reshape the rest of the run.
		size := step.Size
		if size.IsZero() {
			size = testSize
		}
		if err := resizeIfSupported(drv, size); err != nil {
			return nil, fmt.Errorf("resizing terminal for step %d: %w", step.Index, err)
		}

		outcome := runStep(ctx, drv, client, systemPrompt, t, step, opts, priorOutcomes)
		report.Steps = append(report.Steps, outcome)
		priorOutcomes = append(priorOutcomes,
			fmt.Sprintf("Step %d (%s): %s — %s", step.Index, step.Title, outcome.Status(), outcome.Reason))

		if outcome.Aborted {
			report.Aborted = true
			report.Passed = false
			log("aborted: %s", outcome.Reason)
			break
		}
		if outcome.Result != agent.ResultPass {
			report.Passed = false
			log("step %d FAILED: %s", step.Index, outcome.Reason)
			// Stop-on-first-failure is the default because later steps
			// usually assume earlier ones succeeded (services running,
			// files created), so their verdicts would be noise. Under
			// ContinueOnFail the caller has accepted that: the PTY keeps
			// whatever state the failed step left behind, and later steps
			// run against it.
			if !opts.ContinueOnFail {
				break
			}
			continue
		}
		log("step %d passed: %s", step.Index, outcome.Reason)
	}

	return report, nil
}

// resizeIfSupported applies size to drv if it implements driver.Resizable,
// and is a silent no-op otherwise — most device classes (a phone screen,
// a TV) have no resizable viewport concept at all.
func resizeIfSupported(drv driver.Driver, size spec.Size) error {
	r, ok := drv.(driver.Resizable)
	if !ok {
		return nil
	}
	return r.Resize(size.Rows, size.Cols)
}

// shellEnv builds the environment for the PTY shell. A nil return means
// "inherit the parent environment", which is the default; a non-empty
// Term requires materializing the whole environment, since setting
// exec.Cmd.Env replaces it wholesale rather than adding to it.
func shellEnv(term string) []string {
	if term == "" {
		return nil
	}
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TERM="+term)
}

// maxPriorOutcomes bounds the rolling summary. Five is enough for a step
// to reference recent setup without the prompt growing without limit in a
// long spec — and the older an outcome is, the less likely the current
// step turns on it.
const maxPriorOutcomes = 5

// dispatchParams is the generic wire shape runner marshals for
// run_command/send_keys before handing it to driver.Dispatch. Field
// names/tags intentionally match ptydriver's own RunCommandParams/
// SendKeysParams (and any future driver adopting the same shape for
// these bespoke actions) so the runner never needs to import a
// concrete driver package to talk to it.
type dispatchParams struct {
	Command    string `json:"command,omitempty"`
	PressEnter bool   `json:"press_enter,omitempty"`
	WaitMS     int    `json:"wait_ms,omitempty"`
}

func runStep(ctx context.Context, drv driver.Driver, client *agent.Client, systemPrompt string, t *spec.Test, step spec.Step, opts Options, priorOutcomes []string) StepOutcome {
	outcome := StepOutcome{Step: step}
	maxTurns := t.MaxTurnsPerStep
	if maxTurns <= 0 {
		maxTurns = 6
	}

	stepCtx := ctx
	var cancel context.CancelFunc
	if opts.StepTimeout > 0 {
		stepCtx, cancel = context.WithTimeout(ctx, opts.StepTimeout)
		defer cancel()
	}

	// Build the running per-step chat history ourselves so we control
	// exactly what's kept (PTY output) vs. dropped (the model's own
	// "thought" field) — see agent.Action.Thought doc comment for why.
	var msgs []agent.Message

	// Consecutive identical actions are tracked so the harness can break a
	// loop the model cannot see it is in — see repeatNudge.
	var lastSignature string
	repeats := 0

	firstPrompt := fmt.Sprintf(
		"%sSTEP %d: %s\nGoal: %s\nHint: %s\nExpect: %s\n\n(No terminal output yet — this is the start of the step.)",
		priorSummary(priorOutcomes), step.Index, step.Title, step.Goal, orNone(step.Hint), step.Expect)
	nextUser := firstPrompt

	for turn := 1; turn <= maxTurns; turn++ {
		outcome.Turns = turn

		select {
		case <-stepCtx.Done():
			outcome.TimedOut = true
			outcome.Result = agent.ResultFail
			outcome.Reason = "step timed out before the model reached a verdict"
			return outcome
		default:
		}

		msgs = trimStepHistory(msgs)
		reply, err := client.Complete(stepCtx, agent.Turn{
			System:   systemPrompt,
			History:  msgs,
			UserText: nextUser,
		})
		tlog := TurnLog{UserPrompt: nextUser}
		if err != nil {
			tlog.Err = err.Error()
			outcome.Transcript = append(outcome.Transcript, tlog)
			outcome.Aborted = true
			outcome.Reason = "SLM endpoint error: " + err.Error()
			return outcome
		}
		tlog.RawReply = reply
		// Record the user turn we just sent now that we know the call
		// succeeded — every branch below only needs to append the
		// assistant reply to keep history in the correct user/assistant
		// order for the next Complete() call.
		msgs = append(msgs, agent.Message{Role: "user", Content: nextUser})

		action, perr := agent.ParseAction(reply)
		if perr != nil {
			// Feed the schema error back to the model as the next turn
			// instead of aborting — small models often self-correct
			// given one precise error message.
			tlog.Err = perr.Error()
			outcome.Transcript = append(outcome.Transcript, tlog)
			msgs = append(msgs, agent.Message{Role: "assistant", Content: reply})
			nextUser = "Your reply could not be parsed: " + perr.Error() + "\nReply again with ONLY the corrected JSON object."
			continue
		}
		tlog.Action = action

		// Count consecutive identical actions before dispatching, so the
		// nudge can be appended to the output the model is about to see.
		// action.Params is included so this is meaningful for a generic
		// driver action too (press_key, click, ...) — those carry their
		// real content in Params, not Command, which only run_command/
		// send_keys populate; without Params here, two press_key calls
		// with genuinely different keys would look identical.
		signature := string(action.Action) + "\x00" + action.Command + "\x00" + string(action.Params)
		if signature == lastSignature {
			repeats++
		} else {
			repeats = 0
			lastSignature = signature
		}

		switch action.Action {
		case agent.ActionFinishStep:
			tlog.PTYOutput = ""
			outcome.Transcript = append(outcome.Transcript, tlog)
			outcome.Result = action.StepResult
			outcome.Reason = action.Reason
			return outcome

		case agent.ActionAbortTest:
			outcome.Transcript = append(outcome.Transcript, tlog)
			outcome.Aborted = true
			outcome.Reason = action.Reason
			return outcome

		case agent.ActionRunCommand, agent.ActionSendKeys:
			// run_command is DEFINED as "type the command and press
			// Enter", so press_enter is ignored for it. Honoring
			// press_enter:false there produced a silent no-op: the text
			// was typed, nothing ran, no output ever appeared, and the
			// model burned its whole turn budget waiting for a result that
			// could not arrive. A small model emitting the field by
			// mistake is exactly the case this has to survive — see
			// CLAUDE.md, "what running against a real model has shown".
			// send_keys is where not pressing Enter is meaningful.
			pressEnter := true
			if action.Action == agent.ActionSendKeys {
				pressEnter = false
				if action.PressEnter != nil {
					pressEnter = *action.PressEnter
				}
			}
			waitMS := action.WaitMS
			if waitMS <= 0 {
				waitMS = opts.CommandWaitMS
				if waitMS <= 0 {
					waitMS = 1500
				}
			}
			params, _ := json.Marshal(dispatchParams{
				Command:    action.Command,
				PressEnter: pressEnter,
				WaitMS:     waitMS,
			})
			obs, err := drv.Dispatch(stepCtx, driver.ActionType(action.Action), params)
			if err != nil {
				tlog.Err = err.Error()
				outcome.Transcript = append(outcome.Transcript, tlog)
				if recoverable, note := dispatchErrorNote(err); recoverable {
					msgs = append(msgs, agent.Message{Role: "assistant", Content: reply})
					nextUser = note + repeatedMistakeNudge(repeats, action.Action)
					continue
				}
				outcome.Aborted = true
				outcome.Reason = "driver error: " + err.Error()
				return outcome
			}
			out := obs.Text
			tlog.PTYOutput = out
			outcome.Transcript = append(outcome.Transcript, tlog)
			msgs = append(msgs, agent.Message{Role: "assistant", Content: action.ReplayJSON()})
			nextUser = "Terminal output:\n" + orNone(truncateOutput(out)) +
				notExecutedNote(action, pressEnter) + strayVerdictNote(action) + repeatNudge(repeats)

		case agent.ActionWait:
			waitMS := action.WaitMS
			if waitMS <= 0 {
				waitMS = 2000
			}
			obs, err := drv.Observe(stepCtx, time.Duration(waitMS)*time.Millisecond)
			if err != nil {
				tlog.Err = err.Error()
				outcome.Transcript = append(outcome.Transcript, tlog)
				outcome.Aborted = true
				outcome.Reason = "driver error: " + err.Error()
				return outcome
			}
			out := obs.Text
			tlog.PTYOutput = out
			outcome.Transcript = append(outcome.Transcript, tlog)
			msgs = append(msgs, agent.Message{Role: "assistant", Content: action.ReplayJSON()})
			nextUser = "Terminal output:\n" + orNone(truncateOutput(out)) + strayVerdictNote(action) + repeatNudge(repeats)

		default:
			// Any action beyond the original five: a shared primitive
			// (click, type_text, press_key, ...) or a driver's own
			// bespoke action (e.g. a browser driver's "navigate"). Its
			// params travel verbatim in action.Params — agent has no
			// generic way to know their shape, so the active driver's
			// own Dispatch validates them and rejects a name it doesn't
			// offer via driver.UnsupportedActionError, handled below the
			// same way as run_command/send_keys's dispatch error.
			params := action.Params
			if params == nil {
				params = json.RawMessage(`{}`)
			}
			obs, err := drv.Dispatch(stepCtx, driver.ActionType(action.Action), params)
			if err != nil {
				tlog.Err = err.Error()
				outcome.Transcript = append(outcome.Transcript, tlog)
				if recoverable, note := dispatchErrorNote(err); recoverable {
					msgs = append(msgs, agent.Message{Role: "assistant", Content: reply})
					nextUser = note + repeatedMistakeNudge(repeats, action.Action)
					continue
				}
				outcome.Aborted = true
				outcome.Reason = "driver error: " + err.Error()
				return outcome
			}
			out := obs.Text
			tlog.PTYOutput = out
			outcome.Transcript = append(outcome.Transcript, tlog)
			msgs = append(msgs, agent.Message{Role: "assistant", Content: action.ReplayJSON()})
			nextUser = "Observation:\n" + orNone(truncateOutput(out)) + emptyTypeTextNote(action) + strayVerdictNote(action) + repeatNudge(repeats)
		}

		if !drv.Alive() {
			outcome.Aborted = true
			outcome.Reason = "shell process exited unexpectedly"
			return outcome
		}
	}

	outcome.Result = agent.ResultFail
	outcome.Reason = fmt.Sprintf("used all %d turns without calling finish_step", maxTurns)
	return outcome
}

// dispatchErrorNote classifies a driver.Dispatch/Observe error. A
// driver.UnsupportedActionError means the model picked an action name
// the active driver doesn't offer — the same kind of recoverable
// mistake a JSON parse error is, so it's fed back for a retry rather
// than aborting the whole run (matching this project's general
// small-model-robustness policy: state precisely what went wrong and
// let the model self-correct). Any other error means the driver/process
// itself is broken, which genuinely cannot be recovered from mid-step.
func dispatchErrorNote(err error) (recoverable bool, note string) {
	var uae *driver.UnsupportedActionError
	if errors.As(err, &uae) {
		return true, "That action is not available: " + err.Error() +
			"\nReply again using one of the actions described in your instructions."
	}
	var bpe *driver.BadParamsError
	if errors.As(err, &bpe) {
		return true, "Your action's params were invalid: " + err.Error() +
			"\nReply again with the correct params for this action, nested under \"params\" as your instructions describe (except run_command/send_keys, whose fields stay top-level)."
	}
	return false, ""
}

// strayVerdictNote points out a verdict attached to an action that cannot
// deliver one. The action still runs — see agent.StrayVerdict for why
// naming it beats rejecting it.
//
// It deliberately does NOT echo back the verdict the model claimed. An
// earlier version interpolated it into a suggested reply, which read as
// "reply with step_result: pass" to any model that had written pass — and
// a 0.5B model that had typed a command without ever executing it was
// coached straight into a false pass. A harness nudge must never supply
// the verdict; that judgement is the one thing this tool delegates.
func strayVerdictNote(a agent.Action) string {
	if !agent.StrayVerdict(a) {
		return ""
	}
	return fmt.Sprintf("\n\nNOTE: \"step_result\" was set on a %s and ignored — only finish_step carries "+
		"a verdict. Judge from the terminal output above, then reply with finish_step and whichever "+
		"of \"pass\" or \"fail\" the output actually supports.", a.Action)
}

// emptyTypeTextNote confirms an empty type_text was itself a legitimate,
// complete action — not a silent failure worth retrying.
//
// Typing "" into a focused field produces zero visible change in the
// next observation: indistinguishable, from the model's point of view,
// from an action that simply didn't register. Observed live running a
// real Cucumber-derived spec (examples/cucumber-sample-checkout-split-
// test.md) against a real model: it typed an empty string into a field
// deliberately left blank, saw no change, and retried the identical
// action several times before moving on — costing turns on a step
// already tight on budget. This states the fact directly, the same way
// notExecutedNote does for send_keys's own "no visible change" ambiguity
// — it does not say whether the step passed.
func emptyTypeTextNote(a agent.Action) string {
	if string(a.Action) != string(driver.ActionTypeText) {
		return ""
	}
	var p driver.TypeTextParams
	if err := json.Unmarshal(a.Params, &p); err != nil || p.Text != "" {
		return ""
	}
	return "\n\nNOTE: you typed an empty string. If the field was meant to stay blank, that already " +
		"succeeded — the lack of visible change is the correct, complete result, not a sign the action " +
		"failed. There is no need to type it again."
}

// notExecutedNote states a mechanical fact the model may have missed:
// send_keys does not press Enter, so text was typed but nothing ran.
//
// A 0.5B model used send_keys for a whole command, saw the terminal echo
// its own input back, and reported pass on the strength of it. The marker
// string really was on screen — as the echo of what it typed, never as
// command output. This states what happened; it does not say whether the
// step passed.
func notExecutedNote(a agent.Action, pressedEnter bool) string {
	if a.Action != agent.ActionSendKeys || pressedEnter {
		return ""
	}
	return "\n\nNOTE: send_keys does not press Enter, so that text has been typed into the terminal " +
		"but has NOT run. Any text above matching what you typed is the terminal echoing your input, " +
		"not command output. Use run_command to execute it."
}

// repeatNudge tells a model that it is repeating itself. A 1.5B model was
// observed running the same correct command four times against unchanged
// output, with the Expect criterion plainly satisfied in front of it,
// until the turn budget ran out — it could see the output but never drew
// the conclusion. The harness cannot judge the step on the model's behalf
// (that is the whole design), but it can point out the thing the model is
// demonstrably not noticing.
//
// This is the same principle as feeding parse errors back rather than
// aborting: state precisely what is wrong and let the model correct it.
//
// Like strayVerdictNote, it must never name a verdict for the model. An
// earlier version said "reply with step_result \"pass\" now", which is
// the harness putting its thumb on the scale of the one judgement it
// exists to delegate.
func repeatNudge(repeats int) string {
	if repeats < 1 {
		return ""
	}
	return fmt.Sprintf("\n\nNOTE: you have now run that exact command %d times in a row and the "+
		"terminal output above has not changed. Do not run it again. Either reply with finish_step "+
		"and whichever of \"pass\" or \"fail\" the output actually supports, or run a DIFFERENT "+
		"command. Do not repeat this one.", repeats+1)
}

// repeatedMistakeNudge is repeatNudge's counterpart for the recoverable-
// dispatch-error path (driver.UnsupportedActionError / BadParamsError) —
// dispatchErrorNote's own message is appended to nextUser directly on
// that path, bypassing repeatNudge entirely, so a model stuck sending the
// exact same invalid action/params never got the "you are repeating
// yourself" escalation the success path already gives.
//
// Observed live, more than once, running examples/nano-edit-test.md and
// examples/tui-claude-chat-test.md against a real model: it sent
// press_key with a flat top-level "key" field (instead of nested under
// "params") on turn 1, got back exactly the BadParamsError message
// dispatchErrorNote produces, and then repeated the byte-identical
// mistake on every remaining turn of the step's budget — burning the
// whole step on one uncorrected error, unlike the "navigate" case
// (CLAUDE.md, "Driver abstraction") where a model self-corrected on its
// very next attempt. repeatNudge's own wording doesn't fit here (it talks
// about "terminal output ... has not changed" and nudges toward
// finish_step, neither of which makes sense when nothing has actually
// succeeded yet), hence a distinct message rather than reusing it as-is.
//
// After a THIRD identical failure (repeats >= 2), this stops restating
// the rule in prose — a model that didn't act on it twice already isn't
// likely to act on a third rephrasing — and instead hands over a literal
// shape to copy, naming the actual action. Which literal shape depends on
// which side of the schema's one asymmetry the action is on: run_command/
// send_keys keep their fields at the top level (everything else nests
// under "params"), so the escalation for those two names the opposite
// rule from every other action's.
func repeatedMistakeNudge(repeats int, action agent.ActionType) string {
	if repeats < 1 {
		return ""
	}
	msg := fmt.Sprintf("\n\nNOTE: this is the exact same mistake %d times in a row — whatever you "+
		"tried is not working. Re-read the error above carefully and change the SHAPE of your reply "+
		"(which fields are nested where), not just retry it unchanged.", repeats+1)
	if repeats < 2 {
		return msg
	}
	if action == agent.ActionRunCommand || action == agent.ActionSendKeys {
		msg += fmt.Sprintf("\n\nSTOP AND COPY THIS SHAPE EXACTLY, filling in only the values: "+
			`{"action": %q, "command": "..."}`+" — %s's own fields belong at the TOP LEVEL, never nested under \"params\".",
			string(action), action)
	} else {
		msg += fmt.Sprintf("\n\nSTOP AND COPY THIS SHAPE EXACTLY, filling in only the values: "+
			`{"action": %q, "params": {...}}`+" — %s's own fields belong NESTED UNDER \"params\", never at the top level.",
			string(action), action)
	}
	return msg
}

// maxStepHistoryTurns caps how many past user/assistant turn-pairs stay in
// a step's own chat history. Plain shell output from every other spec in
// this project is small enough that per-step history was never a problem
// even across many turns. A real full-screen TUI is a different order of
// magnitude — dense ANSI escape codes on every redraw — and a step that
// needs many `wait` turns to observe genuinely slow real work (e.g.
// waiting for an agentic coding session to finish) can accumulate enough
// raw output to exceed even a generous context window within a handful of
// turns. Confirmed live: a step blew past both an 8192 and a 32768 token
// context in exactly this way — see docs/model-runs.md, "Going further
// with Qwen3.5-9B." Judging "has the state I'm waiting for arrived" only
// needs recent turns, not the full history since the step began, so
// dropping the oldest ones is safe.
const maxStepHistoryTurns = 6

// trimStepHistory keeps only the most recent maxStepHistoryTurns
// user/assistant pairs, dropping older ones from the front. If any were
// dropped, a note is folded into the oldest retained user message's own
// content rather than inserted as a separate message, so strict
// user/assistant alternation is preserved exactly — not every
// OpenAI-compatible server tolerates two consecutive same-role messages.
func trimStepHistory(msgs []agent.Message) []agent.Message {
	maxMsgs := maxStepHistoryTurns * 2
	if len(msgs) <= maxMsgs {
		return msgs
	}
	dropped := (len(msgs) - maxMsgs) / 2 // whole turn-pairs only
	if dropped == 0 {
		return msgs
	}
	kept := make([]agent.Message, len(msgs)-dropped*2)
	copy(kept, msgs[dropped*2:])
	note := fmt.Sprintf("[%d earlier turn(s) in this step were dropped from your context to control "+
		"its size — only the most recent %d turns remain below.]\n\n", dropped, maxStepHistoryTurns)
	kept[0].Content = note + kept[0].Content
	return kept
}

// priorSummary renders the last few step outcomes as a preamble. It
// carries verdicts and reasons only — never terminal output, which is
// what would actually bloat the context and what the per-step reset
// exists to discard.
func priorSummary(prior []string) string {
	if len(prior) == 0 {
		return ""
	}
	if len(prior) > maxPriorOutcomes {
		prior = prior[len(prior)-maxPriorOutcomes:]
	}
	var b strings.Builder
	b.WriteString("Earlier steps in this test (for context only — do not re-verify them):\n")
	for _, line := range prior {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// maxSingleTurnOutputChars caps how much raw PTY output from ONE turn is
// shown to the model. A real full-screen TUI can emit an enormous burst
// of bytes in a single read window — dense ANSI escape codes on every
// character of an animated redraw — enough on its own to exceed even a
// generous context window in a single turn, independent of how much
// history has accumulated. maxStepHistoryTurns caps growth ACROSS turns;
// this caps a single turn's own content, which that alone cannot help
// with. Confirmed live: one turn's output alone pushed a request past a
// 32768-token context — see docs/model-runs.md, "Going further with
// Qwen3.5-9B." Keeping the TAIL rather than the head is deliberate: the
// most recent bytes are the current state, which is what the model needs
// to judge; whatever is cut is earlier churn that already settled.
const maxSingleTurnOutputChars = 6000

// truncateOutput keeps at most maxSingleTurnOutputChars of s, from the
// end, noting how much was cut if anything was.
func truncateOutput(s string) string {
	if len(s) <= maxSingleTurnOutputChars {
		return s
	}
	cut := len(s) - maxSingleTurnOutputChars
	return fmt.Sprintf("[%d earlier character(s) from this turn's output were dropped to control "+
		"context size — showing the most recent %d below.]\n\n%s", cut, maxSingleTurnOutputChars, s[cut:])
}
