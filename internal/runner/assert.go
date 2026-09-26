package runner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/driver"
	"github.com/sjhorn/slmtest/internal/judge"
	"github.com/sjhorn/slmtest/internal/spec"
)

// Ground-truth assertions.
//
// A step's Expect criterion is graded by the model reading the screen, and
// that is the one thing the model can stage. Asked for a hostname it could
// not produce, a real model ran `echo "server-does-not-exist-42"` rather
// than `hostname`; the screen then genuinely contained the expected text,
// and an otherwise-honest judge passed the step (docs/model-roles.md).
//
// A step's Verify command closes that hole by running OUTSIDE the driven
// session, in a fresh process the model never touches — so it cannot alias
// the binary, reorder PATH, or echo a fake answer. The command is never
// added to the system prompt or any user message either: a check the model
// can read is a check it can aim at.

// assertionTimeout bounds one Verify command. Ground-truth checks are
// meant to be cheap (`test -f`, `grep -q`) — a slow one is a broken one,
// and it must not be able to outlive the step it is grading.
const assertionTimeout = 30 * time.Second

// assertionOutputLimit caps captured output. The report keeps it for
// diagnosis, not for replay, and a runaway command should not be able to
// bloat a trace bundle.
const assertionOutputLimit = 4096

// AssertionResult records one Verify command's outcome. It is nil on a
// step that has no Verify, so a spec written before this existed reports
// exactly as it always did.
type AssertionResult struct {
	// Kind is "shell" for a Verify: command run as an external process,
	// "driver" for a VerifyDriver: expression the driver evaluates against
	// its own session, or "judge" for a second-opinion reading of the
	// screen by a decision model. The three have different threat models
	// — see spec.Step.VerifyDriver and internal/judge — so a report must
	// not blur them. Only "shell" and "driver" carry authority over a
	// step's result; "judge" never does.
	Kind     string
	Command  string
	Passed   bool
	ExitCode int
	Output   string
	// Probability is set only for Kind "judge": the decision model's
	// calibrated probability that the step's Expect was satisfied. A
	// pointer, not a float, so that a genuine 0.000 (a confident "no",
	// which these models produce routinely) is reported as 0.000 rather
	// than elided by omitempty the way a plain zero float would be.
	Probability *float64
	// Err is set when the check could not be run at all (shell missing,
	// timeout) as opposed to running and reporting failure. The two are
	// different: the second is evidence about the system under test, the
	// first is evidence about the harness, and conflating them would let
	// a broken assertion masquerade as a failing one.
	Err string
	// AgreedWithModel records whether the model's verdict matched the
	// ground truth. Disagreement is the useful signal: it is a false-pass
	// (or false-fail) detector that runs on ordinary specs, not only on
	// the purpose-built trap suite.
	AgreedWithModel bool
	// ModelReason preserves the model's own stated reason when a failing
	// assertion overrides its verdict, so the override never destroys the
	// audit trail it is overriding.
	ModelReason string
	Duration    time.Duration
}

// runAssertion executes one Verify command with the given shell and
// reports what happened. A non-zero exit is a normal outcome (the ground
// truth does not hold), not an error.
func runAssertion(ctx context.Context, shell, command string) *AssertionResult {
	if shell == "" {
		shell = "/bin/sh"
	}
	res := &AssertionResult{Kind: "shell", Command: command}
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, assertionTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-c", command)
	out, err := cmd.CombinedOutput()
	res.Duration = time.Since(start)
	res.Output = clipOutput(string(out), assertionOutputLimit)

	switch {
	case err == nil:
		res.Passed = true
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Err = fmt.Sprintf("ground-truth check timed out after %s", assertionTimeout)
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode() // ran and said no: a real verdict
		} else {
			res.Err = err.Error() // could not run at all
		}
	}
	return res
}

// applyAssertion runs a step's Verify (if it has one) and folds the result
// into the outcome.
//
// The asymmetry is the whole design: a FAILING check forces the step to
// fail, while a PASSING one leaves the model's verdict alone. The harness
// still never infers "pass" from an exit code — it only refuses to accept
// a pass that ground truth contradicts, which is the single direction
// where models have been observed to be untrustworthy.
//
// A check that could not be RUN (Err set) never overrides anything: a
// broken assertion is a harness problem and must not be reported as the
// system under test failing.
func applyAssertion(ctx context.Context, shell string, drv driver.Driver, step spec.Step, outcome *StepOutcome) {
	var results []*AssertionResult
	if step.Verify != "" {
		results = append(results, runAssertion(ctx, shell, step.Verify))
	}
	if step.VerifyDriver != "" {
		results = append(results, runDriverAssertion(ctx, drv, step.VerifyDriver))
	}
	if len(results) == 0 {
		return
	}

	modelPassed := outcome.Status() == StatusPass
	var failed *AssertionResult
	for _, res := range results {
		res.AgreedWithModel = res.Passed == modelPassed
		if res.Err == "" && !res.Passed && failed == nil {
			failed = res
		}
		outcome.Assertions = append(outcome.Assertions, *res)
	}
	if failed == nil {
		return
	}

	// Report the first check that actually said no. Its own record keeps
	// the model's original reason, so overriding never destroys the audit
	// trail it is overriding.
	for i := range outcome.Assertions {
		if outcome.Assertions[i].Command == failed.Command {
			outcome.Assertions[i].ModelReason = outcome.Reason
		}
	}
	outcome.Result = agent.ResultFail
	outcome.Reason = fmt.Sprintf("ground-truth check failed: %s (%s)%s%s",
		failed.Command, failedDetail(failed),
		firstLineOf(failed.Output),
		modelClaimSuffix(outcome.Reason))
}

