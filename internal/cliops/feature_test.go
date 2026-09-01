package cliops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const featureSpecDoc = `---
name: two-scenario-feature
description: A Feature-style spec with a Background and two Scenarios
---

## Background
### Step 1: Given the harness is ready
Goal: nothing to set up.
Expect: nothing in particular.

## Scenario: First scenario
### Step 1: When the first scenario runs
Goal: the first scenario's own step runs.
Expect: it finishes.

## Scenario: Second scenario
### Step 1: When the second scenario runs
Goal: the second scenario's own step runs.
Expect: it finishes.
`

func writeFeatureSpec(t *testing.T, doc string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "feature.md")
	if err := os.WriteFile(path, []byte(doc), 0644); err != nil {
		t.Fatalf("writing feature spec: %v", err)
	}
	return path
}

// TestRunFeatureRunsEveryScenario proves RunFeature expands a Background
// + two Scenarios into two independent Test runs and aggregates both
// Reports — each scenario needs its own Background step (1 turn) plus its
// own scenario step (1 turn), both via finish_step against the "null"
// driver, matching TestRunMatchesDirectRunnerCall's pattern of avoiding a
// real PTY/model.
func TestRunFeatureRunsEveryScenario(t *testing.T) {
	path := writeFeatureSpec(t, featureSpecDoc)
	endpoint := scriptedSLM(t,
		`{"action":"finish_step","step_result":"pass","reason":"background done"}`,
		`{"action":"finish_step","step_result":"pass","reason":"scenario 1 done"}`,
		`{"action":"finish_step","step_result":"pass","reason":"background done"}`,
		`{"action":"finish_step","step_result":"pass","reason":"scenario 2 done"}`,
	)

	result, err := RunFeature(context.Background(), RunParams{
		SpecPath:   path,
		Endpoint:   endpoint,
		DriverName: "null",
	}, nil)
	if err != nil {
		t.Fatalf("RunFeature: %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; scenarios: %+v", result.Scenarios)
	}
	if len(result.Scenarios) != 2 {
		t.Fatalf("Scenarios = %d, want 2", len(result.Scenarios))
	}
	if result.Scenarios[0].Test.Name != "two-scenario-feature — First scenario" {
		t.Errorf("Scenarios[0].Test.Name = %q", result.Scenarios[0].Test.Name)
	}
	if result.Scenarios[1].Test.Name != "two-scenario-feature — Second scenario" {
		t.Errorf("Scenarios[1].Test.Name = %q", result.Scenarios[1].Test.Name)
	}
	for i, sc := range result.Scenarios {
		if !sc.Report.Passed {
			t.Errorf("scenario %d Report.Passed = false, want true", i)
		}
		if len(sc.Test.Steps) != 2 { // Background + the scenario's own step
			t.Errorf("scenario %d Steps = %d, want 2", i, len(sc.Test.Steps))
		}
	}
}

// TestRunFeatureRunsEveryScenarioRegardlessOfEarlierFailures confirms
// scenarios are always run to completion independently of each other —
// unlike ContinueOnFail (which governs stopping within ONE Test), a
// Feature run never skips a later scenario because an earlier one failed,
// the same way Cucumber runs every Scenario in a Feature file.
func TestRunFeatureRunsEveryScenarioRegardlessOfEarlierFailures(t *testing.T) {
	path := writeFeatureSpec(t, featureSpecDoc)
	endpoint := scriptedSLM(t,
		`{"action":"finish_step","step_result":"pass","reason":"background done"}`,
		`{"action":"finish_step","step_result":"fail","reason":"scenario 1 fails"}`,
		`{"action":"finish_step","step_result":"pass","reason":"background done"}`,
		`{"action":"finish_step","step_result":"pass","reason":"scenario 2 done"}`,
	)

	result, err := RunFeature(context.Background(), RunParams{
		SpecPath:   path,
		Endpoint:   endpoint,
		DriverName: "null",
	}, nil)
	if err != nil {
		t.Fatalf("RunFeature: %v", err)
	}
	if result.Passed {
		t.Fatalf("Passed = true, want false (scenario 1 failed); scenarios: %+v", result.Scenarios)
	}
	if len(result.Scenarios) != 2 {
		t.Fatalf("Scenarios = %d, want 2 (scenario 2 must still run)", len(result.Scenarios))
	}
	if result.Scenarios[0].Report.Passed {
		t.Errorf("scenario 0 Report.Passed = true, want false")
	}
	if !result.Scenarios[1].Report.Passed {
		t.Errorf("scenario 1 Report.Passed = false, want true — it must run regardless of scenario 0's outcome")
	}
}

const taggedFeatureSpecDoc = `---
name: tagged-feature
---

@smoke
## Scenario: Tagged scenario
### Step 1: When it runs
Goal: it runs.
Expect: it finishes.

## Scenario: Untagged scenario
### Step 1: When it runs
Goal: it runs.
Expect: it finishes.
`

// TestRunFeatureFiltersByTag proves the CLI-facing tag-selection path
// (RunFeature's tags param, backed by spec.Feature.Filter) actually skips
// dispatching non-matching scenarios entirely, not just labeling them.
func TestRunFeatureFiltersByTag(t *testing.T) {
	path := writeFeatureSpec(t, taggedFeatureSpecDoc)
	endpoint := scriptedSLM(t,
		`{"action":"finish_step","step_result":"pass","reason":"done"}`,
	)

	result, err := RunFeature(context.Background(), RunParams{
		SpecPath:   path,
		Endpoint:   endpoint,
		DriverName: "null",
	}, []string{"@smoke"})
	if err != nil {
		t.Fatalf("RunFeature: %v", err)
	}
	if len(result.Scenarios) != 1 {
		t.Fatalf("Scenarios = %d, want 1 (only the @smoke-tagged one)", len(result.Scenarios))
	}
	if result.Scenarios[0].Test.Name != "tagged-feature — Tagged scenario" {
		t.Errorf("Test.Name = %q, want the tagged scenario", result.Scenarios[0].Test.Name)
	}
}

func TestRunFeatureTagWithNoMatchesIsAnError(t *testing.T) {
	path := writeFeatureSpec(t, taggedFeatureSpecDoc)
	if _, err := RunFeature(context.Background(), RunParams{
		SpecPath:   path,
		DriverName: "null",
	}, []string{"@nonexistent"}); err == nil {
		t.Fatal("expected an error when no scenario matches the given tag")
	}
}

// TestRunFeatureFallsBackForLegacySpecFiles confirms RunFeature works
// unmodified on an ordinary, non-Feature-style spec file — it expands to
// exactly one scenario, the same shape Run itself would have executed.
func TestRunFeatureFallsBackForLegacySpecFiles(t *testing.T) {
	endpoint := scriptedSLM(t,
		`{"action":"finish_step","step_result":"pass","reason":"done"}`,
	)
	result, err := RunFeature(context.Background(), RunParams{
		SpecPath:   echoTestSpecPath,
		Endpoint:   endpoint,
		DriverName: "null",
	}, nil)
	if err != nil {
		t.Fatalf("RunFeature: %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; scenarios: %+v", result.Scenarios)
	}
	if len(result.Scenarios) != 1 {
		t.Fatalf("Scenarios = %d, want 1 for a legacy spec file", len(result.Scenarios))
	}
	if result.Scenarios[0].Test.Name != "echo-smoke-test" {
		t.Errorf("Test.Name = %q, want echo-smoke-test", result.Scenarios[0].Test.Name)
	}
}
