package main

import (
	"runtime"

	"github.com/sjhorn/slmtest/internal/cliops"
	"github.com/sjhorn/slmtest/internal/sandbox"
)

// RunTestParams mirrors `slmtest run`'s flags — see cliops.RunParams,
// which this is converted to. Field names are snake_case (the MCP/JSON
// convention) rather than matching Go's exported-field convention.
type RunTestParams struct {
	SpecPath string `json:"spec_path" jsonschema:"path to the markdown test-spec file"`

	Endpoint string `json:"endpoint,omitempty" jsonschema:"OpenAI-compatible base URL (default http://localhost:8080/v1)"`
	Model    string `json:"model,omitempty" jsonschema:"model name sent in requests (default local-slm)"`
	APIKey   string `json:"api_key,omitempty" jsonschema:"bearer token, if the endpoint requires one"`

	Shell         string            `json:"shell,omitempty" jsonschema:"shell override (default: the spec's own shell field)"`
	Driver        string            `json:"driver,omitempty" jsonschema:"driver to run against (empty = the spec's driver field, itself defaulting to tui)"`
	DriverOptions map[string]string `json:"driver_options,omitempty" jsonschema:"driver-specific options, overriding the same keys from spec frontmatter"`

	StepTimeoutSeconds    int     `json:"step_timeout_seconds,omitempty" jsonschema:"per-step wall-clock budget in seconds; 0 = no limit"`
	CommandWaitMS         int     `json:"command_wait_ms,omitempty" jsonschema:"default wait after a command when the model omits wait_ms; 0 = built-in 1500ms"`
	ContinueOnFail        bool    `json:"continue_on_fail,omitempty" jsonschema:"attempt every step even after one fails"`
	MaxRetries            int     `json:"max_retries,omitempty" jsonschema:"attempts per SLM request before the run aborts; 1 disables retrying"`
	RequestTimeoutSeconds int     `json:"request_timeout_seconds,omitempty" jsonschema:"timeout for a single model request, in seconds"`
	NativeTools           bool    `json:"native_tools,omitempty" jsonschema:"experimental: use OpenAI tools/tool_calls instead of the prose JSON schema"`
	Temperature           float64 `json:"temperature,omitempty" jsonschema:"sampling temperature sent on every request"`

	ExecPrefix []string       `json:"exec_prefix,omitempty" jsonschema:"wrap the shell in an arbitrary command argv, e.g. [\"ssh\",\"testbox\"] (mutually exclusive with sandbox)"`
	Sandbox    *SandboxParams `json:"sandbox,omitempty" jsonschema:"confine the shell with macOS Seatbelt (mutually exclusive with exec_prefix); omitted entirely defaults to enabled on macOS, disabled elsewhere — any sandbox object, even {}, is explicit and its enabled value (default false) is used as-is"`

	// Tags mirrors the CLI's repeatable -tag flag: with a Feature-style
	// spec (see internal/spec/feature.go), only Scenarios carrying every
	// listed tag are run. Ignored for an ordinary (non-Feature) spec.
	Tags []string `json:"tags,omitempty" jsonschema:"with a Feature-style spec, only run Scenarios carrying every listed tag; ignored for an ordinary spec"`

	JUnitPath    string `json:"junit_path,omitempty" jsonschema:"write the run's report(s) as a JUnit XML document to this path"`
	TraceDir     string `json:"trace_dir,omitempty" jsonschema:"write a self-contained replayable trace bundle (screen snapshots, manifest, report JSON) to this directory"`
	GoldenDir    string `json:"golden_dir,omitempty" jsonschema:"compare each step's final screen against a baseline in this directory (does not affect pass/fail or the reported result)"`
	GoldenUpdate bool   `json:"golden_update,omitempty" jsonschema:"with golden_dir, write/overwrite baselines instead of comparing against them"`

	// The judge is a second opinion recorded in the report, never a gate:
	// it cannot change a step's result or the run's pass/fail, so there is
	// deliberately no param to weight or enforce it. Note a hosted endpoint
	// receives screen contents, which is why it takes an explicit URL.
	JudgeEndpoint string `json:"judge_endpoint,omitempty" jsonschema:"System One decision endpoint that independently grades each step's Expect against the screen; records a second opinion and never affects pass/fail"`
	JudgeModel    string `json:"judge_model,omitempty" jsonschema:"model name sent to judge_endpoint (e.g. open-jev, jev-latest)"`
	JudgeAPIKey   string `json:"judge_api_key,omitempty" jsonschema:"bearer token for judge_endpoint, if it needs one"`
}

// SandboxParams mirrors the CLI's -sandbox* flags.
type SandboxParams struct {
	Enabled       bool     `json:"enabled,omitempty"`
	WritablePaths []string `json:"writable_paths,omitempty" jsonschema:"extra writable paths beyond the scratch-directory defaults"`
	DenyNetwork   bool     `json:"deny_network,omitempty"`
	ProfilePath   string   `json:"profile_path,omitempty" jsonschema:"use this .sb profile instead of the generated one"`
}

func (s *SandboxParams) toConfig() sandbox.Config {
	if s == nil {
		// No sandbox param at all mirrors the CLI's no -sandbox-flag
		// case: OS-aware default (on for macOS, off elsewhere). A
		// caller sending any sandbox object, even {}, is explicit and
		// gets exactly the Enabled value it specified.
		return sandbox.Config{Enabled: cliops.DefaultSandboxEnabled(runtime.GOOS)}
	}
	return sandbox.Config{
		Enabled:       s.Enabled,
		WritablePaths: s.WritablePaths,
		DenyNetwork:   s.DenyNetwork,
		ProfilePath:   s.ProfilePath,
	}
}

// ValidateTestParams is validate_test's input.
type ValidateTestParams struct {
	SpecPath string `json:"spec_path" jsonschema:"path to the markdown test-spec file"`
}

// InitTestParams is init_test's input.
type InitTestParams struct {
	SpecPath string `json:"spec_path" jsonschema:"path to write the new starter test-spec file to; refuses to overwrite an existing file"`
}
