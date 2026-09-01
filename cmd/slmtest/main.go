// Command slmtest runs markdown-defined terminal tests against a small
// language model driving a real PTY session.
//
// Usage:
//
//	slmtest run <file.md> [flags]
//	slmtest validate <file.md>
//	slmtest init <file.md>
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/cliops"
	_ "github.com/sjhorn/slmtest/internal/nulldriver" // registers the "null" driver
	_ "github.com/sjhorn/slmtest/internal/ptydriver"  // registers the "tui" driver
	"github.com/sjhorn/slmtest/internal/runner"
	"github.com/sjhorn/slmtest/internal/sandbox"
	"github.com/sjhorn/slmtest/internal/spec"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "validate":
		err = cmdValidate(os.Args[2:])
	case "init":
		err = cmdInit(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `slmtest — run markdown terminal tests driven by a small language model

Usage:
  slmtest run <file.md> [flags]        execute a test spec
  slmtest validate <file.md> [flags]   parse-check a test spec, no execution
  slmtest init <file.md>               write a starter test spec

Run flags:
  -endpoint   OpenAI-compatible base URL (default http://localhost:8080/v1)
  -model      model name to send in requests (default "local-slm")
  -api-key    bearer token, if the endpoint requires one
  -shell      shell to launch in the PTY (default /bin/bash, overrides spec)
  -driver     driver to run against (empty = spec's driver: field, default tui)
  -driver-option  driver-specific option as key=value (repeatable),
                  overrides the same key from spec frontmatter
  -json       print the final report as JSON instead of human-readable text
  -verbose    print each turn (prompt/reply/pty output) as it happens

  -step-timeout      per-step wall-clock budget (e.g. 90s); 0 = no limit
  -command-wait-ms   default wait after a command when the model omits wait_ms
  -continue-on-fail  attempt every step even after one fails
  -max-retries       attempts per SLM request before aborting (1 disables retrying)
  -request-timeout   timeout for a single model request (default 2m); raise it
                     for slow or CPU-only models
  -native-tools      experimental: use OpenAI tools/tool_calls instead of
                     the prose JSON schema (off by default; see docs/model-runs.md)
  -temperature       sampling temperature sent on every request (default 0.1);
                     no universal right value -- test both extremes per model
                     (see docs/model-runs.md)
  -sandbox           confine the shell with macOS Seatbelt (writes limited
                     to scratch dirs; reads and network still allowed)
  -sandbox-write     with -sandbox, an extra writable path (repeatable)
  -sandbox-deny-network  with -sandbox, also block all network access
  -sandbox-profile   with -sandbox, a custom .sb profile to use instead
  -exec-prefix       wrap the shell in an arbitrary command, e.g.
                     "ssh testbox" (mutually exclusive with -sandbox)

Validate flags:
  -json       print the parsed spec as JSON instead of human-readable text
`)
}

