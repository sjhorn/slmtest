// Package spec parses the markdown test-spec format into a structured Test.
//
// File format (see examples/nginx-test.md):
//
//	---
//	name: nginx-smoke-test
//	description: Verify nginx installs, starts, and serves the default page
//	shell: /bin/bash
//	timeout_seconds: 180
//	max_turns_per_step: 8
//	---
//
//	## Step 1: Install nginx
//	Goal: nginx is installed and the binary is on PATH.
//	Hint: apt-get update && apt-get install -y nginx
//	Expect: `nginx -v` exits 0 and prints a version string.
//
//	## Step 2: Start nginx
//	Goal: the nginx service is running and listening on port 80.
//	Hint: service nginx start
//	Expect: curl to localhost:80 returns HTTP 200.
//
// "Hint" is a suggestion, not a script — the agent is free to reason its
// way to a different command if the hint doesn't work (retry, install a
// missing dependency, use a different flag, etc). "Goal" and "Expect" are
// what the agent is ultimately graded against for that step.
package spec

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// Test is a fully parsed markdown test-spec file.
type Test struct {
	Name            string
	Description     string
	Shell           string // e.g. /bin/bash. Defaults to /bin/sh.
	TimeoutSeconds  int    // whole-test wall-clock budget. 0 = no limit.
	MaxTurnsPerStep int    // per-step reasoning-turn budget. 0 = default (6).
	Steps           []Step
}

// Step is a single numbered step the agent must complete before moving on.
type Step struct {
	Index  int
	Title  string
	Goal   string
	Hint   string
	Expect string
}

// Parse reads a markdown test-spec document and returns the structured Test.
func Parse(md string) (*Test, error) {
	fm, body, err := splitFrontmatter(md)
	if err != nil {
		return nil, err
	}

	t := &Test{
		Shell:           "/bin/sh",
		MaxTurnsPerStep: 6,
	}
	for k, v := range fm {
		switch k {
		case "name":
			t.Name = v
		case "description":
			t.Description = v
		case "shell":
			t.Shell = v
		case "timeout_seconds":
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("frontmatter timeout_seconds: %w", err)
			}
			t.TimeoutSeconds = n
		case "max_turns_per_step":
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("frontmatter max_turns_per_step: %w", err)
			}
			t.MaxTurnsPerStep = n
		}
	}
	if t.Name == "" {
		return nil, fmt.Errorf("frontmatter missing required field: name")
	}

	steps, err := parseSteps(body)
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("no steps found (expected '## Step N: ...' headings)")
	}
	t.Steps = steps
	return t, nil
}

// splitFrontmatter extracts a leading "---\nkey: value\n...\n---" block.
// Values are treated as plain strings (no nested YAML) — this keeps the
// parser dependency-free and the format easy for both humans and small
// models to write correctly.
func splitFrontmatter(md string) (map[string]string, string, error) {
	lines := strings.Split(md, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, "", fmt.Errorf("markdown must start with a '---' frontmatter block")
	}
	fm := map[string]string{}
	i := 1
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			i++
			break
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, "", fmt.Errorf("frontmatter line %d: expected 'key: value', got %q", i+1, line)
		}
		fm[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	body := strings.Join(lines[i:], "\n")
	return fm, body, nil
}

// parseSteps scans the markdown body for "## Step N: Title" sections and
// pulls Goal / Hint / Expect fields out of each section's body.
func parseSteps(body string) ([]Step, error) {
	var steps []Step
	var cur *Step
	var buf strings.Builder

	flush := func() error {
		if cur == nil {
			return nil
		}
		for _, line := range strings.Split(buf.String(), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			switch {
			case hasFieldPrefix(line, "Goal:"):
				cur.Goal = strings.TrimSpace(strings.TrimPrefix(line, "Goal:"))
			case hasFieldPrefix(line, "Hint:"):
				cur.Hint = strings.TrimSpace(strings.TrimPrefix(line, "Hint:"))
			case hasFieldPrefix(line, "Expect:"):
				cur.Expect = strings.TrimSpace(strings.TrimPrefix(line, "Expect:"))
			}
		}
		if cur.Goal == "" {
			return fmt.Errorf("step %d (%q) is missing a Goal: line", cur.Index, cur.Title)
		}
		if cur.Expect == "" {
			return fmt.Errorf("step %d (%q) is missing an Expect: line", cur.Index, cur.Title)
		}
		steps = append(steps, *cur)
		return nil
	}

	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	idx := 0
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## Step") {
			if err := flush(); err != nil {
				return nil, err
			}
			idx++
			title := strings.TrimSpace(strings.TrimPrefix(trimmed, "##"))
			// title looks like "Step 1: Install nginx"
			_, after, _ := strings.Cut(title, ":")
			cur = &Step{Index: idx, Title: strings.TrimSpace(after)}
			buf.Reset()
			continue
		}
		if cur != nil {
			buf.WriteString(line)
			buf.WriteString("\n")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return steps, nil
}

func hasFieldPrefix(line, prefix string) bool {
	// tolerate leading markdown bold/backtick noise like "**Goal:**"
	stripped := strings.TrimLeft(line, "*_ ")
	return strings.HasPrefix(stripped, prefix) || strings.HasPrefix(line, prefix)
}
