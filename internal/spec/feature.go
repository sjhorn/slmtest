// This file adds an optional, purely-additive BDD/Gherkin-flavored layer
// on top of the plain Test/Step format spec.go already parses: a
// "## Background" section of steps shared by every scenario, and one or
// more "## Scenario: <name>" (or "## Scenario Outline: <name>" with an
// "### Examples" data table) sections — the same structural role
// Cucumber's Feature/Background/Scenario/Scenario Outline play, expressed
// in this project's existing markdown dialect rather than a second,
// Gherkin-specific parser.
//
// This is deliberately additive, not a rewrite: a markdown file that
// doesn't use any "## Background"/"## Scenario:"/"## Scenario Outline:"
// heading is unaffected — ParseFeature falls back to Parse and wraps the
// single resulting Test as a one-scenario Feature, so every existing spec
// file, and every existing caller of Parse, keeps working completely
// unchanged. See CLAUDE.md's BDD-format investigation writeup for why
// this shape was chosen over a second parser.
package spec

import (
	"fmt"
	"strconv"
	"strings"
)

// Feature is a markdown file containing one or more named, independent
// Scenarios that optionally share a Background. Everything here mirrors
// Test's own frontmatter fields — a Feature IS a Test's frontmatter, plus
// this additional Background/Scenario structure instead of Test's flat
// Steps list.
type Feature struct {
	Name            string
	Description     string
	Shell           string
	TimeoutSeconds  int
	MaxTurnsPerStep int
	Term            string
	Size            Size
	Driver          string
	DriverOptions   map[string]string

	// Background steps are prepended fresh to every Scenario when
	// expanded — never carried as live driver state between scenarios,
	// exactly like Cucumber's own Background semantics (each Scenario is
	// isolated; only the Background's steps, not any state a prior
	// Scenario's run left behind, are shared).
	Background []Step
	Scenarios  []Scenario
}

// Scenario is one named, independent step sequence within a Feature. Tags
// are collected but not yet interpreted anywhere (no run-time selection
// exists yet) — see CLAUDE.md's BDD-format investigation for the
// planned-but-not-implemented tag-based selection this sets up for.
type Scenario struct {
	Name  string
	Tags  []string
	Steps []Step
	// Outline holds a Scenario Outline's Examples table — nil for an
	// ordinary Scenario. When non-nil, Steps is the template (containing
	// "<placeholder>" tokens) rather than directly runnable; see Expand.
	Outline *ExamplesTable
}

// ExamplesTable is a Scenario Outline's data table: one Test is produced
// per data row, with every "<header>" placeholder in that row's Scenario
// template substituted by the row's value for that column.
type ExamplesTable struct {
	Headers []string
	Rows    [][]string
}

// ParseFeature parses md as a Feature. If the file uses none of
// "## Background"/"## Scenario:"/"## Scenario Outline:", it degrades to
// calling Parse and wraps the resulting Test as a single implicit,
// unnamed Scenario — this is what makes ParseFeature safe to call on
// every existing spec file unconditionally, not just ones written in the
// new style.
func ParseFeature(md string) (*Feature, error) {
	fm, body, err := splitFrontmatter(md)
	if err != nil {
		return nil, err
	}
	if !looksLikeFeature(body) {
		t, err := Parse(md)
		if err != nil {
			return nil, err
		}
		return &Feature{
			Name: t.Name, Description: t.Description, Shell: t.Shell,
			TimeoutSeconds: t.TimeoutSeconds, MaxTurnsPerStep: t.MaxTurnsPerStep,
			Term: t.Term, Size: t.Size, Driver: t.Driver, DriverOptions: t.DriverOptions,
			Scenarios: []Scenario{{Steps: t.Steps}},
		}, nil
	}

	f := &Feature{Shell: "/bin/sh", MaxTurnsPerStep: 6}
	if err := applyFrontmatter(f, fm); err != nil {
		return nil, err
	}

	tagsByHeading, body := extractTags(body)

	for _, sec := range splitSections(body, "##") {
		switch {
		case sec.title == "Background":
			steps, err := parseStepSections(sec.body, "###")
			if err != nil {
				return nil, fmt.Errorf("Background: %w", err)
			}
			f.Background = steps

		case strings.HasPrefix(sec.title, "Scenario Outline:"):
			name := strings.TrimSpace(strings.TrimPrefix(sec.title, "Scenario Outline:"))
			scenario, err := parseScenarioOutline(name, sec.body)
			if err != nil {
				return nil, err
			}
			scenario.Tags = tagsByHeading[sec.title]
			f.Scenarios = append(f.Scenarios, *scenario)

		case strings.HasPrefix(sec.title, "Scenario:"):
			name := strings.TrimSpace(strings.TrimPrefix(sec.title, "Scenario:"))
			steps, err := parseStepSections(sec.body, "###")
			if err != nil {
				return nil, fmt.Errorf("Scenario %q: %w", name, err)
			}
			f.Scenarios = append(f.Scenarios, Scenario{Name: name, Tags: tagsByHeading[sec.title], Steps: steps})

		default:
			// Any other "##" heading (or none) before the first
			// Background/Scenario is ignored rather than rejected, the
			// same tolerance Test's own frontmatter/body split has for
			// blank lines — keeps the format forgiving of a stray "# Feature:"
			// title heading a human author reaches for out of Gherkin habit.
		}
	}

	if len(f.Scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios found (expected '## Scenario: ...' or '## Scenario Outline: ...' headings)")
	}
	if f.Name == "" {
		return nil, fmt.Errorf("frontmatter missing required field: name")
	}
	if f.Driver == "" {
		f.Driver = "tui"
	}
	return f, nil
}

