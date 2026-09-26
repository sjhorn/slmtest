package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/judge"
	"github.com/sjhorn/slmtest/internal/spec"
)

// fakeJudge scripts one verdict, the way fakeSLM scripts model replies.
type fakeJudge struct {
	verdict judge.Verdict
	err     error
	// seen records what the judge was actually asked, so a test can prove
	// the screen and criterion reached it intact.
	seenExpect string
	seenScreen string
	calls      int
}

func (f *fakeJudge) Grade(_ context.Context, expect, screen string) (judge.Verdict, error) {
	f.calls++
	f.seenExpect, f.seenScreen = expect, screen
	return f.verdict, f.err
}

func passOutcome() *StepOutcome {
	return &StepOutcome{Result: agent.ResultPass, Reason: "saw the expected output"}
}

func failOutcome() *StepOutcome {
	return &StepOutcome{Result: agent.ResultFail, Reason: "never saw it"}
}

// TestJudgeNeverOverridesAPass is the load-bearing guarantee of the whole
// feature. Every backend measured produced confident false fails, so a
// judge that could fail a step would break passing runs. Cf.
// TestApplyAssertionNeverUpgradesAFail, which guards the other direction
// for real ground truth.
func TestJudgeNeverOverridesAPass(t *testing.T) {
	out := passOutcome()
	j := &fakeJudge{verdict: judge.Verdict{Probability: 0.00, Passed: false}}

	applyJudge(context.Background(), j, spec.Step{Expect: "x"}, "screen", true, out)

	if out.Result != agent.ResultPass {
		t.Fatalf("judge changed the verdict to %v; it must never have authority", out.Result)
	}
	if out.Reason != "saw the expected output" {
		t.Fatalf("judge rewrote the reason to %q", out.Reason)
	}
	if len(out.Assertions) != 1 || out.Assertions[0].Kind != "judge" {
		t.Fatalf("expected one judge assertion, got %+v", out.Assertions)
	}
	if out.Assertions[0].AgreedWithModel {
		t.Fatal("judge said fail against a model pass; that is a disagreement")
	}
}

// A judge must not manufacture a pass either — the direction the harness
// has never delegated to anything, model or otherwise.
func TestJudgeNeverOverridesAFail(t *testing.T) {
	out := failOutcome()
	j := &fakeJudge{verdict: judge.Verdict{Probability: 0.99, Passed: true}}

	applyJudge(context.Background(), j, spec.Step{Expect: "x"}, "screen", false, out)

	if out.Result != agent.ResultFail {
		t.Fatalf("judge upgraded a fail to %v", out.Result)
	}
	if out.Assertions[0].AgreedWithModel {
		t.Fatal("judge said pass against a model fail; that is a disagreement")
	}
}

func TestJudgeRecordsAgreement(t *testing.T) {
	out := passOutcome()
	j := &fakeJudge{verdict: judge.Verdict{Probability: 0.97, Passed: true, Model: "open-jev"}}

	applyJudge(context.Background(), j, spec.Step{Expect: "output contains hi"}, "screen text", true, out)

	a := out.Assertions[0]
	if !a.AgreedWithModel {
		t.Fatal("judge and model both said pass; expected agreement")
	}
	if a.Probability == nil || *a.Probability != 0.97 {
		t.Fatalf("probability = %v, want 0.97", a.Probability)
	}
	if !strings.Contains(a.Output, "p=0.970") || !strings.Contains(a.Output, "open-jev") {
		t.Fatalf("output %q should carry the probability and answering model", a.Output)
	}
	if a.ModelReason != "" {
		t.Fatalf("no disagreement, so the model's reason need not be preserved: %q", a.ModelReason)
	}
	if j.seenExpect != "output contains hi" || j.seenScreen != "screen text" {
		t.Fatalf("judge saw expect=%q screen=%q", j.seenExpect, j.seenScreen)
	}
}

// A judge that cannot answer is a harness fault, never evidence about the
// system under test — the same distinction runAssertion draws between
// "ran and said no" and "could not run".
func TestJudgeErrorIsRecordedNotFatal(t *testing.T) {
	out := passOutcome()
	j := &fakeJudge{err: errors.New("connection refused")}

	applyJudge(context.Background(), j, spec.Step{Expect: "x"}, "screen", true, out)

	if out.Result != agent.ResultPass {
		t.Fatalf("an unreachable judge changed the verdict to %v", out.Result)
	}
	a := out.Assertions[0]
	if a.Err == "" || !strings.Contains(a.Err, "connection refused") {
		t.Fatalf("expected the error recorded, got %+v", a)
	}
	if a.AgreedWithModel {
		t.Fatal("a judge that never answered must not be recorded as agreeing")
	}
}