// runDriverAssertion asks the driver to evaluate its own ground-truth
// expression. A driver that does not implement driver.Asserter reports the
// check as unrunnable rather than skipping it: a check silently treated as
// satisfied is precisely the failure this feature exists to remove.
func runDriverAssertion(ctx context.Context, drv driver.Driver, expr string) *AssertionResult {
	res := &AssertionResult{Kind: "driver", Command: expr}
	start := time.Now()
	defer func() { res.Duration = time.Since(start) }()

	asserter, ok := drv.(driver.Asserter)
	if !ok {
		res.Err = fmt.Sprintf("driver %q cannot evaluate a VerifyDriver: check", drv.Name())
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, assertionTimeout)
	defer cancel()

	okResult, detail, err := asserter.Assert(ctx, expr)
	res.Output = clipOutput(detail, assertionOutputLimit)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	res.Passed = okResult
	return res
}

func failedDetail(res *AssertionResult) string {
	if res.Kind == "driver" || res.Kind == "judge" {
		return "not satisfied"
	}
	return fmt.Sprintf("exit %d", res.ExitCode)
}

// clipOutput is a local cap rather than runner.truncateOutput, which exists
// to fit a model's context window and carries its own elision wording; this
// output is only ever read by a human or a report.
func clipOutput(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n... (truncated)"
}

func firstLineOf(out string) string {
	line := strings.TrimSpace(out)
	if line == "" {
		return ""
	}
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return " — " + line
}

func modelClaimSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return fmt.Sprintf(" [model had said: %s]", reason)
}

// assertionShell picks the shell a Verify command runs under. It reuses
// the spec's own shell so an author writes one dialect, not two — but
// note the command still runs in a FRESH process, never inside the driven
// session, which is the property that makes it unforgeable.
func assertionShell(t *spec.Test, opts Options) string {
	if opts.Shell != "" {
		return opts.Shell
	}
	if t != nil && t.Shell != "" {
		return t.Shell
	}
	return "/bin/sh"
}

// Judge is the optional second-opinion reader: a decision model that
// grades a step's Expect against the screen. Defined as an interface here
// rather than taking *judge.Client so tests can substitute a fake, the
// same way agent's fakeSLM does for the model side.
type Judge interface {
	Grade(ctx context.Context, expect, screen string) (judge.Verdict, error)
}

// applyJudge records a judge's reading of the step as an AssertionResult
// and nothing more.
//
// It is a DELIBERATELY separate function from applyAssertion, not a third
// branch inside it, and the separation is the safety property: this
// function never assigns outcome.Result or outcome.Reason, so a judge
// cannot change a verdict even by mistake. That is not a stylistic
// preference. Measured across 48 hand-labelled screens, every backend
// tested produced at least one confident false fail (Open-Jev-9B and Kev
// both rated a satisfied criterion at 0.00-0.05), and the local ones
// returned almost entirely saturated probabilities, so no threshold
// separated their errors from their correct answers. A reader that wrong,
// that confidently, must not be able to fail a step.
//
// What it IS for: AgreedWithModel. A disagreement between the acting model
// and an independent reader is a false-pass detector that runs on ordinary
// specs — the same signal Verify: provides, on the pure-screen steps where
// Verify: cannot reach. It is a flag for a human, not a gate.
func applyJudge(ctx context.Context, j Judge, step spec.Step, screen string, modelPassed bool, outcome *StepOutcome) {
	if j == nil {
		return
	}
	res := &AssertionResult{Kind: "judge", Command: step.Expect}
	start := time.Now()

	verdict, err := j.Grade(ctx, step.Expect, screen)
	res.Duration = time.Since(start)
	if err != nil {
		// No opinion. Reported as a harness fault, never as the system
		// under test failing — the same distinction runAssertion draws
		// between "ran and said no" and "could not run".
		res.Err = err.Error()
		outcome.Assertions = append(outcome.Assertions, *res)
		return
	}
	res.Passed = verdict.Passed
	res.Probability = &verdict.Probability
	// Compared against the MODEL's own verdict, captured before any
	// ground-truth override — not against outcome.Status(), which a failing
	// Verify: may already have corrected. Running a fabricating fine-tune
	// against examples/trap-terminal-test.md showed why: on the three steps
	// where it claimed a pass it had not earned, ground truth flipped the
	// status to fail first, so comparing against the status recorded the
	// judge as AGREEING — masking the one signal the field exists to
	// surface. Guarded by TestJudgeAgreementIsAgainstTheModelNotGroundTruth.
	res.AgreedWithModel = verdict.Passed == modelPassed
	res.Output = fmt.Sprintf("p=%.3f", verdict.Probability)
	if verdict.Model != "" {
		res.Output += " (" + verdict.Model + ")"
	}
	if !res.AgreedWithModel {
		// Preserve what the model claimed, so a disagreement line in the
		// report carries both readings rather than only the judge's.
		res.ModelReason = outcome.Reason
	}
	outcome.Assertions = append(outcome.Assertions, *res)
}