// LooksLikeFeature reports whether md uses this package's optional
// Feature/Background/Scenario layer at all — the same detection
// ParseFeature uses internally to decide whether to fall back to Parse.
// A caller that needs to choose between running md as a single Test or
// as a Feature before fully parsing (e.g. to decide which of two
// differently-shaped code paths to take) uses this instead of
// duplicating the detection logic itself.
func LooksLikeFeature(md string) (bool, error) {
	_, body, err := splitFrontmatter(md)
	if err != nil {
		return false, err
	}
	return looksLikeFeature(body), nil
}

// extractTags scans body for "@tag1 @tag2 ..." lines that appear
// directly before a "## Scenario:"/"## Scenario Outline:" heading
// (blank lines in between are tolerated, matching how a human writes
// Gherkin tags above a Scenario keyword with a blank line separating
// sections) and returns a map from that heading's exact title text (the
// same string splitSections produces) to its tags, plus body with those
// tag lines removed so they don't otherwise show up as stray content in
// whatever section precedes the Scenario.
func extractTags(body string) (map[string][]string, string) {
	tags := map[string][]string{}
	var pending []string
	var out []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "@"):
			pending = append(pending, strings.Fields(trimmed)...)
			continue // dropped from the body entirely
		case trimmed == "":
			// tolerate a blank line between tags and the heading
		case strings.HasPrefix(trimmed, "## Scenario:") || strings.HasPrefix(trimmed, "## Scenario Outline:"):
			if len(pending) > 0 {
				title := strings.TrimSpace(strings.TrimPrefix(trimmed, "##"))
				tags[title] = pending
			}
			pending = nil
		default:
			pending = nil // tags only attach to the very next heading
		}
		out = append(out, line)
	}
	return tags, strings.Join(out, "\n")
}

// Filter returns a copy of f containing only the Scenarios that carry
// every tag in want (an empty want matches everything, returning f
// unchanged) — the mechanism a run-time "-tag" selection flag builds on,
// the same role Cucumber's own --tags expression plays.
func (f *Feature) Filter(want []string) *Feature {
	if len(want) == 0 {
		return f
	}
	out := *f
	out.Scenarios = nil
	for _, sc := range f.Scenarios {
		if hasAllTags(sc.Tags, want) {
			out.Scenarios = append(out.Scenarios, sc)
		}
	}
	return &out
}

func hasAllTags(have, want []string) bool {
	set := make(map[string]bool, len(have))
	for _, t := range have {
		set[t] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// looksLikeFeature reports whether body contains a "## Background",
// "## Scenario:", or "## Scenario Outline:" heading — the signal
// ParseFeature uses to decide whether a file opts into this layer at
// all.
func looksLikeFeature(body string) bool {
	for _, sec := range splitSections(body, "##") {
		if sec.title == "Background" || strings.HasPrefix(sec.title, "Scenario:") || strings.HasPrefix(sec.title, "Scenario Outline:") {
			return true
		}
	}
	return false
}

// applyFrontmatter is splitFrontmatter's field-by-field switch, factored
// out of Parse so Feature and Test read identical frontmatter without
// duplicating the switch itself.
func applyFrontmatter(f *Feature, fm map[string]string) error {
	for k, v := range fm {
		switch k {
		case "name":
			f.Name = v
		case "description":
			f.Description = v
		case "shell":
			f.Shell = v
		case "timeout_seconds":
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("frontmatter timeout_seconds: %w", err)
			}
			f.TimeoutSeconds = n
		case "max_turns_per_step":
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("frontmatter max_turns_per_step: %w", err)
			}
			f.MaxTurnsPerStep = n
		case "term":
			f.Term = v
		case "size":
			sz, err := ParseSize(v)
			if err != nil {
				return fmt.Errorf("frontmatter size: %w", err)
			}
			f.Size = sz
		case "driver":
			f.Driver = v
		}
	}
	prefix := f.Driver + "_"
	if prefix == "_" {
		prefix = "tui_"
	}
	f.DriverOptions = map[string]string{}
	for k, v := range fm {
		if rest, ok := strings.CutPrefix(k, prefix); ok {
			f.DriverOptions[rest] = v
		}
	}
	return nil
}