// A nil judge is the default, and must cost a run nothing at all.
func TestNilJudgeIsANoOp(t *testing.T) {
	out := passOutcome()
	applyJudge(context.Background(), nil, spec.Step{Expect: "x"}, "screen", true, out)
	if len(out.Assertions) != 0 {
		t.Fatalf("nil judge produced assertions: %+v", out.Assertions)
	}
}

// The judge grades the screen the step ENDED on, not the first one.
func TestLastNonEmptyScreenPicksTheFinalSnapshot(t *testing.T) {
	out := StepOutcome{Transcript: []TurnLog{
		{Screen: "first"}, {Screen: "second"}, {Screen: ""},
	}}
	if got := lastNonEmptyScreen(out); got != "second" {
		t.Fatalf("lastNonEmptyScreen = %q, want %q", got, "second")
	}
	if got := lastNonEmptyScreen(StepOutcome{}); got != "" {
		t.Fatalf("no transcript should yield an empty screen, got %q", got)
	}
}

// TestJudgeVerdictNeverReachesStepStatus guards a regression found by
// running the feature end to end: applyJudge correctly left Result alone,
// but StepOutcome.Status() scanned every assertion for a failure and so
// reported StatusFail anyway — the human report printed "[FAIL] step 1"
// beside "RESULT: PASS". A judge must not reach status by any route.
func TestJudgeVerdictNeverReachesStepStatus(t *testing.T) {
	out := passOutcome()
	applyJudge(context.Background(), &fakeJudge{verdict: judge.Verdict{Probability: 0.02}},
		spec.Step{Expect: "x"}, "screen", true, out)

	if got := out.Status(); got != StatusPass {
		t.Fatalf("Status() = %q after a judge said fail; want %q", got, StatusPass)
	}
}

// The same scan must still honour a REAL ground-truth failure alongside a
// judge assertion — the exclusion is by kind, not "any assertion present".
func TestGroundTruthStillFailsStatusAlongsideAJudge(t *testing.T) {
	out := passOutcome()
	out.Assertions = append(out.Assertions, AssertionResult{Kind: "shell", Command: "test -f /nope", ExitCode: 1})
	applyJudge(context.Background(), &fakeJudge{verdict: judge.Verdict{Probability: 0.99, Passed: true}},
		spec.Step{Expect: "x"}, "screen", false, out)

	if got := out.Status(); got != StatusFail {
		t.Fatalf("Status() = %q; a failed shell check must still fail the step", got)
	}
}

// TestJudgeAgreementIsAgainstTheModelNotGroundTruth guards the flaw found
// by running the fabricating LFM2.5-350M fine-tune against
// examples/trap-terminal-test.md: on every step where it claimed a pass it
// had not earned, a failing Verify: corrected the status to fail FIRST, so
// comparing the judge against outcome.Status() recorded it as agreeing —
// hiding the disagreement that is the entire point of the field.
func TestJudgeAgreementIsAgainstTheModelNotGroundTruth(t *testing.T) {
	// The shape of a caught false pass: model claimed pass, ground truth
	// said no, judge also read the screen as fail.
	out := failOutcome() // status already corrected by ground truth
	out.Assertions = append(out.Assertions, AssertionResult{Kind: "shell", Command: "test -f /nope", ExitCode: 1})

	applyJudge(context.Background(), &fakeJudge{verdict: judge.Verdict{Probability: 0.001}},
		spec.Step{Expect: "x"}, "screen", true /* the model had claimed pass */, out)

	j := out.Assertions[len(out.Assertions)-1]
	if j.AgreedWithModel {
		t.Fatal("judge said fail and the model claimed pass: that is a DISAGREEMENT, " +
			"even though ground truth had already corrected the status")
	}
}

// A confident 0.000 must survive into the report rather than being elided
// as a zero value — these models produce it routinely.
func TestJudgeZeroProbabilityIsReportedNotElided(t *testing.T) {
	out := failOutcome()
	applyJudge(context.Background(), &fakeJudge{verdict: judge.Verdict{Probability: 0}},
		spec.Step{Expect: "x"}, "screen", false, out)

	a := out.Assertions[0]
	if a.Probability == nil {
		t.Fatal("probability 0.000 was dropped; a confident no must be reported")
	}
	if *a.Probability != 0 {
		t.Fatalf("probability = %v, want 0", *a.Probability)
	}
}
