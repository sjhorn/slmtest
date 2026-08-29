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
	"time"

	"github.com/example/slmtest/internal/agent"
	"github.com/example/slmtest/internal/runner"
	"github.com/example/slmtest/internal/spec"
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
  slmtest validate <file.md>           parse-check a test spec, no execution
  slmtest init <file.md>               write a starter test spec

Run flags:
  -endpoint   OpenAI-compatible base URL (default http://localhost:8080/v1)
  -model      model name to send in requests (default "local-slm")
  -api-key    bearer token, if the endpoint requires one
  -shell      shell to launch in the PTY (default /bin/bash, overrides spec)
  -json       print the final report as JSON instead of human-readable text
  -verbose    print each turn (prompt/reply/pty output) as it happens

  -step-timeout      per-step wall-clock budget (e.g. 90s); 0 = no limit
  -command-wait-ms   default wait after a command when the model omits wait_ms
`)
}

func cmdRun(args []string) error {
	filePath, rest, err := takeLeadingPositional(args)
	if err != nil {
		return fmt.Errorf("usage: slmtest run <file.md> [flags]: %w", err)
	}

	fs := flag.NewFlagSet("run", flag.ExitOnError)
	endpoint := fs.String("endpoint", "http://localhost:8080/v1", "OpenAI-compatible base URL")
	model := fs.String("model", "local-slm", "model name")
	apiKey := fs.String("api-key", "", "bearer token")
	shell := fs.String("shell", "", "shell override")
	asJSON := fs.Bool("json", false, "print JSON report")
	verbose := fs.Bool("verbose", false, "print each turn")
	stepTimeout := fs.Duration("step-timeout", 0, "per-step wall-clock budget (e.g. 90s); 0 = no limit")
	commandWait := fs.Int("command-wait-ms", 0, "default wait after a command when the model omits wait_ms (0 = built-in 1500)")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	t, err := loadSpec(filePath)
	if err != nil {
		return err
	}

	client := agent.NewClient(*endpoint, *model, *apiKey)

	var logFn func(string, ...any)
	if *verbose {
		logFn = func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if t.TimeoutSeconds > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(t.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	report, err := runner.Run(ctx, t, client, runner.Options{
		Shell:         *shell,
		StepTimeout:   *stepTimeout,
		CommandWaitMS: *commandWait,
		Verbose:       logFn,
	})
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		printReport(report)
	}

	if !report.Passed {
		os.Exit(1)
	}
	return nil
}

func cmdValidate(args []string) error {
	filePath, rest, err := takeLeadingPositional(args)
	if err != nil || len(rest) != 0 {
		return fmt.Errorf("usage: slmtest validate <file.md>")
	}
	t, err := loadSpec(filePath)
	if err != nil {
		return err
	}
	fmt.Printf("OK: %q — %d step(s)\n", t.Name, len(t.Steps))
	for _, s := range t.Steps {
		fmt.Printf("  step %d: %s\n", s.Index, s.Title)
	}
	return nil
}

func cmdInit(args []string) error {
	path, rest, err := takeLeadingPositional(args)
	if err != nil || len(rest) != 0 {
		return fmt.Errorf("usage: slmtest init <file.md>")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	return os.WriteFile(path, []byte(starterTemplate), 0644)
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

func loadSpec(path string) (*spec.Test, error) {
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

const starterTemplate = `---
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