// rawSection is one heading-delimited chunk of a markdown body.
type rawSection struct {
	title string // heading text with the marker and leading/trailing space stripped
	body  string
}

// splitSections scans body for lines starting with exactly marker+" "
// (e.g. "## " or "### ") and splits it into the sections those headings
// delimit — the same "">> heading, then body until the next same-level
// heading" shape parseSteps already uses for "## Step", generalized to
// an arbitrary marker level and heading text (not just "Step N: Title").
// Content before the first matching heading is discarded.
func splitSections(body, marker string) []rawSection {
	prefix := marker + " "
	var sections []rawSection
	var cur *rawSection
	var buf strings.Builder

	flush := func() {
		if cur != nil {
			cur.body = buf.String()
			sections = append(sections, *cur)
		}
	}

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			flush()
			title := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			cur = &rawSection{title: title}
			buf.Reset()
			continue
		}
		if cur != nil {
			buf.WriteString(line)
			buf.WriteString("\n")
		}
	}
	flush()
	return sections
}

// parseStepSections parses a Background's or Scenario's body into Steps,
// where each step is a marker-level heading ("### Step N: Title") whose
// body carries the same Goal/Hint/Expect/Size fields "## Step N: Title"
// does in the flat format — reusing fieldValue and ParseSize directly so
// the two formats never drift in what a step body accepts.
func parseStepSections(body, marker string) ([]Step, error) {
	var steps []Step
	idx := 0
	for _, sec := range splitSections(body, marker) {
		if !strings.HasPrefix(sec.title, "Step") {
			continue // e.g. an "Examples" section, handled by the caller
		}
		idx++
		_, after, _ := strings.Cut(sec.title, ":")
		step := Step{Index: idx, Title: strings.TrimSpace(after)}
		for _, line := range strings.Split(sec.body, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if v, ok := fieldValue(line, "Goal:"); ok {
				step.Goal = v
			} else if v, ok := fieldValue(line, "Hint:"); ok {
				step.Hint = v
			} else if v, ok := fieldValue(line, "Expect:"); ok {
				step.Expect = v
			} else if v, ok := fieldValue(line, "Size:"); ok {
				sz, err := ParseSize(v)
				if err != nil {
					return nil, fmt.Errorf("step %d (%q) Size: %w", step.Index, step.Title, err)
				}
				step.Size = sz
			}
		}
		if step.Goal == "" {
			return nil, fmt.Errorf("step %d (%q) is missing a Goal: line", step.Index, step.Title)
		}
		if step.Expect == "" {
			return nil, fmt.Errorf("step %d (%q) is missing an Expect: line", step.Index, step.Title)
		}
		steps = append(steps, step)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("no steps found (expected '%s Step N: ...' headings)", marker)
	}
	return steps, nil
}

// parseScenarioOutline parses a "## Scenario Outline: <name>" section: its
// step template(s) plus its "### Examples" markdown table.
func parseScenarioOutline(name, body string) (*Scenario, error) {
	var steps []Step
	var table *ExamplesTable
	for _, sec := range splitSections(body, "###") {
		switch {
		case strings.HasPrefix(sec.title, "Step"):
			idx := len(steps) + 1
			_, after, _ := strings.Cut(sec.title, ":")
			step := Step{Index: idx, Title: strings.TrimSpace(after)}
			for _, line := range strings.Split(sec.body, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if v, ok := fieldValue(line, "Goal:"); ok {
					step.Goal = v
				} else if v, ok := fieldValue(line, "Hint:"); ok {
					step.Hint = v
				} else if v, ok := fieldValue(line, "Expect:"); ok {
					step.Expect = v
				}
			}
			if step.Goal == "" || step.Expect == "" {
				return nil, fmt.Errorf("scenario outline %q step %d (%q) needs both Goal: and Expect:", name, step.Index, step.Title)
			}
			steps = append(steps, step)

		case sec.title == "Examples":
			t, err := parseExamplesTable(sec.body)
			if err != nil {
				return nil, fmt.Errorf("scenario outline %q: %w", name, err)
			}
			table = t
		}
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("scenario outline %q has no '### Step N: ...' sections", name)
	}
	if table == nil {
		return nil, fmt.Errorf("scenario outline %q has no '### Examples' data table", name)
	}
	return &Scenario{Name: name, Steps: steps, Outline: table}, nil
}

