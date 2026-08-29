package spec

import (
	"strings"
	"testing"
)

const goodDoc = `---
name: nginx-smoke-test
description: Verify nginx installs and serves
shell: /bin/bash
timeout_seconds: 300
max_turns_per_step: 8
---

## Step 1: Install nginx
Goal: nginx is installed and on PATH.
Hint: apt-get install -y nginx
Expect: ` + "`nginx -v`" + ` exits 0.

## Step 2: Start the service
Goal: the service is running.
Expect: curl to localhost:80 returns HTTP 200.
`

func TestParseHappyPath(t *testing.T) {
	got, err := Parse(goodDoc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Name != "nginx-smoke-test" {
		t.Errorf("Name = %q, want nginx-smoke-test", got.Name)
	}
	if got.Description != "Verify nginx installs and serves" {
		t.Errorf("Description = %q", got.Description)
	}
	if got.Shell != "/bin/bash" {
		t.Errorf("Shell = %q, want /bin/bash", got.Shell)
	}
	if got.TimeoutSeconds != 300 {
		t.Errorf("TimeoutSeconds = %d, want 300", got.TimeoutSeconds)
	}
	if got.MaxTurnsPerStep != 8 {
		t.Errorf("MaxTurnsPerStep = %d, want 8", got.MaxTurnsPerStep)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(got.Steps))
	}

	s1 := got.Steps[0]
	if s1.Index != 1 || s1.Title != "Install nginx" {
		t.Errorf("step 1 = {%d, %q}, want {1, \"Install nginx\"}", s1.Index, s1.Title)
	}
	if s1.Goal != "nginx is installed and on PATH." {
		t.Errorf("step 1 Goal = %q", s1.Goal)
	}
	if s1.Hint != "apt-get install -y nginx" {
		t.Errorf("step 1 Hint = %q", s1.Hint)
	}
	if s1.Expect != "`nginx -v` exits 0." {
		t.Errorf("step 1 Expect = %q", s1.Expect)
	}

	// Hint is optional; step 2 omits it entirely.
	if got.Steps[1].Hint != "" {
		t.Errorf("step 2 Hint = %q, want empty", got.Steps[1].Hint)
	}
	if got.Steps[1].Index != 2 {
		t.Errorf("step 2 Index = %d, want 2", got.Steps[1].Index)
	}
}

func TestParseAppliesDefaults(t *testing.T) {
	// Only `name` is required; shell and max_turns_per_step should fall
	// back to the documented defaults, and timeout to 0 (= unlimited).
	got, err := Parse(`---
name: minimal
---

## Step 1: Do a thing
Goal: a thing happens.
Expect: the thing is visible in output.
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Shell != "/bin/sh" {
		t.Errorf("Shell = %q, want default /bin/sh", got.Shell)
	}
	if got.MaxTurnsPerStep != 6 {
		t.Errorf("MaxTurnsPerStep = %d, want default 6", got.MaxTurnsPerStep)
	}
	if got.TimeoutSeconds != 0 {
		t.Errorf("TimeoutSeconds = %d, want 0 (unlimited)", got.TimeoutSeconds)
	}
}

// Step indexes come from document order, not from the number the author
// typed in the heading — otherwise a misnumbered spec would produce
// duplicate or gapped indexes in the report.
func TestParseIndexesByPositionNotHeadingNumber(t *testing.T) {
	got, err := Parse(`---
name: misnumbered
---

## Step 5: First
Goal: g
Expect: e

## Step 5: Second
Goal: g
Expect: e
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Steps[0].Index != 1 || got.Steps[1].Index != 2 {
		t.Errorf("indexes = %d, %d; want 1, 2", got.Steps[0].Index, got.Steps[1].Index)
	}
}

// The format doc promises tolerance for markdown emphasis around field
// labels, since both humans and models reach for **Goal:** by habit.
func TestParseToleratesBoldFieldLabels(t *testing.T) {
	got, err := Parse(`---
name: bolded
---

## Step 1: Bold fields
**Goal:** nginx is installed.
**Hint:** apt-get install nginx
**Expect:** version string is printed.
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := got.Steps[0]
	if s.Goal != "nginx is installed." {
		t.Errorf("Goal = %q, want %q", s.Goal, "nginx is installed.")
	}
	if s.Hint != "apt-get install nginx" {
		t.Errorf("Hint = %q, want %q", s.Hint, "apt-get install nginx")
	}
	if s.Expect != "version string is printed." {
		t.Errorf("Expect = %q, want %q", s.Expect, "version string is printed.")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{
			name:    "no frontmatter",
			doc:     "## Step 1: Thing\nGoal: g\nExpect: e\n",
			wantErr: "must start with a '---' frontmatter block",
		},
		{
			name:    "missing name",
			doc:     "---\ndescription: no name here\n---\n\n## Step 1: T\nGoal: g\nExpect: e\n",
			wantErr: "missing required field: name",
		},
		{
			name:    "frontmatter line without colon",
			doc:     "---\nname: x\nthis line has no colon\n---\n\n## Step 1: T\nGoal: g\nExpect: e\n",
			wantErr: "expected 'key: value'",
		},
		{
			name:    "non-numeric timeout",
			doc:     "---\nname: x\ntimeout_seconds: soon\n---\n\n## Step 1: T\nGoal: g\nExpect: e\n",
			wantErr: "timeout_seconds",
		},
		{
			name:    "non-numeric max turns",
			doc:     "---\nname: x\nmax_turns_per_step: lots\n---\n\n## Step 1: T\nGoal: g\nExpect: e\n",
			wantErr: "max_turns_per_step",
		},
		{
			name:    "no steps",
			doc:     "---\nname: x\n---\n\nJust some prose, no step headings.\n",
			wantErr: "no steps found",
		},
		{
			name:    "step missing Goal",
			doc:     "---\nname: x\n---\n\n## Step 1: T\nExpect: e\n",
			wantErr: "missing a Goal",
		},
		{
			name:    "step missing Expect",
			doc:     "---\nname: x\n---\n\n## Step 1: T\nGoal: g\n",
			wantErr: "missing an Expect",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.doc)
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want error containing %q", tc.name, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// A later step's fields must not bleed into an earlier one (or vice
// versa) — each "## Step" heading resets the field buffer.
func TestParseDoesNotLeakFieldsBetweenSteps(t *testing.T) {
	got, err := Parse(`---
name: leaky
---

## Step 1: Has a hint
Goal: g1
Hint: h1
Expect: e1

## Step 2: Has no hint
Goal: g2
Expect: e2
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Steps[1].Hint != "" {
		t.Errorf("step 2 inherited step 1's Hint: %q", got.Steps[1].Hint)
	}
	if got.Steps[0].Goal != "g1" || got.Steps[1].Goal != "g2" {
		t.Errorf("goals = %q, %q; want g1, g2", got.Steps[0].Goal, got.Steps[1].Goal)
	}
}
