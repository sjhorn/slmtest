package cliops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sjhorn/slmtest/internal/runner"
)

// This file writes a self-contained, replayable trace bundle for one or
// more Reports — the "what did the model actually see" audit artifact
// from docs/roadmap-reporting-and-agents.md, Phase C. It reuses
// TurnLog.Screen (Phase A/the driver.Observation.Screen plumbing) as its
// only capture mechanism; this file only adds persistence + a manifest,
// mirroring how a Playwright trace.zip is one self-contained artifact a
// human or agent can inspect after the fact without re-running anything.

// traceManifestEntry is one row of manifest.json — one per turn that had
// a non-empty screen snapshot.
type traceManifestEntry struct {
	Test         string `json:"test"`
	StepIndex    int    `json:"step_index"`
	StepTitle    string `json:"step_title"`
	Turn         int    `json:"turn"`
	SnapshotFile string `json:"snapshot_file"`
	Action       string `json:"action"`
	Status       string `json:"status"`
	Reason       string `json:"reason"`
	StartedAt    string `json:"started_at,omitempty"`
}

// writeTraceBundle writes, under dir:
//   - <test-slug>/step-<N>-turn-<M>.txt for every turn with a non-empty
//     screen snapshot
//   - <test-slug>/report.json, the full Report JSON for that test
//   - manifest.json, a flat index across every report passed in
func writeTraceBundle(dir string, reports []*runner.Report) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating trace dir %s: %w", dir, err)
	}

	var manifest []traceManifestEntry
	for _, r := range reports {
		name := "test"
		if r.Test != nil && r.Test.Name != "" {
			name = r.Test.Name
		}
		slug := slugify(name)
		testDir := filepath.Join(dir, slug)
		if err := os.MkdirAll(testDir, 0755); err != nil {
			return fmt.Errorf("creating trace dir %s: %w", testDir, err)
		}

		for _, s := range r.Steps {
			for turnIdx, tl := range s.Transcript {
				if tl.Screen == "" {
					continue
				}
				turn := turnIdx + 1
				fname := fmt.Sprintf("step-%d-turn-%d.txt", s.Step.Index, turn)
				if err := os.WriteFile(filepath.Join(testDir, fname), []byte(tl.Screen), 0644); err != nil {
					return fmt.Errorf("writing snapshot %s: %w", fname, err)
				}
				manifest = append(manifest, traceManifestEntry{
					Test:         name,
					StepIndex:    s.Step.Index,
					StepTitle:    s.Step.Title,
					Turn:         turn,
					SnapshotFile: filepath.Join(slug, fname),
					Action:       string(tl.Action.Action),
					Status:       string(s.Status()),
					Reason:       s.Reason,
					StartedAt:    formatTraceTime(tl),
				})
			}
		}

		reportJSON, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling report for %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(testDir, "report.json"), reportJSON, 0644); err != nil {
			return fmt.Errorf("writing report.json for %s: %w", name, err)
		}
	}

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling trace manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestJSON, 0644); err != nil {
		return fmt.Errorf("writing trace manifest: %w", err)
	}
	return nil
}

// formatTraceTime renders a turn's StartedAt the same way the -json
// report's own started_at fields are formatted (RFC3339), or "" for a
// zero time.
func formatTraceTime(tl runner.TurnLog) string {
	if tl.StartedAt.IsZero() {
		return ""
	}
	return tl.StartedAt.Format(time.RFC3339)
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a test name into a safe directory-name component:
// lowercase, non-alphanumeric runs collapsed to a single "-", and
// leading/trailing "-" trimmed.
func slugify(name string) string {
	s := slugNonAlnum.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "test"
	}
	return s
}
