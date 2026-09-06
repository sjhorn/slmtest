package runner

import (
	"strings"
	"testing"
	"time"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/spec"
)

// fixedReport builds a small, deterministic Report covering all four
// StepStatus values, for the golden-XML assertion below. Durations are
// fixed (not wall-clock) so the test is reproducible.
func fixedReport() *Report {
	return &Report{
		Test:     &spec.Test{Name: "junit-fixture-test"},
		Duration: 4 * time.Second,
		Steps: []StepOutcome{
			{
				Step:     spec.Step{Index: 1, Title: "a passing step"},
				Result:   agent.ResultPass,
				Reason:   "saw the marker",
				Duration: 1 * time.Second,
				Transcript: []TurnLog{
					{Action: agent.Action{Action: agent.ActionRunCommand}, PTYOutput: "hello"},
				},
			},
			{
				Step:     spec.Step{Index: 2, Title: "a failing step"},
				Result:   agent.ResultFail,
				Reason:   "expected output never appeared",
				Duration: 1 * time.Second,
			},
			{
				Step:     spec.Step{Index: 3, Title: "a timed-out step"},
				TimedOut: true,
				Reason:   "step timed out before the model reached a verdict",
				Duration: 1 * time.Second,
			},
			{
				Step:     spec.Step{Index: 4, Title: "an aborted step"},
				Aborted:  true,
				Reason:   "shell process exited unexpectedly",
				Duration: 1 * time.Second,
			},
		},
	}
}

func TestMarshalJUnitGolden(t *testing.T) {
	out, err := MarshalJUnit(fixedReport())
	if err != nil {
		t.Fatalf("MarshalJUnit: %v", err)
	}
	const want = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites tests="4" failures="1" errors="2" time="4.000">
  <testsuite name="junit-fixture-test" tests="4" failures="1" errors="2" time="4.000">
    <testcase classname="junit-fixture-test" name="a passing step" time="1.000">
      <system-out>turn 1: run_command&#xA;  hello&#xA;</system-out>
    </testcase>
    <testcase classname="junit-fixture-test" name="a failing step" time="1.000">
      <failure message="expected output never appeared"></failure>
    </testcase>
    <testcase classname="junit-fixture-test" name="a timed-out step" time="1.000">
      <error message="step timed out: step timed out before the model reached a verdict"></error>
    </testcase>
    <testcase classname="junit-fixture-test" name="an aborted step" time="1.000">
      <error message="shell process exited unexpectedly"></error>
    </testcase>
  </testsuite>
</testsuites>`
	if got := strings.TrimRight(string(out), "\n"); got != want {
		t.Errorf("MarshalJUnit output mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestMarshalJUnitMultipleReportsOneSuiteEach(t *testing.T) {
	r1 := fixedReport()
	r2 := fixedReport()
	r2.Test.Name = "second-test"
	out, err := MarshalJUnit(r1, r2)
	if err != nil {
		t.Fatalf("MarshalJUnit: %v", err)
	}
	s := string(out)
	if strings.Count(s, "<testsuite ") != 2 {
		t.Errorf("expected 2 <testsuite> elements, got:\n%s", s)
	}
	if !strings.Contains(s, `testsuites tests="8" failures="2" errors="4"`) {
		t.Errorf("aggregate totals not doubled:\n%s", s)
	}
}
