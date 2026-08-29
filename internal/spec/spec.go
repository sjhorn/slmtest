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
	// Term sets TERM in the shell's environment. Only matters when the
	// test drives something that inspects it (a TUI, a pager, anything
	// that colorizes); empty inherits the parent environment's value.
	Term string
	// Size is the default terminal size for the whole test. A zero Size
	// means the driver's built-in default.
	Size  Size
	Steps []Step
}

// Size is a terminal size in rows and columns. Zero means "unspecified" —
// callers fall back to the test default, then to the driver's.
type Size struct {
	Rows int
	Cols int
}

// IsZero reports whether no size was specified.
func (s Size) IsZero() bool { return s.Rows == 0 && s.Cols == 0 }

// Step is a single numbered step the agent must complete before moving on.
type Step struct {
	Index  int
	Title  string
	Goal   string
	Hint   string
	Expect string
	// Size overrides the test's terminal size for this step only, for a
	// step that drives something which reflows (a TUI, a wide table).
	// Zero means "inherit the test's size".
	Size Size
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
		case "term":
			t.Term = v
		case "size":
			sz, err := ParseSize(v)
			if err != nil {
				return nil, fmt.Errorf("frontmatter size: %w", err)
			}
			t.Size = sz
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
			if v, ok := fieldValue(line, "Goal:"); ok {
				cur.Goal = v
			} else if v, ok := fieldValue(line, "Hint:"); ok {
				cur.Hint = v
			} else if v, ok := fieldValue(line, "Expect:"); ok {
				cur.Expect = v
			} else if v, ok := fieldValue(line, "Size:"); ok {
				sz, err := ParseSize(v)
				if err != nil {
					return fmt.Errorf("step %d (%q) Size: %w", cur.Index, cur.Title, err)
				}
				cur.Size = sz
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

// fieldValue matches a "Goal:"/"Hint:"/"Expect:" label at the start of a
// line and returns the text after it. Markdown emphasis around the label
// is tolerated ("**Goal:** ...", "_Hint:_ ...") because both humans and
// small models reach for it by habit — and the emphasis is stripped from
// the returned value too, so the model is never shown a Goal that reads
// "**Goal:** nginx is installed".
//
// Only the emphasis run that opened the label is removed from the value,
// so content that legitimately starts with punctuation (a backtick-quoted
// command, say) survives intact.
func fieldValue(line, label string) (string, bool) {
	emphasis := "*_"
	stripped := strings.TrimLeft(line, emphasis+" ")
	if !strings.HasPrefix(stripped, label) {
		return "", false
	}
	opener := strings.Trim(line[:len(line)-len(stripped)], " ")
	value := strings.TrimPrefix(stripped, label)
	if opener != "" {
		value = strings.TrimPrefix(strings.TrimLeft(value, " "), opener)
	}
	return strings.TrimSpace(value), true
}

// ParseSize reads a "ROWSxCOLS" terminal size, e.g. "24x80". Rows come
// first, matching stty and pty.Winsize rather than the WIDTHxHEIGHT
// convention of image tooling — the ordering is easy to get backwards, so
// the format doc calls it out too.
func ParseSize(v string) (Size, error) {
	rowsStr, colsStr, ok := strings.Cut(strings.TrimSpace(v), "x")
	if !ok {
		return Size{}, fmt.Errorf("expected ROWSxCOLS (e.g. 24x80), got %q", v)
	}
	rows, err := strconv.Atoi(strings.TrimSpace(rowsStr))
	if err != nil {
		return Size{}, fmt.Errorf("rows in %q: %w", v, err)
	}
	cols, err := strconv.Atoi(strings.TrimSpace(colsStr))
	if err != nil {
		return Size{}, fmt.Errorf("columns in %q: %w", v, err)
	}
	// Bounds are the PTY's: Winsize fields are uint16, and a zero
	// dimension would silently mean "unspecified" further down.
	if rows <= 0 || cols <= 0 {
		return Size{}, fmt.Errorf("rows and columns must both be positive, got %q", v)
	}
	if rows > 65535 || cols > 65535 {
		return Size{}, fmt.Errorf("rows and columns must each fit in 16 bits, got %q", v)
	}
	return Size{Rows: rows, Cols: cols}, nil
}
