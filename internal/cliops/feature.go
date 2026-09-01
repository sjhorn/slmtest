package cliops

import (
	"context"
	"fmt"
	"os"

	"github.com/sjhorn/slmtest/internal/runner"
	"github.com/sjhorn/slmtest/internal/spec"
)

// FeatureResult is what RunFeature returns: the parsed Feature alongside
// one Report per expanded scenario, in the same order Feature.Expand
// produced them.
type FeatureResult struct {
	Feature *spec.Feature
	// Scenarios holds one entry per expanded Test — its own Test (name,
	// steps) alongside the Report running it produced.
	Scenarios []ScenarioResult
	// Passed is true only if every scenario passed — mirrors Report.Passed's
	// own all-or-nothing meaning, one level up.
	Passed bool
}

// ScenarioResult is one expanded scenario's Test and the Report from
// running it.
type ScenarioResult struct {
	Test   *spec.Test
	Report *runner.Report
}

// IsFeatureSpec reports whether the spec file at path uses this
// project's optional Feature/Background/Scenario markdown layer — the
// signal cmd/slmtest's `run` command uses to choose between Run (one
// Test, the existing -json report shape, unchanged) and RunFeature (a
// Feature's own report shape) before running anything.
func IsFeatureSpec(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	looks, err := spec.LooksLikeFeature(string(raw))
	if err != nil {
		return false, fmt.Errorf("parsing %s: %w", path, err)
	}
	return looks, nil
}

// RunFeature runs every Scenario in a Feature-style markdown file to
// completion — unlike Run's own ContinueOnFail (which decides whether to
// keep going within ONE Test after a step fails), scenarios are always
// run to completion regardless of each other's outcome, the same way
// Cucumber runs every Scenario in a Feature file independently. A file
// with no "## Background"/"## Scenario:" heading still works here — it
// expands to exactly one scenario, identical to what Run itself would
// have executed (see spec.ParseFeature's fallback).
//
// p.SpecPath is read and parsed as a Feature; every other RunParams field
// carries through to each expanded scenario's execution exactly as Run
// applies them to a single Test. Tags, if non-empty, keeps only the
// Scenarios carrying every listed tag (see spec.Feature.Filter) — an
// empty Tags runs every Scenario, unchanged.
func RunFeature(ctx context.Context, p RunParams, tags []string) (*FeatureResult, error) {
	raw, err := os.ReadFile(p.SpecPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p.SpecPath, err)
	}
	f, err := spec.ParseFeature(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p.SpecPath, err)
	}
	f = f.Filter(tags)
	if len(f.Scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios in %s match tags %v", p.SpecPath, tags)
	}
	tests, err := f.Expand()
	if err != nil {
		return nil, fmt.Errorf("expanding %s: %w", p.SpecPath, err)
	}

	result := &FeatureResult{Feature: f, Passed: true}
	for _, t := range tests {
		report, err := runLoadedTest(ctx, t, p)
		if err != nil {
			return nil, fmt.Errorf("scenario %q: %w", t.Name, err)
		}
		result.Scenarios = append(result.Scenarios, ScenarioResult{Test: t, Report: report})
		if !report.Passed {
			result.Passed = false
		}
	}
	return result, nil
}
