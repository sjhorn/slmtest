package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/driver"
	"github.com/sjhorn/slmtest/internal/nulldriver"
	"github.com/sjhorn/slmtest/internal/spec"
)

func TestRunAssertionPassesOnExitZero(t *testing.T) {
	res := runAssertion(context.Background(), "/bin/sh", "exit 0")
	if !res.Passed || res.ExitCode != 0 || res.Err != "" {
		t.Fatalf("want a clean pass, got %+v", res)
	}
}

func TestRunAssertionFailsOnNonZeroExit(t *testing.T) {
	res := runAssertion(context.Background(), "/bin/sh", "echo nope >&2; exit 3")
	if res.Passed {
		t.Fatal("exit 3 must not pass")
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
	if res.Err != "" {
		t.Fatalf("a command that RAN and said no must not set Err: %q", res.Err)
	}
	if !strings.Contains(res.Output, "nope") {
		t.Fatalf("output %q should capture stderr", res.Output)
	}
}

// The override is the whole point: a model claiming pass against a false
// ground truth must not be able to make the step pass.
func TestApplyAssertionOverridesAFalsePass(t *testing.T) {
	out := StepOutcome{Result: agent.ResultPass, Reason: "the file is definitely there"}
	step := spec.Step{Index: 1, Title: "Create it", Verify: "exit 1"}

	applyAssertion(context.Background(), "/bin/sh", step, &out)

	if out.Status() != StatusFail {
		t.Fatalf("status = %s, want fail", out.Status())
	}
	if out.Assertion == nil || out.Assertion.AgreedWithModel {
		t.Fatalf("assertion should be recorded as disagreeing, got %+v", out.Assertion)
	}
	if out.Assertion.ModelReason != "the file is definitely there" {
		t.Fatalf("the model's original reason must be preserved, got %q", out.Assertion.ModelReason)
	}
	if !strings.Contains(out.Reason, "ground-truth check failed") {
		t.Fatalf("reason %q should say why it was overridden", out.Reason)
	}
}

// The asymmetry: a passing check must NOT manufacture a pass the model
// did not give. The harness still never infers pass from an exit code.
func TestApplyAssertionNeverUpgradesAFail(t *testing.T) {
	out := StepOutcome{Result: agent.ResultFail, Reason: "I could not do it"}
	step := spec.Step{Index: 1, Verify: "exit 0"}

	applyAssertion(context.Background(), "/bin/sh", step, &out)

	if out.Status() != StatusFail {
		t.Fatalf("status = %s, want the model's fail to stand", out.Status())
	}
	if out.Reason != "I could not do it" {
		t.Fatalf("reason = %q, want the model's own reason untouched", out.Reason)
	}
	if out.Assertion == nil || out.Assertion.AgreedWithModel {
		t.Fatal("a passing check against a model fail is a disagreement, and should be recorded as one")
	}
}

// A check that cannot RUN is a harness fault. Reporting it as the system
// under test failing would be a lie of exactly the kind this feature exists
// to prevent, just pointed the other way.
func TestApplyAssertionBrokenCheckDoesNotOverride(t *testing.T) {
	out := StepOutcome{Result: agent.ResultPass, Reason: "saw the marker"}
	step := spec.Step{Index: 1, Verify: "exit 0"}

	applyAssertion(context.Background(), "/nonexistent/shell", step, &out)

	if out.Assertion == nil || out.Assertion.Err == "" {
		t.Fatalf("want a harness-level error recorded, got %+v", out.Assertion)
	}
	if out.Status() != StatusPass {
		t.Fatalf("status = %s, want the model's verdict to stand when the check is broken", out.Status())
	}
}

func TestStepWithNoVerifyGetsNoAssertion(t *testing.T) {
	out := StepOutcome{Result: agent.ResultPass, Reason: "fine"}
	applyAssertion(context.Background(), "/bin/sh", spec.Step{Index: 1}, &out)
	if out.Assertion != nil {
		t.Fatalf("a step without Verify must report no assertion, got %+v", out.Assertion)
	}
}

// The check must be invisible to the model. A ground-truth check the model
// can read is a ground-truth check it can aim at — the same reasoning that
// keeps the trap suite's expected verdicts in a sidecar file rather than in
// the spec (docs/trap-suite.md).
func TestVerifyIsNeverShownToTheModel(t *testing.T) {
	nd := nulldriver.NewScripted(driver.Observation{Text: "marker-abc appeared on screen"})
	name := "null-verify-invisible"
	driver.Register(name, func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return nd, nil
	})

	const secret = "cat /etc/ground-truth-secret-marker"
	st := step(1, "one")
	st.Verify = secret

	f := newFakeSLM(t, replyEcho, replyPass)
	report, err := Run(context.Background(), testSpec(t, st), f.client(), Options{DriverName: name})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, s := range report.Steps {
		for i, turn := range s.Transcript {
			if strings.Contains(turn.UserPrompt, secret) {
				t.Fatalf("turn %d prompt leaked the Verify command:\n%s", i, turn.UserPrompt)
			}
		}
	}
}

// -exec-prefix puts the session on another machine; a local check would
// grade the wrong filesystem and quietly pass. Refusing is the only safe
// answer until the check can be run through the prefix too.
func TestVerifyWithExecPrefixIsRefused(t *testing.T) {
	st := step(1, "one")
	st.Verify = "test -f /tmp/whatever"

	f := newFakeSLM(t)
	_, err := Run(context.Background(), testSpec(t, st), f.client(),
		Options{ExecPrefix: []string{"ssh", "testbox"}, SessionIsRemote: true})
	if err == nil {
		t.Fatal("want an error when Verify is combined with -exec-prefix")
	}
	if !strings.Contains(err.Error(), "-exec-prefix") {
		t.Fatalf("error %q should explain the conflict", err)
	}
}
