package main

import "github.com/sjhorn/slmtest/internal/sandbox"

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
	Sandbox    *SandboxParams `json:"sandbox,omitempty" jsonschema:"confine the shell with macOS Seatbelt (mutually exclusive with exec_prefix)"`

	// Tags mirrors the CLI's repeatable -tag flag: with a Feature-style
	// spec (see internal/spec/feature.go), only Scenarios carrying every
	// listed tag are run. Ignored for an ordinary (non-Feature) spec.
	Tags []string `json:"tags,omitempty" jsonschema:"with a Feature-style spec, only run Scenarios carrying every listed tag; ignored for an ordinary spec"`
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
		return sandbox.Config{}
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
