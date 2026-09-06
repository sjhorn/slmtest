package cliops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sjhorn/slmtest/internal/driver"
	"github.com/sjhorn/slmtest/internal/nulldriver"
)

// A driver registered here, wrapping nulldriver.NewScripted with a
// pre-loaded observation that carries a Screen — the registry-based path
// (driver.Get, which is all cliops.Run/runner.Run ever use) has no other
// way to inject scripted observations, unlike a direct nulldriver.Driver
// construction.
func init() {
	driver.Register("cliops-test-screen", func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return nulldriver.NewScripted(driver.Observation{Text: "hello", Screen: "hello\nscreen"}), nil
	})
}

func TestRunWritesTraceBundle(t *testing.T) {
	endpoint := scriptedSLM(t,
		`{"action":"run_command","command":"echo hi","wait_ms":10}`,
		`{"action":"finish_step","step_result":"pass","reason":"done"}`,
	)
	dir := t.TempDir()
	_, err := Run(context.Background(), RunParams{
		SpecPath:   echoTestSpecPath,
		Endpoint:   endpoint,
		DriverName: "cliops-test-screen",
		TracePath:  dir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	manifestRaw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("reading manifest.json: %v", err)
	}
	var manifest []map[string]any
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatalf("parsing manifest.json: %v", err)
	}
	if len(manifest) == 0 {
		t.Fatal("manifest.json has no entries, want at least one snapshot")
	}
	snapshotFile, ok := manifest[0]["snapshot_file"].(string)
	if !ok || snapshotFile == "" {
		t.Fatalf("manifest[0].snapshot_file = %v, want a non-empty path", manifest[0]["snapshot_file"])
	}
	if _, err := os.Stat(filepath.Join(dir, snapshotFile)); err != nil {
		t.Errorf("manifest's snapshot_file does not resolve: %v", err)
	}
}
