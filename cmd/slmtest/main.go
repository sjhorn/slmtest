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
	"io"
	"os"
	"runtime"
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
  -quiet      suppress the default spinner/progress output on stderr
              (progress feedback is on by default)

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
                     (default: on, macOS only; an unset -sandbox is
                     silently disabled when -exec-prefix is also given)
  -sandbox-write     with -sandbox, an extra writable path (repeatable)
  -sandbox-deny-network  with -sandbox, also block all network access
  -sandbox-profile   with -sandbox, a custom .sb profile to use instead
  -exec-prefix       wrap the shell in an arbitrary command, e.g.
                     "ssh testbox" (mutually exclusive with -sandbox)
  -junit <path>      write the run's report(s) as a JUnit XML document
  -trace <dir>       write a self-contained replayable trace bundle
                     (per-turn screen snapshots, manifest, report JSON)
  -golden <dir>      compare each step's final screen against a baseline
                     in this directory (does not affect pass/fail or
                     exit code — a complement to the model's own verdict)
  -golden-update     with -golden, write/overwrite baselines instead of
                     comparing against them

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
	quiet := fs.Bool("quiet", false, "suppress the default spinner/progress output on stderr (progress is on by default)")
	stepTimeout := fs.Duration("step-timeout", 0, "per-step wall-clock budget (e.g. 90s); 0 = no limit")
	commandWait := fs.Int("command-wait-ms", 0, "default wait after a command when the model omits wait_ms (0 = built-in 1500)")
	continueOnFail := fs.Bool("continue-on-fail", false, "attempt every step even after one fails")
	maxRetries := fs.Int("max-retries", agent.DefaultRetry().MaxAttempts, "attempts per SLM request before the run aborts (1 disables retrying)")
	requestTimeout := fs.Duration("request-timeout", agent.DefaultRequestTimeout, "timeout for a single model request; raise it for slow or CPU-only models")
	nativeTools := fs.Bool("native-tools", false, "experimental: use OpenAI tools/tool_calls instead of the prose JSON schema (see docs/model-runs.md)")
	temperature := fs.Float64("temperature", agent.DefaultTemperature, "sampling temperature sent on every request; no universal right value, test both extremes per model (see docs/model-runs.md)")
	execPrefix := fs.String("exec-prefix", "", `wrap the shell in an arbitrary command, e.g. "ssh testbox"`)
	useSandbox := fs.Bool("sandbox", cliops.DefaultSandboxEnabled(runtime.GOOS), "confine the shell with macOS Seatbelt: writes limited to scratch dirs (default: on, macOS only)")
	denyNetwork := fs.Bool("sandbox-deny-network", false, "with -sandbox, also block all network access")
	sandboxProfile := fs.String("sandbox-profile", "", "with -sandbox, use this .sb profile instead of the generated one")
	var writable stringList
	fs.Var(&writable, "sandbox-write", "with -sandbox, an extra writable path (repeatable)")
	var driverOptions stringList
	fs.Var(&driverOptions, "driver-option", `driver-specific option as key=value (repeatable), e.g. -driver-option url=file:///path/to/page.html; overrides the same key from spec frontmatter`)
	var tags stringList
	fs.Var(&tags, "tag", `with a Feature-style spec (see internal/spec/feature.go), only run Scenarios carrying this tag (repeatable — a scenario must carry every listed tag); ignored for an ordinary spec file`)
	junitPath := fs.String("junit", "", "write the run's report(s) as a JUnit XML document to this path")
	tracePath := fs.String("trace", "", "write a self-contained replayable trace bundle (screen snapshots, manifest, report JSON) to this directory")
	goldenDir := fs.String("golden", "", "compare each step's final screen against a baseline in this directory (does not affect pass/fail or exit code)")
	goldenUpdate := fs.Bool("golden-update", false, "with -golden, write/overwrite baselines instead of comparing against them")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	sandboxExplicitlySet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "sandbox" {
			sandboxExplicitlySet = true
		}
	})

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

	var progressFn func(runner.ProgressEvent)
	if !*quiet {
		progressFn = newProgressPrinter(os.Stderr, isTerminal(os.Stderr)).handle
	}

	sandboxEnabled := resolveSandbox(*useSandbox, sandboxExplicitlySet, len(prefix) > 0)

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
			Enabled:       sandboxEnabled,
			WritablePaths: writable,
			DenyNetwork:   *denyNetwork,
			ProfilePath:   *sandboxProfile,
		},
		Verbose:      logFn,
		Progress:     progressFn,
		JUnitPath:    *junitPath,
		TracePath:    *tracePath,
		GoldenDir:    *goldenDir,
		GoldenUpdate: *goldenUpdate,
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
		if err := encodeJSONWithGolden(result.Report, result.Golden); err != nil {
			return err
		}
	} else {
		printReport(result.Report)
	}
	printGoldenResults(os.Stderr, result.Golden)

	if !result.Report.Passed {
		os.Exit(1)
	}
	return nil
}

