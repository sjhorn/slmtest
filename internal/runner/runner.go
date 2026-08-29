// Package runner drives one Test end-to-end: for each step, it repeatedly
// (a) shows the model the step goal + recent PTY output, (b) gets back one
// Action, (c) executes it against the PTY, until the model calls
// finish_step or the step's turn/time budget runs out.
package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/example/slmtest/internal/agent"
	"github.com/example/slmtest/internal/ptydriver"
	"github.com/example/slmtest/internal/spec"
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

const systemPrompt = `You are operating a real Linux shell through a pseudo-terminal to complete one step of a test script. You are not chatting with a user — every reply you send is parsed as a single JSON object and used to control the terminal directly.

Reply with EXACTLY one JSON object matching this schema, and nothing else (no prose, no markdown fence):

{
  "thought": "<optional, one short sentence>",
  "action": "run_command" | "send_keys" | "wait" | "finish_step" | "abort_test",
  "command": "<shell text, required for run_command/send_keys>",
  "press_enter": <bool, optional>,
  "wait_ms": <int, optional, default 1500>,
  "step_result": "pass" | "fail",   // required for finish_step
  "reason": "<required for finish_step and abort_test>"
}

Rules:
- run_command: types the command, presses Enter, waits wait_ms, then you'll be shown new terminal output.
- send_keys: like run_command but does NOT press Enter by default — use for partial input, control characters (e.g. "\u0003" for Ctrl-C), or interactive prompts.
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
	Verbose       func(format string, args ...any)
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
	drv, err := ptydriver.Start(shell, nil)
	if err != nil {
		return nil, fmt.Errorf("starting pty: %w", err)
	}
	defer drv.Close()

	report := &Report{Test: t, Passed: true}

	for _, step := range t.Steps {
		log("=== step %d: %s ===", step.Index, step.Title)
		outcome := runStep(ctx, drv, client, t, step, opts)
		report.Steps = append(report.Steps, outcome)

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
			// files created). If you want to run every step regardless,
			// continue instead of break — see CLAUDE.md "continue-on-fail".
			break
		}
		log("step %d passed: %s", step.Index, outcome.Reason)
	}

	return report, nil
}

func runStep(ctx context.Context, drv *ptydriver.Driver, client *agent.Client, t *spec.Test, step spec.Step, opts Options) StepOutcome {
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

	firstPrompt := fmt.Sprintf(
		"STEP %d: %s\nGoal: %s\nHint: %s\nExpect: %s\n\n(No terminal output yet — this is the start of the step.)",
		step.Index, step.Title, step.Goal, orNone(step.Hint), step.Expect)
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
			pressEnter := action.Action == agent.ActionRunCommand
			if action.PressEnter != nil {
				pressEnter = *action.PressEnter
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
			msgs = append(msgs, agent.Message{Role: "assistant", Content: reply})
			nextUser = "Terminal output:\n" + orNone(out)

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
			msgs = append(msgs, agent.Message{Role: "assistant", Content: reply})
			nextUser = "Terminal output:\n" + orNone(out)
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

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
