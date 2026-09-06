package cliops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func runWithScreenGolden(t *testing.T, dir string, update bool) *RunResult {
	t.Helper()
	endpoint := scriptedSLM(t,
		`{"action":"run_command","command":"echo hi","wait_ms":10}`,
		`{"action":"finish_step","step_result":"pass","reason":"done"}`,
	)
	result, err := Run(context.Background(), RunParams{
		SpecPath:     echoTestSpecPath,
		Endpoint:     endpoint,
		DriverName:   "cliops-test-screen",
		GoldenDir:    dir,
		GoldenUpdate: update,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result
}

func TestGoldenFirstRunReportsMissing(t *testing.T) {
	dir := t.TempDir()
	result := runWithScreenGolden(t, dir, false)
	if len(result.Golden) != 1 {
		t.Fatalf("len(Golden) = %d, want 1", len(result.Golden))
	}
	if result.Golden[0].Status != "missing" {
		t.Errorf("Status = %q, want missing", result.Golden[0].Status)
	}
}

func TestGoldenUpdateThenMatch(t *testing.T) {
	dir := t.TempDir()
	updated := runWithScreenGolden(t, dir, true)
	if updated.Golden[0].Status != "updated" {
		t.Fatalf("Status = %q, want updated", updated.Golden[0].Status)
	}

	matched := runWithScreenGolden(t, dir, false)
	if matched.Golden[0].Status != "match" {
		t.Fatalf("Status = %q, want match", matched.Golden[0].Status)
	}
}

func TestGoldenHandEditedBaselineReportsMismatch(t *testing.T) {
	dir := t.TempDir()
	runWithScreenGolden(t, dir, true)

	baseline := filepath.Join(dir, "echo-smoke-test", "step-1.golden.txt")
	if err := os.WriteFile(baseline, []byte("something completely different"), 0644); err != nil {
		t.Fatalf("hand-editing baseline: %v", err)
	}

	result := runWithScreenGolden(t, dir, false)
	if result.Golden[0].Status != "mismatch" {
		t.Fatalf("Status = %q, want mismatch", result.Golden[0].Status)
	}
	if result.Golden[0].DiffPreview == "" {
		t.Error("DiffPreview is empty, want a non-empty preview")
	}
}