// encodeJSONWithGolden prints report as JSON, plus a sibling "golden" key
// when golden results were computed — a thin wrapper, not a change to
// runner.Report.MarshalJSON itself, since golden comparison is a cliops-
// level concern, not part of the runner's own report contract. See
// docs/roadmap-reporting-and-agents.md, Phase D.
func encodeJSONWithGolden(report *runner.Report, golden []cliops.GoldenResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if len(golden) == 0 {
		return enc.Encode(report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	var merged map[string]any
	if err := json.Unmarshal(raw, &merged); err != nil {
		return err
	}
	merged["golden"] = golden
	return enc.Encode(merged)
}

// printGoldenResults prints one line per golden-file comparison result to
// w, in the same style as the existing progress lines (✓/✗/+). Golden
// results never affect the process exit code — see cliops.GoldenResult's
// own doc comment.
func printGoldenResults(w io.Writer, golden []cliops.GoldenResult) {
	for _, g := range golden {
		switch g.Status {
		case "match":
			fmt.Fprintf(w, "✓ golden step %d matches\n", g.StepIndex)
		case "mismatch":
			fmt.Fprintf(w, "✗ golden step %d mismatch: %s\n", g.StepIndex, g.DiffPreview)
		case "missing":
			fmt.Fprintf(w, "  golden step %d: no baseline yet (run -golden-update to create one)\n", g.StepIndex)
		case "updated":
			fmt.Fprintf(w, "+ golden step %d baseline created\n", g.StepIndex)
		}
	}
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
			Feature   string                `json:"feature"`
			Passed    bool                  `json:"passed"`
			Scenarios []*runner.Report      `json:"scenarios"`
			Golden    []cliops.GoldenResult `json:"golden,omitempty"`
		}
		out := featureJSON{Feature: result.Feature.Name, Passed: result.Passed, Golden: result.Golden}
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
	printGoldenResults(os.Stderr, result.Golden)

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

// resolveSandbox decides the effective sandbox-enabled value from the
// parsed -sandbox flag, whether it was explicitly passed (vs. left at
// its OS-aware default), and whether -exec-prefix was also given.
//
// An explicit -exec-prefix silently disables an *unset* sandbox default
// — sandboxing was never asked for on this run, and the two flags are
// mutually exclusive anyway (cliops.runLoadedTest still refuses the case
// where both are explicitly requested; that check is unchanged). But an
// explicitly-passed -sandbox always wins, so `-sandbox -exec-prefix ...`
// still surfaces that existing mutual-exclusion error rather than being
// silently overridden.
func resolveSandbox(flagValue, explicitlySet, hasExecPrefix bool) bool {
	if hasExecPrefix && !explicitlySet {
		return false
	}
	return flagValue
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
		// A ground-truth check that disagreed with the model is the single
		// most important line in this report when it happens: it means the
		// model's own verdict could not be trusted. Say so explicitly
		// rather than leaving it to whoever reads the JSON.
		for _, a := range s.Assertions {
			switch {
			case a.Err != "":
				fmt.Printf("        ground-truth check could not run: %s\n", a.Err)
			case !a.AgreedWithModel:
				fmt.Printf("        ground-truth check DISAGREED with the model (%s)\n", a.Command)
			}
		}
	}
	if r.Passed {
		fmt.Println("RESULT: PASS")
	} else {
		fmt.Println("RESULT: FAIL")
	}
}