// parseExamplesTable reads a GitHub-flavored-markdown-style pipe table:
// a header row, a "---" separator row, and one or more data rows. This
// is a small, hand-rolled parser rather than a markdown-table library —
// the same "zero exotic dependencies" choice CLAUDE.md documents for
// frontmatter, and the format is simple enough (split on "|", trim) not
// to need one.
func parseExamplesTable(body string) (*ExamplesTable, error) {
	var rows [][]string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i, c := range cells {
			cells[i] = strings.TrimSpace(c)
		}
		if isTableSeparatorRow(cells) {
			continue
		}
		rows = append(rows, cells)
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("expected a header row plus at least one data row in the Examples table")
	}
	headers := rows[0]
	for _, r := range rows[1:] {
		if len(r) != len(headers) {
			return nil, fmt.Errorf("examples row %v has %d cells, want %d (matching the header)", r, len(r), len(headers))
		}
	}
	return &ExamplesTable{Headers: headers, Rows: rows[1:]}, nil
}

// isTableSeparatorRow reports whether every cell is made up only of '-'
// and ':' (markdown's header/body divider row, e.g. "|---|:---:|").
func isTableSeparatorRow(cells []string) bool {
	for _, c := range cells {
		if c == "" {
			return false
		}
		if strings.Trim(c, "-:") != "" {
			return false
		}
	}
	return true
}

// Expand returns one independently-runnable Test per Scenario — or, for
// a Scenario Outline, one Test per Examples row, with every
// "<header>" placeholder in that Scenario's step template substituted by
// the row's value for that column. Background's steps are prepended
// fresh to every expanded Test and the whole sequence renumbered, so
// runner.Run itself never needs to know Feature/Scenario/Background/
// Outline exist — every expanded value is an ordinary *Test, exactly as
// if a human had hand-written one file per scenario.
func (f *Feature) Expand() ([]*Test, error) {
	var tests []*Test
	for _, sc := range f.Scenarios {
		if sc.Outline == nil {
			tests = append(tests, f.buildTest(sc.Name, sc.Steps))
			continue
		}
		for _, row := range sc.Outline.Rows {
			steps, err := substituteRow(sc.Steps, sc.Outline.Headers, row)
			if err != nil {
				return nil, fmt.Errorf("scenario outline %q: %w", sc.Name, err)
			}
			tests = append(tests, f.buildTest(sc.Name, steps))
		}
	}
	return tests, nil
}

// buildTest assembles one expanded Test: Feature's frontmatter, plus
// Background's steps followed by scenarioSteps, renumbered 1..N in that
// order. Name identifies which scenario this run is, the same way a
// human splitting one Feature into several hand-written files would name
// them.
func (f *Feature) buildTest(scenarioName string, scenarioSteps []Step) *Test {
	name := f.Name
	if scenarioName != "" {
		name = f.Name + " — " + scenarioName
	}
	all := make([]Step, 0, len(f.Background)+len(scenarioSteps))
	all = append(all, f.Background...)
	all = append(all, scenarioSteps...)
	for i := range all {
		all[i].Index = i + 1
	}
	return &Test{
		Name: name, Description: f.Description, Shell: f.Shell,
		TimeoutSeconds: f.TimeoutSeconds, MaxTurnsPerStep: f.MaxTurnsPerStep,
		Term: f.Term, Size: f.Size, Driver: f.Driver, DriverOptions: f.DriverOptions,
		Steps: all,
	}
}

// substituteRow returns a copy of steps with every "<header>" placeholder
// in Title/Goal/Hint/Expect replaced by the matching cell in row.
func substituteRow(steps []Step, headers, row []string) ([]Step, error) {
	if len(headers) != len(row) {
		return nil, fmt.Errorf("row %v has %d cells, want %d", row, len(row), len(headers))
	}
	replacer := make([]string, 0, len(headers)*2)
	for i, h := range headers {
		replacer = append(replacer, "<"+h+">", row[i])
	}
	r := strings.NewReplacer(replacer...)
	out := make([]Step, len(steps))
	for i, s := range steps {
		out[i] = Step{
			Index:  s.Index,
			Title:  r.Replace(s.Title),
			Goal:   r.Replace(s.Goal),
			Hint:   r.Replace(s.Hint),
			Expect: r.Replace(s.Expect),
			Size:   s.Size,
		}
	}
	return out, nil
}
