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
	"github.com/sjhorn/slmtest/internal/buildinfo"
	"github.com/sjhorn/slmtest/internal/judge"
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

// DefaultSandboxEnabled reports whether sandboxing should default to on
// for goos — a pure function of the OS name (not runtime.GOOS itself),
// so both branches are unit-testable regardless of which OS the test
// binary actually runs on. Sandboxing (internal/sandbox) is Seatbelt,
// macOS-only, so only "darwin" defaults to true.
func DefaultSandboxEnabled(goos string) bool {
	return goos == "darwin"
}

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
	// Progress, if set, receives step/turn boundary events for
	// default-on progress feedback (the CLI's spinner, suppressed with
	// -quiet). See runner.Options.OnProgress.
	Progress func(runner.ProgressEvent)

	// JudgeEndpoint, if set, enables the optional second-opinion judge:
	// a System One decision endpoint that grades each step's Expect
	// against the screen. Verified against Open-Jev locally
	// ("http://127.0.0.1:8011/v1/systemone") and hosted Jev
	// ("https://api.typesafe.ai/v1/systemone"). The verdict is recorded
	// in the report and has no authority over any step's result.
	//
	// Note this sends screen contents to whatever endpoint is named,
	// which for a hosted backend means off this machine — hence opt-in
	// by explicit URL rather than any kind of default.
	JudgeEndpoint string
	JudgeModel    string
	JudgeAPIKey   string

	// JUnitPath, if set, writes the run's report(s) as a JUnit XML
	// document to this path after the run completes — see
	// docs/roadmap-reporting-and-agents.md, Phase B.
	JUnitPath string
	// TracePath, if set, writes a self-contained replayable trace bundle
	// (per-turn screen snapshots, a manifest, the full report JSON) to
	// this directory after the run completes — see
	// docs/roadmap-reporting-and-agents.md, Phase C.
	TracePath string
	// GoldenDir/GoldenUpdate configure golden-file screen regression
	// checking (Phase D): GoldenDir is the baseline directory; GoldenUpdate
	// overwrites baselines instead of comparing against them. Deliberately
	// does not affect step pass/fail or the process exit code — this is a
	// complement to the model's own verdict, not a replacement.
	GoldenDir    string
	GoldenUpdate bool
}

// RunResult is everything a caller needs after a run: the parsed Test
// (name, description, step count) alongside the Report -json already
// documents.
type RunResult struct {
	Test   *spec.Test
	Report *runner.Report
	// Golden holds per-step golden-file comparison results, populated
	// only when p.GoldenDir was set — see docs/roadmap-reporting-and-agents.md,
	// Phase D. nil when golden checking wasn't requested.
	Golden []GoldenResult
}

// Run executes one test end-to-end. This is cmdRun's body, unchanged in
// behavior, minus flag parsing and stdout rendering.
func Run(ctx context.Context, p RunParams) (*RunResult, error) {
	t, err := LoadSpec(p.SpecPath)
	if err != nil {
		return nil, err
	}
	report, err := runLoadedTest(ctx, t, p)
	if err != nil {
		return nil, err
	}
	result := &RunResult{Test: t, Report: report}
	if err := writeRunArtifacts(p, []*runner.Report{report}); err != nil {
		return nil, err
	}
	if p.GoldenDir != "" {
		golden, err := compareGolden(p.GoldenDir, []*runner.Report{report}, p.GoldenUpdate)
		if err != nil {
			return nil, err
		}
		result.Golden = golden
	}
	return result, nil
}

// writeRunArtifacts writes whichever optional CI/audit artifacts p
// requested (JUnit XML, a trace bundle) — shared by Run and RunFeature so
// both a single Test and a Feature's whole set of scenario reports go
// through the exact same writers.
func writeRunArtifacts(p RunParams, reports []*runner.Report) error {
	if p.JUnitPath != "" {
		if err := writeJUnitFile(p.JUnitPath, reports); err != nil {
			return err
		}
	}
	if p.TracePath != "" {
		if err := writeTraceBundle(p.TracePath, reports); err != nil {
			return err
		}
	}
	return nil
}

// runLoadedTest is Run's execution body, factored out so RunFeature (see
// feature.go) can run each of a Feature's expanded scenarios through
// exactly the same client/sandbox/timeout setup as a hand-written spec
// file gets — runner.Run itself never needs to know a Test came from a
// Feature's Background+Scenario expansion rather than a human-written
// "## Step N: ..." file.
func runLoadedTest(ctx context.Context, t *spec.Test, p RunParams) (*runner.Report, error) {
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
		// Only a caller-supplied prefix may move the session off this
		// machine; the sandbox prefix keeps it local, so it must not
		// disqualify a step's local Verify check.
		SessionIsRemote: len(p.ExecPrefix) > 0,
		DriverName:      p.DriverName,
		Verbose:         p.Verbose,
		OnProgress:      p.Progress,
		Judge:           p.judge(),
	})
	if err != nil {
		return nil, err
	}
	stampRunContext(report, p, endpoint, model, len(sandboxArgv) > 0)
	return report, nil
}

// stampRunContext fills in the audit-metadata fields of report.RunContext
// that runner.Run itself has no way to know (it only resolves and fills
// in Driver) — the endpoint/model/temperature actually sent, whether
// native-tools mode or sandboxing were in effect, and version/host
// metadata. Shared by Run (via runLoadedTest) and RunFeature, once per
// scenario's report, so both paths get this for free. See
// docs/roadmap-reporting-and-agents.md, Phase A.
func stampRunContext(report *runner.Report, p RunParams, endpoint, model string, sandboxed bool) {
	build := buildinfo.Get()
	host, _ := os.Hostname()
	report.RunContext.Endpoint = endpoint
	report.RunContext.Model = model
	report.RunContext.Temperature = p.Temperature
	report.RunContext.NativeTools = p.NativeTools
	report.RunContext.Sandboxed = sandboxed
	report.RunContext.SlmtestVersion = build.Version
	report.RunContext.GitCommit = build.GitCommit
	report.RunContext.GitDirty = build.GitDirty
	report.RunContext.Host = host
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
	raw, err := LoadSpecRaw(path)
	if err != nil {
		return nil, err
	}
	t, err := spec.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return t, nil
}

// LoadSpecRaw reads a spec file's raw markdown, named/erroring the same
// way LoadSpec does — the shared first step both LoadSpec (parses via
// spec.Parse) and IsFeatureSpec/RunFeature (parse via spec.ParseFeature
// instead) build on, so there is exactly one place a spec file gets read
// off disk.
func LoadSpecRaw(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return string(raw), nil
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

// judge builds the optional second-opinion client, or returns nil when no
// endpoint was given. Returning a nil interface rather than a non-nil
// pointer wrapping an empty endpoint matters: runner.applyJudge skips on
// nil, so an unconfigured judge costs a run nothing at all.
func (p RunParams) judge() runner.Judge {
	if p.JudgeEndpoint == "" {
		return nil
	}
	return &judge.Client{
		Endpoint: p.JudgeEndpoint,
		Model:    p.JudgeModel,
		APIKey:   p.JudgeAPIKey,
	}
}
