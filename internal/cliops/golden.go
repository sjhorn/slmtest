package cliops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sjhorn/slmtest/internal/runner"
)

// This file implements golden-file screen regression checking (Phase D
// of docs/roadmap-reporting-and-agents.md), built directly on Phase A's
// TurnLog.Screen — no new capture mechanism. It deliberately does NOT
// affect a step's pass/fail verdict or the process exit code: it is a
// complement to the model's own judgement (did the screen at
// finish_step time match what a prior run produced?), not a replacement
// for it. No diff engine dependency, matching this project's "no exotic
// dependencies" stance — a mismatch reports the first differing line,
// not a full diff.

// GoldenResult is one step's golden-file comparison outcome.
type GoldenResult struct {
	StepIndex int
	StepTitle string
	// Status is one of "match", "mismatch", "missing", or "updated".
	Status string
	// DiffPreview is set only for "mismatch": the first differing line's
	// index and old/new content.
	DiffPreview string
}

// compareGolden takes, for each report's each step, the LAST non-empty
// Screen captured during that step (the state at finish_step time) as
// the step's golden candidate, and compares it against
// <dir>/<test-slug>/step-<N>.golden.txt. With update true, the baseline
// is written/overwritten instead of compared.
func compareGolden(dir string, reports []*runner.Report, update bool) ([]GoldenResult, error) {
	var results []GoldenResult
	for _, r := range reports {
		name := "test"
		if r.Test != nil && r.Test.Name != "" {
			name = r.Test.Name
		}
		slug := slugify(name)
		testDir := filepath.Join(dir, slug)

		for _, s := range r.Steps {
			candidate := lastNonEmptyScreen(s)
			if candidate == "" {
				// No screen snapshot captured for this step at all (e.g. a
				// driver with no screen concept, or the step never
				// dispatched anything) — nothing to compare.
				continue
			}
			baselinePath := filepath.Join(testDir, fmt.Sprintf("step-%d.golden.txt", s.Step.Index))
			result := GoldenResult{StepIndex: s.Step.Index, StepTitle: s.Step.Title}

			if update {
				if err := os.MkdirAll(testDir, 0755); err != nil {
					return nil, fmt.Errorf("creating golden dir %s: %w", testDir, err)
				}
				if err := os.WriteFile(baselinePath, []byte(candidate), 0644); err != nil {
					return nil, fmt.Errorf("writing golden baseline %s: %w", baselinePath, err)
				}
				result.Status = "updated"
				results = append(results, result)
				continue
			}

			baseline, err := os.ReadFile(baselinePath)
			if os.IsNotExist(err) {
				result.Status = "missing"
				results = append(results, result)
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("reading golden baseline %s: %w", baselinePath, err)
			}
			if string(baseline) == candidate {
				result.Status = "match"
			} else {
				result.Status = "mismatch"
				result.DiffPreview = firstDiffLine(string(baseline), candidate)
			}
			results = append(results, result)
		}
	}
	return results, nil
}

// lastNonEmptyScreen returns the last non-empty TurnLog.Screen captured
// during a step — the screen state at (or nearest to) finish_step time.
func lastNonEmptyScreen(s runner.StepOutcome) string {
	for i := len(s.Transcript) - 1; i >= 0; i-- {
		if s.Transcript[i].Screen != "" {
			return s.Transcript[i].Screen
		}
	}
	return ""
}

// firstDiffLine returns a short preview of the first line that differs
// between old and new, without pulling in a full diff engine.
func firstDiffLine(old, new string) string {
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new, "\n")
	max := len(oldLines)
	if len(newLines) > max {
		max = len(newLines)
	}
	for i := 0; i < max; i++ {
		var o, n string
		if i < len(oldLines) {
			o = oldLines[i]
		}
		if i < len(newLines) {
			n = newLines[i]
		}
		if o != n {
			return fmt.Sprintf("line %d: %q -> %q", i+1, o, n)
		}
	}
	return "(content differs but no differing line found — length mismatch only)"
}