func cmdRun(args []string) error {
	filePath, rest, err := takeLeadingPositional(args)
	if err != nil {
		return fmt.Errorf("usage: slmtest run <file.md> [flags]: %w", err)
	}

	fs := flag.NewFlagSet("run", flag.ExitOnError)
	endpoint := fs.String("endpoint", cliops.DefaultEndpoint, "OpenAI-compatible base URL")
	model := fs.String("model", cliops.DefaultModel, "model name")
	apiKey := fs.String("api-key", "", "bearer token")
	shell := fs.String("shell", "", "shell override")
	driverName := fs.String("driver", "", "driver to run against (empty = use the spec's driver: field, itself defaulting to tui)")
	asJSON := fs.Bool("json", false, "print JSON report")
	verbose := fs.Bool("verbose", false, "print each turn")
	stepTimeout := fs.Duration("step-timeout", 0, "per-step wall-clock budget (e.g. 90s); 0 = no limit")
	commandWait := fs.Int("command-wait-ms", 0, "default wait after a command when the model omits wait_ms (0 = built-in 1500)")
	continueOnFail := fs.Bool("continue-on-fail", false, "attempt every step even after one fails")
	maxRetries := fs.Int("max-retries", agent.DefaultRetry().MaxAttempts, "attempts per SLM request before the run aborts (1 disables retrying)")
	requestTimeout := fs.Duration("request-timeout", agent.DefaultRequestTimeout, "timeout for a single model request; raise it for slow or CPU-only models")
	nativeTools := fs.Bool("native-tools", false, "experimental: use OpenAI tools/tool_calls instead of the prose JSON schema (see docs/model-runs.md)")
	temperature := fs.Float64("temperature", agent.DefaultTemperature, "sampling temperature sent on every request; no universal right value, test both extremes per model (see docs/model-runs.md)")
	execPrefix := fs.String("exec-prefix", "", `wrap the shell in an arbitrary command, e.g. "ssh testbox"`)
	useSandbox := fs.Bool("sandbox", false, "confine the shell with macOS Seatbelt: writes limited to scratch dirs")
	denyNetwork := fs.Bool("sandbox-deny-network", false, "with -sandbox, also block all network access")
	sandboxProfile := fs.String("sandbox-profile", "", "with -sandbox, use this .sb profile instead of the generated one")
	var writable stringList
	fs.Var(&writable, "sandbox-write", "with -sandbox, an extra writable path (repeatable)")
	var driverOptions stringList
	fs.Var(&driverOptions, "driver-option", `driver-specific option as key=value (repeatable), e.g. -driver-option url=file:///path/to/page.html; overrides the same key from spec frontmatter`)
	var tags stringList
	fs.Var(&tags, "tag", `with a Feature-style spec (see internal/spec/feature.go), only run Scenarios carrying this tag (repeatable — a scenario must carry every listed tag); ignored for an ordinary spec file`)
	if err := fs.Parse(rest); err != nil {
		return err
	}

	prefix, err := splitArgs(*execPrefix)
	if err != nil {
		return fmt.Errorf("-exec-prefix: %w", err)
	}

	driverOpts, err := parseKeyValueList(driverOptions, "-driver-option")
	if err != nil {
		return err
	}

	var logFn func(string, ...any)
	if *verbose {
		logFn = func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
	}

	runParams := cliops.RunParams{
		SpecPath:       filePath,
		Endpoint:       *endpoint,
		Model:          *model,
		APIKey:         *apiKey,
		Shell:          *shell,
		DriverName:     *driverName,
		DriverOptions:  driverOpts,
		StepTimeout:    *stepTimeout,
		CommandWaitMS:  *commandWait,
		ContinueOnFail: *continueOnFail,
		MaxRetries:     *maxRetries,
		RequestTimeout: *requestTimeout,
		NativeTools:    *nativeTools,
		Temperature:    *temperature,
		ExecPrefix:     prefix,
		Sandbox: sandbox.Config{
			Enabled:       *useSandbox,
			WritablePaths: writable,
			DenyNetwork:   *denyNetwork,
			ProfilePath:   *sandboxProfile,
		},
		Verbose: logFn,
	}

	// A spec using the optional Feature/Background/Scenario markdown
	// layer (see internal/spec/feature.go) runs every Scenario to
	// completion and gets its own report shape; an ordinary spec file's
	// behavior — including the -json shape, a documented CI contract —
	// is completely unchanged, going through cliops.Run exactly as
	// before.
	isFeature, err := cliops.IsFeatureSpec(filePath)
	if err != nil {
		return err
	}
	if isFeature {
		return runFeature(runParams, tags, *asJSON)
	}

	result, err := cliops.Run(context.Background(), runParams)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result.Report); err != nil {
			return err
		}
	} else {
		printReport(result.Report)
	}

	if !result.Report.Passed {
		os.Exit(1)
	}
	return nil
}

// runFeature runs a Feature-style spec (see internal/spec/feature.go)
// and renders its result — a wrapper around printReport/-json's existing
// per-scenario shape rather than a new one, so a Feature report reads as
// "the same report format, once per scenario" instead of a bespoke
// format to learn.
func runFeature(p cliops.RunParams, tags []string, asJSON bool) error {
	result, err := cliops.RunFeature(context.Background(), p, tags)
	if err != nil {
		return err
	}

	if asJSON {
		type featureJSON struct {
			Feature   string           `json:"feature"`
			Passed    bool             `json:"passed"`
			Scenarios []*runner.Report `json:"scenarios"`
		}
		out := featureJSON{Feature: result.Feature.Name, Passed: result.Passed}
		for _, sc := range result.Scenarios {
			out.Scenarios = append(out.Scenarios, sc.Report)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		fmt.Printf("Feature: %s\n", result.Feature.Name)
		for _, sc := range result.Scenarios {
			fmt.Printf("  Scenario: %s\n", sc.Test.Name)
			for _, s := range sc.Report.Steps {
				status := strings.ToUpper(string(s.Status()))
				fmt.Printf("    [%s] step %d: %s (%d turns) — %s\n", status, s.Step.Index, s.Step.Title, s.Turns, s.Reason)
			}
		}
		passedCount := 0
		for _, sc := range result.Scenarios {
			if sc.Report.Passed {
				passedCount++
			}
		}
		verdict := "PASS"
		if !result.Passed {
			verdict = "FAIL"
		}
		fmt.Printf("FEATURE RESULT: %s (%d/%d scenarios passed)\n", verdict, passedCount, len(result.Scenarios))
	}

	if !result.Passed {
		os.Exit(1)
	}
	return nil
}

func cmdValidate(args []string) error {
	filePath, rest, err := takeLeadingPositional(args)
	if err != nil {
		return fmt.Errorf("usage: slmtest validate <file.md> [flags]: %w", err)
	}
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the parsed spec as JSON")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	isFeature, err := cliops.IsFeatureSpec(filePath)
	if err != nil {
		return err
	}
	if isFeature {
		return validateFeature(filePath, *asJSON)
	}

	t, err := cliops.Validate(filePath)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(t)
	}

	fmt.Printf("OK: %q — %d step(s)\n", t.Name, len(t.Steps))
	for _, s := range t.Steps {
		fmt.Printf("  step %d: %s\n", s.Index, s.Title)
	}
	return nil
}

