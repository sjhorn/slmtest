package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sjhorn/slmtest/internal/cliops"
	"github.com/sjhorn/slmtest/internal/spec"
)

// asStructured marshals v (honoring any custom MarshalJSON — notably
// runner.Report's own, which is the documented -json CI contract) and
// re-decodes it into a generic map so the MCP tool's StructuredContent
// carries exactly the same shape -json produces, without AddTool
// inferring a schema from Go's internal (and differently-shaped) struct
// fields.
func asStructured(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshaling result: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("re-decoding result: %w", err)
	}
	return out, nil
}

// handleRunTest is run_test's handler. It mirrors slmtest run's flags
// (via RunTestParams -> cliops.RunParams) and returns the same
// structured result -json produces, reusing runner.Report directly —
// see asStructured. Progress notifications are sent once per completed
// step, matching CLAUDE.md's documented behavior, but only if the
// client actually requested them (supplied a progress token) — sending
// them unconditionally would be protocol noise for a client that never
// asked.
func handleRunTest(ctx context.Context, req *mcp.CallToolRequest, in RunTestParams) (*mcp.CallToolResult, map[string]any, error) {
	isFeature, err := cliops.IsFeatureSpec(in.SpecPath)
	if err != nil {
		return nil, nil, err
	}

	runParams := cliops.RunParams{
		SpecPath:       in.SpecPath,
		Endpoint:       in.Endpoint,
		Model:          in.Model,
		APIKey:         in.APIKey,
		Shell:          in.Shell,
		DriverName:     in.Driver,
		DriverOptions:  in.DriverOptions,
		StepTimeout:    time.Duration(in.StepTimeoutSeconds) * time.Second,
		CommandWaitMS:  in.CommandWaitMS,
		ContinueOnFail: in.ContinueOnFail,
		MaxRetries:     in.MaxRetries,
		RequestTimeout: time.Duration(in.RequestTimeoutSeconds) * time.Second,
		NativeTools:    in.NativeTools,
		Temperature:    in.Temperature,
		ExecPrefix:     in.ExecPrefix,
		Sandbox:        in.Sandbox.toConfig(),
	}

	// A spec using the optional Feature/Background/Scenario markdown
	// layer (see internal/spec/feature.go) gets its own report shape and
	// its own progress-notification granularity (per scenario, not per
	// step — see handleRunFeatureTest); an ordinary spec's behavior,
	// including the structured result's shape (the same one -json
	// documents), is completely unchanged either way, matching
	// cmd/slmtest's own run/validate auto-detection.
	if isFeature {
		return handleRunFeatureTest(ctx, req, runParams, in.Tags)
	}

	progressToken := req.Params.GetProgressToken()
	if progressToken != nil {
		// Loaded once up front, purely to know the step count for
		// progress totals — cliops.Run loads it again itself. Harmless
		// duplicate parse; restructuring cliops.Run to expose this
		// mid-run isn't worth the added surface for a total that's
		// already cheap to compute.
		totalSteps := 0
		if t, err := cliops.LoadSpec(in.SpecPath); err == nil {
			totalSteps = len(t.Steps)
		}
		runParams.Verbose = func(format string, args ...any) {
			// runner.go's Verbose call sites: "=== step %d: %s ===" at a
			// step's start, "step %d passed: %s" / "step %d FAILED: %s"
			// at its end, "aborted: %s" on abort. Only the completion
			// lines are step-boundary events worth a progress
			// notification — the start line would double up per step.
			if !strings.HasPrefix(format, "step %d passed") && !strings.HasPrefix(format, "step %d FAILED") {
				return
			}
			idx, _ := args[0].(int)
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: progressToken,
				Message:       fmt.Sprintf(format, args...),
				Progress:      float64(idx),
				Total:         float64(totalSteps),
			})
		}
	}

	result, err := cliops.Run(ctx, runParams)
	if err != nil {
		return nil, nil, err
	}
	out, err := asStructured(result.Report)
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

