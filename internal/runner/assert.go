package runner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sjhorn/slmtest/internal/agent"
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
	Command  string
	Passed   bool
	ExitCode int
	Output   string
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
	res := &AssertionResult{Command: command}
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
func applyAssertion(ctx context.Context, shell string, step spec.Step, outcome *StepOutcome) {
	if step.Verify == "" {
		return
	}
	res := runAssertion(ctx, shell, step.Verify)
	res.AgreedWithModel = res.Passed == (outcome.Status() == StatusPass)
	outcome.Assertion = res

	if res.Err != "" || res.Passed {
		return
	}
	res.ModelReason = outcome.Reason
	outcome.Result = agent.ResultFail
	outcome.Reason = fmt.Sprintf("ground-truth check failed: %s (exit %d)%s%s",
		step.Verify, res.ExitCode,
		firstLineOf(res.Output),
		modelClaimSuffix(res.ModelReason))
}

// clipOutput is a local cap rather than runner.truncateOutput, which
// exists to fit a model's context window and carries its own elision
// wording; this output is only ever read by a human or a report.
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