// validateFeature parse-checks a Feature-style spec (see
// internal/spec/feature.go): every Scenario's Background+own steps are
// shown expanded, exactly as RunFeature would run them — this is the
// Feature-aware equivalent of cmdValidate's own "print each parsed step"
// summary.
func validateFeature(filePath string, asJSON bool) error {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", filePath, err)
	}
	f, err := spec.ParseFeature(string(raw))
	if err != nil {
		return fmt.Errorf("parsing %s: %w", filePath, err)
	}
	tests, err := f.Expand()
	if err != nil {
		return fmt.Errorf("expanding %s: %w", filePath, err)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Feature   *spec.Feature `json:"feature"`
			Scenarios []*spec.Test  `json:"scenarios"`
		}{f, tests})
	}

	fmt.Printf("OK: %q — %d scenario(s)\n", f.Name, len(tests))
	for _, t := range tests {
		fmt.Printf("  scenario %q — %d step(s)\n", t.Name, len(t.Steps))
		for _, s := range t.Steps {
			fmt.Printf("    step %d: %s\n", s.Index, s.Title)
		}
	}
	return nil
}

func cmdInit(args []string) error {
	path, rest, err := takeLeadingPositional(args)
	if err != nil || len(rest) != 0 {
		return fmt.Errorf("usage: slmtest init <file.md>")
	}
	return cliops.Init(path)
}

// takeLeadingPositional pulls a single required leading positional argument
// (the test-spec path) off args, returning it and the remaining args for
// flag.FlagSet.Parse. This exists because Go's flag package stops parsing
// at the first non-flag token, which would otherwise silently swallow any
// flags placed after the file path — and our documented usage puts the
// path first (`slmtest run <file.md> [flags]`).
func takeLeadingPositional(args []string) (positional string, rest []string, err error) {
	if len(args) == 0 || len(args[0]) == 0 || args[0][0] == '-' {
		return "", nil, fmt.Errorf("missing required file argument")
	}
	return args[0], args[1:], nil
}

// stringList collects a repeatable flag into a slice. Repetition beats a
// comma-separated value here because the values are filesystem paths,
// which may legitimately contain commas.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ", ") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// parseKeyValueList turns repeated "key=value" flag values into a map,
// naming flagName in the error so it's clear which flag was malformed.
func parseKeyValueList(kvs []string, flagName string) (map[string]string, error) {
	if len(kvs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("%s %q: expected key=value", flagName, kv)
		}
		m[k] = v
	}
	return m, nil
}

// splitArgs splits a command line into argv the way a shell would for the
// simple cases: whitespace separates words, and single quotes, double
// quotes, and backslash escapes group them.
//
// It deliberately stops there — no variable expansion, globbing, pipes, or
// substitution. -exec-prefix takes a sandbox invocation like
// `docker run --rm -it ubuntu:24.04`, and the alternative (handing the
// string to `sh -c`) would silently accept shell metacharacters that this
// harness has no business evaluating on the user's behalf.
func splitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	var quote rune // 0, '\'' or '"'
	started := false

	for i := 0; i < len(s); i++ {
		c := rune(s[i])
		switch {
		case quote == 0 && (c == ' ' || c == '\t' || c == '\n'):
			if started {
				args = append(args, cur.String())
				cur.Reset()
				started = false
			}
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
			started = true
		case quote != 0 && c == quote:
			quote = 0
		case c == '\\' && quote != '\'':
			// A backslash escapes the next character everywhere except
			// inside single quotes, matching shell behavior.
			if i+1 >= len(s) {
				return nil, fmt.Errorf("trailing backslash in %q", s)
			}
			i++
			cur.WriteByte(s[i])
			started = true
		default:
			cur.WriteRune(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote in %q", quote, s)
	}
	if started {
		args = append(args, cur.String())
	}
	return args, nil
}

func printReport(r *runner.Report) {
	fmt.Printf("Test: %s\n", r.Test.Name)
	for _, s := range r.Steps {
		status := strings.ToUpper(string(s.Status()))
		fmt.Printf("  [%s] step %d: %s (%d turns) — %s\n", status, s.Step.Index, s.Step.Title, s.Turns, s.Reason)
	}
	if r.Passed {
		fmt.Println("RESULT: PASS")
	} else {
		fmt.Println("RESULT: FAIL")
	}
}
