// Package cliops holds the flag-independent business logic behind
// `slmtest run`/`validate`/`init`: typed params in, typed results out,
// no flag.FlagSet, no os.Exit, no stdout/stderr writes. This is the
// concrete mechanism that keeps cmd/slmtest-mcp from becoming a second
// implementation of "run a test" — it calls these same functions with
// params built from an MCP tool call's JSON arguments instead of parsed
// CLI flags, so there is exactly one place this logic lives.
//
// cmd/slmtest's cmdRun/cmdValidate/cmdInit are thin wrappers: parse
// flags, build a Params value, call the matching function here, render
// the result as text or JSON. Anything CLI-specific — flag parsing,
// -exec-prefix's shell-like string splitting, os.Exit codes — stays in
// cmd/slmtest, since an MCP tool call arrives with already-structured
// JSON and has no equivalent concept.
package cliops

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/runner"
	"github.com/sjhorn/slmtest/internal/sandbox"
	"github.com/sjhorn/slmtest/internal/spec"
)

// Defaults shared by both the CLI's flag defaults and the MCP server's
// optional tool params, so the two surfaces can't silently drift apart.
const (
	DefaultEndpoint = "http://localhost:8080/v1"
	DefaultModel    = "local-slm"
)

// RunParams configures one test run. Every field here is typed and
// resolved already — no raw strings needing further parsing (that's
// -exec-prefix's job in cmd/slmtest, done before building this).
type RunParams struct {
	SpecPath string

	Endpoint string
	Model    string
	APIKey   string

	Shell string
	// DriverName overrides the spec's driver: field (itself defaulting
	// to "tui"). Empty means "use the spec's".
	DriverName string
	// DriverOptions overrides/adds to the spec's own DriverOptions
	// (frontmatter's "<driver>_key" values, prefix stripped) — the same
	// role -driver-option plays on the CLI.
	DriverOptions map[string]string

	StepTimeout    time.Duration
	CommandWaitMS  int
	ContinueOnFail bool
	MaxRetries     int
	RequestTimeout time.Duration
	NativeTools    bool
	Temperature    float64

	// ExecPrefix wraps the shell in a sandbox or remote session argv —
	// already split into words, unlike the CLI's raw -exec-prefix string.
	ExecPrefix []string
	// Sandbox is applied the same way cmd/slmtest's -sandbox flags are:
	// its resolved argv replaces ExecPrefix (the two are mutually
	// exclusive; see Run).
	Sandbox sandbox.Config

	// Verbose, if set, receives the same per-turn log lines -verbose
	// prints to stderr on the CLI.
	Verbose func(format string, args ...any)
}

// RunResult is everything a caller needs after a run: the parsed Test
// (name, description, step count) alongside the Report -json already
// documents.
type RunResult struct {
	Test   *spec.Test
	Report *runner.Report
}

// Run executes one test end-to-end. This is cmdRun's body, unchanged in
// behavior, minus flag parsing and stdout rendering.
func Run(ctx context.Context, p RunParams) (*RunResult, error) {
	t, err := LoadSpec(p.SpecPath)
	if err != nil {
		return nil, err
	}
	if len(p.DriverOptions) > 0 {
		if t.DriverOptions == nil {
			t.DriverOptions = map[string]string{}
		}
		for k, v := range p.DriverOptions {
			t.DriverOptions[k] = v
		}
	}

	sandboxArgv, err := p.Sandbox.Argv()
	if err != nil {
		return nil, err
	}
	prefix := p.ExecPrefix
	// Composing the two would sandbox the wrapper rather than the shell —
	// `sandbox-exec ... ssh host sh` confines the ssh client, not the
	// remote shell — so refuse rather than silently doing the wrong thing.
	if len(sandboxArgv) > 0 && len(prefix) > 0 {
		return nil, fmt.Errorf("sandbox and exec prefix are mutually exclusive: " +
			"sandboxing the wrapper would not sandbox the shell it launches")
	}
	if len(sandboxArgv) > 0 {
		prefix = sandboxArgv
	}

	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	model := p.Model
	if model == "" {
		model = DefaultModel
	}
	client := agent.NewClient(endpoint, model, p.APIKey)
	if p.MaxRetries > 0 {
		client.Retry.MaxAttempts = p.MaxRetries
	}
	if p.RequestTimeout > 0 {
		client.SetRequestTimeout(p.RequestTimeout)
	}
	client.NativeTools = p.NativeTools
	if p.Temperature != 0 {
		client.Temperature = p.Temperature
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if t.TimeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(t.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	report, err := runner.Run(runCtx, t, client, runner.Options{
		Shell:          p.Shell,
		StepTimeout:    p.StepTimeout,
		CommandWaitMS:  p.CommandWaitMS,
		ContinueOnFail: p.ContinueOnFail,
		ExecPrefix:     prefix,
		DriverName:     p.DriverName,
		Verbose:        p.Verbose,
	})
	if err != nil {
		return nil, err
	}
	return &RunResult{Test: t, Report: report}, nil
}

// Validate parse-checks a spec file, with no execution and no model
// call — identical to LoadSpec, named separately so callers (the CLI's
// `validate` command, the MCP `validate_test` tool) read as intentional
// rather than reusing Run's loader by coincidence.
func Validate(specPath string) (*spec.Test, error) {
	return LoadSpec(specPath)
}

// LoadSpec reads and parses one markdown test-spec file.
func LoadSpec(path string) (*spec.Test, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	t, err := spec.Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return t, nil
}

// Init writes a starter test spec to path, refusing to overwrite an
// existing file.
func Init(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	return os.WriteFile(path, []byte(StarterTemplate), 0644)
}

// StarterTemplate is the file Init writes.
const StarterTemplate = `---
name: my-test
description: One-line description of what this test verifies
shell: /bin/bash
timeout_seconds: 180
max_turns_per_step: 6
---

## Step 1: Describe the first thing to check
Goal: What state the system should be in after this step.
Hint: an optional suggested command — the agent may deviate from it.
Expect: The concrete, checkable condition that means this step passed.

## Step 2: Describe the next thing to check
Goal: ...
Hint: ...
Expect: ...
`
