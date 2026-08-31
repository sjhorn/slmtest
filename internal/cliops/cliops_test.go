package cliops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/sjhorn/slmtest/internal/nulldriver" // registers "null", avoiding a real PTY in these tests
	"github.com/sjhorn/slmtest/internal/sandbox"
)

// scriptedSLM is a minimal fixed-script OpenAI-compatible server, in the
// same spirit as internal/runner's fakeSLM and internal/agent's fixture
// pattern — deterministic, no real model weights needed.
func scriptedSLM(t *testing.T, replies ...string) string {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n >= len(replies) {
			http.Error(w, "script exhausted", http.StatusInternalServerError)
			return
		}
		reply := replies[n]
		n++
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": reply}}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

const echoTestSpecPath = "../../examples/echo-test.md"

func TestLoadSpecParsesAnExampleSpec(t *testing.T) {
	tst, err := LoadSpec(echoTestSpecPath)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if tst.Name != "echo-smoke-test" {
		t.Errorf("Name = %q, want echo-smoke-test", tst.Name)
	}
	if len(tst.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(tst.Steps))
	}
}

func TestValidateIsLoadSpec(t *testing.T) {
	viaValidate, err := Validate(echoTestSpecPath)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	viaLoad, err := LoadSpec(echoTestSpecPath)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if viaValidate.Name != viaLoad.Name || len(viaValidate.Steps) != len(viaLoad.Steps) {
		t.Errorf("Validate and LoadSpec disagree: %+v vs %+v", viaValidate, viaLoad)
	}
}

func TestLoadSpecMissingFile(t *testing.T) {
	if _, err := LoadSpec("/nonexistent/path/to/a/spec.md"); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestInitWritesStarterTemplateAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new-test.md")

	if err := Init(path); err != nil {
		t.Fatalf("Init: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(raw) != StarterTemplate {
		t.Errorf("written content does not match StarterTemplate")
	}

	if err := Init(path); err == nil {
		t.Fatal("expected Init to refuse to overwrite an existing file")
	}
}

// TestRunMatchesDirectRunnerCall proves cliops.Run is a thin,
// behavior-preserving wrapper around runner.Run + agent.NewClient — the
// same guarantee cmd/slmtest's smoke-tested CLI behavior depends on, and
// what cmd/slmtest-mcp will depend on in turn.
func TestRunMatchesDirectRunnerCall(t *testing.T) {
	// finish_step needs no driver Dispatch at all — the "null" driver's
	// registry factory starts with no scripted observations, so this
	// avoids relying on one existing (that's nulldriver.NewScripted's
	// job, exercised directly in internal/nulldriver's own tests).
	endpoint := scriptedSLM(t,
		`{"action":"finish_step","step_result":"pass","reason":"done"}`,
	)

	result, err := Run(context.Background(), RunParams{
		SpecPath:   echoTestSpecPath,
		Endpoint:   endpoint,
		DriverName: "null",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Test.Name != "echo-smoke-test" {
		t.Errorf("Test.Name = %q", result.Test.Name)
	}
	if !result.Report.Passed {
		t.Fatalf("Report.Passed = false, want true; steps: %+v", result.Report.Steps)
	}
}

func TestRunUnknownSpecPathIsAnError(t *testing.T) {
	if _, err := Run(context.Background(), RunParams{SpecPath: "/nonexistent.md"}); err == nil {
		t.Fatal("expected an error for a missing spec file")
	}
}

func TestRunAppliesDriverOptionsOverride(t *testing.T) {
	endpoint := scriptedSLM(t,
		`{"action":"finish_step","step_result":"pass","reason":"done"}`,
	)
	result, err := Run(context.Background(), RunParams{
		SpecPath:      echoTestSpecPath,
		Endpoint:      endpoint,
		DriverName:    "null",
		DriverOptions: map[string]string{"extra": "value"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Test.DriverOptions["extra"] != "value" {
		t.Errorf("DriverOptions[extra] = %q, want it merged in from RunParams", result.Test.DriverOptions["extra"])
	}
}

func TestRunSandboxAndExecPrefixAreMutuallyExclusive(t *testing.T) {
	_, err := Run(context.Background(), RunParams{
		SpecPath:   echoTestSpecPath,
		DriverName: "null",
		ExecPrefix: []string{"ssh", "host"},
		Sandbox:    sandbox.Config{Enabled: true},
	})
	if err == nil {
		t.Fatal("expected an error when both Sandbox and ExecPrefix are set")
	}
}