// handleRunFeatureTest is handleRunTest's Feature-spec branch: runs
// every Scenario via cliops.RunFeature and returns the same
// {"feature": ..., "passed": ..., "scenarios": [...]} shape
// cmd/slmtest's own runFeature prints for -json, so a client that
// already understands one understands the other. Progress notifications
// (when the caller supplied a token) fire once per completed scenario
// rather than once per step — a Feature run's natural granularity, since
// steps reset per scenario the same way they reset per Test.
func handleRunFeatureTest(ctx context.Context, req *mcp.CallToolRequest, p cliops.RunParams, tags []string) (*mcp.CallToolResult, map[string]any, error) {
	progressToken := req.Params.GetProgressToken()
	var totalScenarios int
	if progressToken != nil {
		if raw, err := cliops.LoadSpecRaw(p.SpecPath); err == nil {
			if f, err := spec.ParseFeature(raw); err == nil {
				totalScenarios = len(f.Filter(tags).Scenarios)
			}
		}
	}

	result, err := cliops.RunFeature(ctx, p, tags)
	if err != nil {
		return nil, nil, err
	}

	if progressToken != nil {
		for i, sc := range result.Scenarios {
			status := "passed"
			if !sc.Report.Passed {
				status = "FAILED"
			}
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: progressToken,
				Message:       fmt.Sprintf("scenario %d %s: %s", i+1, status, sc.Test.Name),
				Progress:      float64(i + 1),
				Total:         float64(totalScenarios),
			})
		}
	}

	scenarios := make([]map[string]any, 0, len(result.Scenarios))
	for _, sc := range result.Scenarios {
		out, err := asStructured(sc.Report)
		if err != nil {
			return nil, nil, err
		}
		scenarios = append(scenarios, out)
	}
	// Round-tripped through asStructured, like every other handler's
	// result, so a caller sees the same generic JSON-shaped map/slice
	// types (map[string]any / []any) it would get from any other tool,
	// rather than this handler's own Go-native []map[string]any leaking
	// through untouched.
	out, err := asStructured(map[string]any{
		"feature":   result.Feature.Name,
		"passed":    result.Passed,
		"scenarios": scenarios,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

// handleValidateTest is validate_test's handler: parse-only, no
// execution, no model call — safe to call liberally while an agent
// iterates on a spec it's authoring.
func handleValidateTest(ctx context.Context, req *mcp.CallToolRequest, in ValidateTestParams) (*mcp.CallToolResult, map[string]any, error) {
	isFeature, err := cliops.IsFeatureSpec(in.SpecPath)
	if err != nil {
		return nil, nil, err
	}
	if isFeature {
		return handleValidateFeatureTest(in.SpecPath)
	}

	t, err := cliops.Validate(in.SpecPath)
	if err != nil {
		return nil, nil, err
	}
	out, err := asStructured(t)
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

// handleValidateFeatureTest is handleValidateTest's Feature-spec branch:
// parses and expands (Background prepended, Scenario Outline rows
// substituted) without running anything, returning
// {"feature": ..., "scenarios": [...]} — the same shape cmd/slmtest's
// own validateFeature prints for -json.
func handleValidateFeatureTest(specPath string) (*mcp.CallToolResult, map[string]any, error) {
	raw, err := cliops.LoadSpecRaw(specPath)
	if err != nil {
		return nil, nil, err
	}
	f, err := spec.ParseFeature(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", specPath, err)
	}
	tests, err := f.Expand()
	if err != nil {
		return nil, nil, fmt.Errorf("expanding %s: %w", specPath, err)
	}
	out, err := asStructured(struct {
		Feature   *spec.Feature `json:"feature"`
		Scenarios []*spec.Test  `json:"scenarios"`
	}{f, tests})
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

// handleInitTest is init_test's handler: scaffolds a new spec file.
func handleInitTest(ctx context.Context, req *mcp.CallToolRequest, in InitTestParams) (*mcp.CallToolResult, map[string]any, error) {
	if err := cliops.Init(in.SpecPath); err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"spec_path": in.SpecPath, "status": "created"}, nil
}
