package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sjhorn/slmtest/internal/cliops"
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
	// Loaded once up front, purely to know the step count for progress
	// totals — cliops.Run loads it again itself. Harmless duplicate
	// parse; restructuring cliops.Run to expose this mid-run isn't worth
	// the added surface for a total that's already cheap to compute.
	totalSteps := 0
	if t, err := cliops.LoadSpec(in.SpecPath); err == nil {
		totalSteps = len(t.Steps)
	}

	progressToken := req.Params.GetProgressToken()
	var verbose func(format string, args ...any)
	if progressToken != nil {
		verbose = func(format string, args ...any) {
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

	result, err := cliops.Run(ctx, cliops.RunParams{
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
		Verbose:        verbose,
	})
	if err != nil {
		return nil, nil, err
	}
	out, err := asStructured(result.Report)
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

// handleValidateTest is validate_test's handler: parse-only, no
// execution, no model call — safe to call liberally while an agent
// iterates on a spec it's authoring.
func handleValidateTest(ctx context.Context, req *mcp.CallToolRequest, in ValidateTestParams) (*mcp.CallToolResult, map[string]any, error) {
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

// handleInitTest is init_test's handler: scaffolds a new spec file.
func handleInitTest(ctx context.Context, req *mcp.CallToolRequest, in InitTestParams) (*mcp.CallToolResult, map[string]any, error) {
	if err := cliops.Init(in.SpecPath); err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"spec_path": in.SpecPath, "status": "created"}, nil
}
