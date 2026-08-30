// Package runner drives one Test end-to-end: for each step, it repeatedly
// (a) shows the model the step goal + recent PTY output, (b) gets back one
// Action, (c) executes it against the PTY, until the model calls
// finish_step or the step's turn/time budget runs out.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/ptydriver"
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

const systemPrompt = `You are operating a real Linux shell through a pseudo-terminal to complete one step of a test script. You are not chatting with a user — every reply you send is parsed as a single JSON object and used to control the terminal directly.

Reply with EXACTLY one JSON object matching this schema, and nothing else (no prose, no markdown fence):

{
  "thought": "<optional, one short sentence>",
  "action": "run_command" | "send_keys" | "wait" | "finish_step" | "abort_test",
  "command": "<shell text, required for run_command/send_keys>",
  "press_enter": <bool, optional, send_keys only>,
  "wait_ms": <int, optional, default 1500>,
  "step_result": "pass" | "fail",   // required for finish_step
  "reason": "<required for finish_step and abort_test>"
}

Rules:
- run_command: types the command and ALWAYS presses Enter, waits wait_ms, then you'll be shown new terminal output. Do not set press_enter here; it is ignored.
- send_keys: types the command WITHOUT pressing Enter — use for partial input, control characters (e.g. "\u0003" for Ctrl-C), or interactive prompts. Set "press_enter": true here if you do want a newline sent.
- If a command you ran produced no new output at all, it did not run. Use run_command (not send_keys) to execute something.
- wait: takes no terminal action, just waits and shows you new output. Use when a previous command (build, install, download) is likely still running.
- finish_step: ends the current step. Use "pass" only if the Expect criterion is clearly satisfied by output you have actually seen. Use "fail" if you're confident it cannot be satisfied (command not found, wrong result, contradicts Expect) — don't guess "pass".
- abort_test: only if the environment itself is broken (shell died, container unusable) — not for a step simply failing.
- A Hint is a suggestion, not a requirement. If it doesn't work, reason about why (missing package? wrong path? needs sudo?) and try something else before failing the step.
- Judge only by terminal output you can see in this conversation, never by assumption.`

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
}

func Run(ctx context.Context, t *spec.Test, client *agent.Client, opts Options) (*Report, error) {
	log := opts.Verbose
	if log == nil {
		log = func(string, ...any) {}
	}

	shell := opts.Shell
	if shell == "" {
		shell = t.Shell
	}
	// The exec prefix wraps the shell rather than replacing it, so the
	// spec's own `shell` field still decides what runs inside the sandbox.
	argv := append(append([]string{}, opts.ExecPrefix...), shell)
	drv, err := ptydriver.Start(argv, shellEnv(t.Term))
	if err != nil {
		return nil, fmt.Errorf("starting pty: %w", err)
	}
	defer drv.Close()

	// Apply the test-wide size once; per-step overrides are applied in
	// runStep and reverted to this on the next step that doesn't override.
	testSize := t.Size
	if testSize.IsZero() {
		testSize = spec.Size{Rows: ptydriver.DefaultRows, Cols: ptydriver.DefaultCols}
	}
	if err := drv.Resize(testSize.Rows, testSize.Cols); err != nil {
		return nil, fmt.Errorf("setting terminal size: %w", err)
	}

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
		if err := drv.Resize(size.Rows, size.Cols); err != nil {
			return nil, fmt.Errorf("resizing terminal for step %d: %w", step.Index, err)
		}

		outcome := runStep(ctx, drv, client, t, step, opts, priorOutcomes)
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

func runStep(ctx context.Context, drv *ptydriver.Driver, client *agent.Client, t *spec.Test, step spec.Step, opts Options, priorOutcomes []string) StepOutcome {
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
		signature := string(action.Action) + "\x00" + action.Command
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
			out, err := drv.RunCommand(stepCtx, action.Command, pressEnter, time.Duration(waitMS)*time.Millisecond)
			if err != nil {
				tlog.Err = err.Error()
				outcome.Transcript = append(outcome.Transcript, tlog)
				outcome.Aborted = true
				outcome.Reason = "pty error: " + err.Error()
				return outcome
			}
			tlog.PTYOutput = out
			outcome.Transcript = append(outcome.Transcript, tlog)
			msgs = append(msgs, agent.Message{Role: "assistant", Content: action.ReplayJSON()})
			nextUser = "Terminal output:\n" + orNone(out) + strayVerdictNote(action) + repeatNudge(repeats)

		case agent.ActionWait:
			waitMS := action.WaitMS
			if waitMS <= 0 {
				waitMS = 2000
			}
			out, err := drv.WaitAndSnapshot(stepCtx, time.Duration(waitMS)*time.Millisecond)
			if err != nil {
				tlog.Err = err.Error()
				outcome.Transcript = append(outcome.Transcript, tlog)
				outcome.Aborted = true
				outcome.Reason = "pty error: " + err.Error()
				return outcome
			}
			tlog.PTYOutput = out
			outcome.Transcript = append(outcome.Transcript, tlog)
			msgs = append(msgs, agent.Message{Role: "assistant", Content: action.ReplayJSON()})
			nextUser = "Terminal output:\n" + orNone(out) + strayVerdictNote(action) + repeatNudge(repeats)
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

// strayVerdictNote points out a verdict attached to an action that cannot
// deliver one, and shows the reply that would. The action still runs —
// see agent.StrayVerdict for why naming it beats rejecting it.
func strayVerdictNote(a agent.Action) string {
	if !agent.StrayVerdict(a) {
		return ""
	}
	return fmt.Sprintf("\n\nNOTE: you set \"step_result\": \"%s\" on a %s, which was ignored — only "+
		"finish_step ends a step. If the Expect criterion is satisfied by the output above, your next "+
		"reply should be exactly: {\"action\": \"finish_step\", \"step_result\": \"%s\", \"reason\": \"<why>\"}",
		a.StepResult, a.Action, a.StepResult)
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
func repeatNudge(repeats int) string {
	if repeats < 1 {
		return ""
	}
	return fmt.Sprintf("\n\nNOTE: you have now run that exact command %d times in a row and the "+
		"terminal output above has not changed. Do not run it again. Read the output: if it "+
		"satisfies the Expect criterion, reply with finish_step and step_result \"pass\" now. "+
		"If it does not, run a DIFFERENT command.", repeats+1)
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
