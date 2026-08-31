package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sjhorn/slmtest/internal/cliops"
	_ "github.com/sjhorn/slmtest/internal/nulldriver" // registers "null", avoiding a real PTY in these tests
)

const echoTestSpecPath = "../../examples/echo-test.md"

// scriptedSLM mirrors internal/cliops's own fixture — deterministic,
// no real model weights needed. Kept package-local rather than shared,
// since a handler test package importing a sibling _test.go's helper
// isn't possible across packages in Go.
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

// noProgressRequest is a *mcp.CallToolRequest with no progress token —
// safe to pass to a handler without a real ServerSession, since
// handleRunTest only touches req.Session when a progress token is
// present.
func noProgressRequest() *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
}

// TestHandleRunTestMatchesCLIReportShape asserts run_test's handler
// produces the same result struct `slmtest run -json` does for a given
// spec — using an example spec as the fixture, per this project's
// existing-example-specs-as-fixtures convention, rather than inventing
// a new one for this alone.
func TestHandleRunTestMatchesCLIReportShape(t *testing.T) {
	endpoint := scriptedSLM(t,
		`{"action":"finish_step","step_result":"pass","reason":"done"}`,
	)

	_, out, err := handleRunTest(context.Background(), noProgressRequest(), RunTestParams{
		SpecPath: echoTestSpecPath,
		Endpoint: endpoint,
		Driver:   "null",
	})
	if err != nil {
		t.Fatalf("handleRunTest: %v", err)
	}

	// Same shape CLAUDE.md documents for `-json`: passed/aborted/steps,
	// each step flattened with status/reason/turns/transcript.
	if out["passed"] != true {
		t.Errorf("passed = %v, want true", out["passed"])
	}
	if out["name"] != "echo-smoke-test" {
		t.Errorf("name = %v, want echo-smoke-test", out["name"])
	}
	steps, ok := out["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("steps = %v, want a one-element array", out["steps"])
	}
	step, ok := steps[0].(map[string]any)
	if !ok {
		t.Fatalf("steps[0] = %v, want an object", steps[0])
	}
	if step["status"] != "pass" {
		t.Errorf("steps[0].status = %v, want pass", step["status"])
	}
}

func TestHandleRunTestSurfacesRunError(t *testing.T) {
	_, _, err := handleRunTest(context.Background(), noProgressRequest(), RunTestParams{
		SpecPath: "/nonexistent/spec.md",
	})
	if err == nil {
		t.Fatal("expected an error for a missing spec file")
	}
}

func TestHandleRunTestAppliesDriverOptions(t *testing.T) {
	endpoint := scriptedSLM(t,
		`{"action":"finish_step","step_result":"pass","reason":"done"}`,
	)
	_, out, err := handleRunTest(context.Background(), noProgressRequest(), RunTestParams{
		SpecPath:      echoTestSpecPath,
		Endpoint:      endpoint,
		Driver:        "null",
		DriverOptions: map[string]string{"extra": "value"},
	})
	if err != nil {
		t.Fatalf("handleRunTest: %v", err)
	}
	if out["passed"] != true {
		t.Fatalf("passed = %v, want true; out: %+v", out["passed"], out)
	}
}

// TestHandleValidateTestMatchesCLI asserts validate_test's handler
// surfaces the same errors `slmtest validate` does — success and
// failure paths, both against the same fixtures cliops's own tests use.
func TestHandleValidateTestMatchesCLI(t *testing.T) {
	_, out, err := handleValidateTest(context.Background(), noProgressRequest(), ValidateTestParams{
		SpecPath: echoTestSpecPath,
	})
	if err != nil {
		t.Fatalf("handleValidateTest: %v", err)
	}
	if out["name"] != "echo-smoke-test" {
		t.Errorf("name = %v, want echo-smoke-test", out["name"])
	}
	steps, ok := out["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("steps = %v, want a one-element array", out["steps"])
	}

	// The error path must match cliops.Validate's own error, since that's
	// exactly what `slmtest validate` surfaces too.
	wantErr, cliErr := cliops.Validate("/nonexistent/spec.md")
	if cliErr == nil {
		t.Fatalf("expected cliops.Validate to error for a missing file, got %+v", wantErr)
	}
	_, _, gotErr := handleValidateTest(context.Background(), noProgressRequest(), ValidateTestParams{
		SpecPath: "/nonexistent/spec.md",
	})
	if gotErr == nil {
		t.Fatal("expected handleValidateTest to error for a missing file")
	}
}

func TestHandleInitTestWritesAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new-test.md")

	_, out, err := handleInitTest(context.Background(), noProgressRequest(), InitTestParams{SpecPath: path})
	if err != nil {
		t.Fatalf("handleInitTest: %v", err)
	}
	if out["status"] != "created" {
		t.Errorf("status = %v, want created", out["status"])
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(raw) != cliops.StarterTemplate {
		t.Error("written content does not match cliops.StarterTemplate")
	}

	if _, _, err := handleInitTest(context.Background(), noProgressRequest(), InitTestParams{SpecPath: path}); err == nil {
		t.Fatal("expected handleInitTest to refuse to overwrite an existing file")
	}
}
