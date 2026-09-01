package spec

import (
	"strings"
	"testing"
)

// TestParseFeatureFallsBackToLegacyFormat confirms ParseFeature is safe
// to call unconditionally on every existing spec file — one with no
// Background/Scenario headings expands to exactly one Test identical to
// what Parse itself would produce.
func TestParseFeatureFallsBackToLegacyFormat(t *testing.T) {
	f, err := ParseFeature(goodDoc)
	if err != nil {
		t.Fatalf("ParseFeature: %v", err)
	}
	if len(f.Scenarios) != 1 {
		t.Fatalf("Scenarios = %d, want 1 for a legacy-format file", len(f.Scenarios))
	}
	if f.Scenarios[0].Name != "" {
		t.Errorf("Scenarios[0].Name = %q, want empty (unnamed) for a legacy-format file", f.Scenarios[0].Name)
	}

	tests, err := f.Expand()
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("Expand() = %d tests, want 1", len(tests))
	}
	want, err := Parse(goodDoc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tests[0].Name != want.Name {
		t.Errorf("Name = %q, want %q", tests[0].Name, want.Name)
	}
	if len(tests[0].Steps) != len(want.Steps) {
		t.Fatalf("Steps = %d, want %d", len(tests[0].Steps), len(want.Steps))
	}
	for i := range want.Steps {
		if tests[0].Steps[i] != want.Steps[i] {
			t.Errorf("Steps[%d] = %+v, want %+v", i, tests[0].Steps[i], want.Steps[i])
		}
	}
}

const featureDoc = `---
name: login-flow
description: A login feature with a shared Background and two Scenarios
driver: browser
---

## Background
### Step 1: Given a registered user on the sign-in page
Goal: the sign-in page is loaded with empty fields.
Expect: the page shows a Username field, a Password field, and a Log in button.

## Scenario: Successful login
### Step 1: When they submit the correct username and password
Goal: the login form is submitted with valid credentials.
Hint: type alice / wonderland123 and click Log in.
Expect: the browser navigates to the dashboard.

### Step 2: Then they see a welcome message
Goal: the dashboard shows a personalized welcome.
Expect: the visible text contains "Welcome back, alice!".

## Scenario: Login fails with an incorrect password
### Step 1: When they submit the correct username but the wrong password
Goal: the login form is submitted with a wrong password.
Expect: the visible text contains "Invalid username or password.".
`

func TestParseFeatureBackgroundAndScenarios(t *testing.T) {
	f, err := ParseFeature(featureDoc)
	if err != nil {
		t.Fatalf("ParseFeature: %v", err)
	}
	if f.Name != "login-flow" {
		t.Errorf("Name = %q, want login-flow", f.Name)
	}
	if f.Driver != "browser" {
		t.Errorf("Driver = %q, want browser", f.Driver)
	}
	if len(f.Background) != 1 {
		t.Fatalf("Background = %d steps, want 1", len(f.Background))
	}
	if f.Background[0].Title != "Given a registered user on the sign-in page" {
		t.Errorf("Background[0].Title = %q", f.Background[0].Title)
	}
	if len(f.Scenarios) != 2 {
		t.Fatalf("Scenarios = %d, want 2", len(f.Scenarios))
	}
	if f.Scenarios[0].Name != "Successful login" {
		t.Errorf("Scenarios[0].Name = %q", f.Scenarios[0].Name)
	}
	if len(f.Scenarios[0].Steps) != 2 {
		t.Errorf("Scenarios[0].Steps = %d, want 2", len(f.Scenarios[0].Steps))
	}
	if f.Scenarios[1].Name != "Login fails with an incorrect password" {
		t.Errorf("Scenarios[1].Name = %q", f.Scenarios[1].Name)
	}
}

func TestFeatureExpandPrependsBackgroundAndRenumbers(t *testing.T) {
	f, err := ParseFeature(featureDoc)
	if err != nil {
		t.Fatalf("ParseFeature: %v", err)
	}
	tests, err := f.Expand()
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(tests) != 2 {
		t.Fatalf("Expand() = %d tests, want 2 (one per scenario)", len(tests))
	}

	first := tests[0]
	if first.Name != "login-flow — Successful login" {
		t.Errorf("Name = %q", first.Name)
	}
	if len(first.Steps) != 3 { // 1 Background + 2 scenario steps
		t.Fatalf("Steps = %d, want 3", len(first.Steps))
	}
	if first.Steps[0].Title != "Given a registered user on the sign-in page" {
		t.Errorf("Steps[0] should be the Background step, got %q", first.Steps[0].Title)
	}
	for i, s := range first.Steps {
		if s.Index != i+1 {
			t.Errorf("Steps[%d].Index = %d, want %d (renumbered sequentially)", i, s.Index, i+1)
		}
	}
	// Driver/frontmatter fields propagate to every expanded Test.
	if first.Driver != "browser" {
		t.Errorf("Driver = %q, want browser", first.Driver)
	}

	second := tests[1]
	if second.Name != "login-flow — Login fails with an incorrect password" {
		t.Errorf("Name = %q", second.Name)
	}
	if len(second.Steps) != 2 { // 1 Background + 1 scenario step
		t.Fatalf("Steps = %d, want 2", len(second.Steps))
	}
}

const outlineDoc = `---
name: login-validation
description: Scenario Outline over several invalid login inputs
driver: browser
---

## Scenario Outline: Login rejects invalid input
### Step 1: When they submit "<username>" and "<password>"
Goal: the login form is submitted with the given credentials.
Hint: type "<username>" / "<password>" and click Log in.
Expect: the message area shows "<error>".

### Examples
| username | password      | error                          |
|----------|---------------|---------------------------------|
|          | wonderland123 | Username is required.          |
| alice    |               | Password is required.          |
| alice    | wrongpass     | Invalid username or password.  |
`

func TestParseScenarioOutlineAndExamples(t *testing.T) {
	f, err := ParseFeature(outlineDoc)
	if err != nil {
		t.Fatalf("ParseFeature: %v", err)
	}
	if len(f.Scenarios) != 1 {
		t.Fatalf("Scenarios = %d, want 1", len(f.Scenarios))
	}
	sc := f.Scenarios[0]
	if sc.Name != "Login rejects invalid input" {
		t.Errorf("Name = %q", sc.Name)
	}
	if sc.Outline == nil {
		t.Fatal("Outline = nil, want the parsed Examples table")
	}
	wantHeaders := []string{"username", "password", "error"}
	if len(sc.Outline.Headers) != len(wantHeaders) {
		t.Fatalf("Headers = %v, want %v", sc.Outline.Headers, wantHeaders)
	}
	for i, h := range wantHeaders {
		if sc.Outline.Headers[i] != h {
			t.Errorf("Headers[%d] = %q, want %q", i, sc.Outline.Headers[i], h)
		}
	}
	if len(sc.Outline.Rows) != 3 {
		t.Fatalf("Rows = %d, want 3", len(sc.Outline.Rows))
	}
}

func TestFeatureExpandScenarioOutlineOneTestPerRow(t *testing.T) {
	f, err := ParseFeature(outlineDoc)
	if err != nil {
		t.Fatalf("ParseFeature: %v", err)
	}
	tests, err := f.Expand()
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(tests) != 3 {
		t.Fatalf("Expand() = %d tests, want 3 (one per Examples row)", len(tests))
	}

	cases := []struct {
		wantExpectSubstring string
	}{
		{"Username is required."},
		{"Password is required."},
		{"Invalid username or password."},
	}
	for i, tc := range cases {
		got := tests[i].Steps[0].Expect
		if !strings.Contains(got, tc.wantExpectSubstring) {
			t.Errorf("test %d Expect = %q, want it to contain %q", i, got, tc.wantExpectSubstring)
		}
		if strings.Contains(got, "<error>") {
			t.Errorf("test %d Expect = %q, placeholder was not substituted", i, got)
		}
	}
	// The Hint's two placeholders both get substituted from the same row.
	if hint := tests[0].Steps[0].Hint; !strings.Contains(hint, `""`) {
		t.Errorf("test 0 Hint = %q, want the empty username row's placeholder substituted with an empty string", hint)
	}
}

func TestParseFeatureRequiresGoalAndExpectPerStep(t *testing.T) {
	bad := `---
name: bad-feature
---

## Scenario: Missing Expect
### Step 1: When something happens
Goal: something is true.
`
	if _, err := ParseFeature(bad); err == nil {
		t.Fatal("expected an error for a step missing Expect:")
	}
}

func TestParseFeatureRequiresExamplesForOutline(t *testing.T) {
	bad := `---
name: bad-outline
---

## Scenario Outline: No examples table
### Step 1: When something happens
Goal: something is true.
Expect: something else is true.
`
	if _, err := ParseFeature(bad); err == nil {
		t.Fatal("expected an error for a Scenario Outline with no Examples table")
	}
}

const taggedFeatureDoc = `---
name: tagged-feature
---

@smoke @login
## Scenario: Successful login
### Step 1: When something happens
Goal: something is true.
Expect: something else is true.

@slow
## Scenario: A slower scenario
### Step 1: When something else happens
Goal: something is true.
Expect: something else is true.

## Scenario: An untagged scenario
### Step 1: When a third thing happens
Goal: something is true.
Expect: something else is true.
`

func TestParseFeatureTags(t *testing.T) {
	f, err := ParseFeature(taggedFeatureDoc)
	if err != nil {
		t.Fatalf("ParseFeature: %v", err)
	}
	if len(f.Scenarios) != 3 {
		t.Fatalf("Scenarios = %d, want 3", len(f.Scenarios))
	}
	wantTags := [][]string{{"@smoke", "@login"}, {"@slow"}, nil}
	for i, want := range wantTags {
		got := f.Scenarios[i].Tags
		if len(got) != len(want) {
			t.Errorf("Scenarios[%d].Tags = %v, want %v", i, got, want)
			continue
		}
		for j := range want {
			if got[j] != want[j] {
				t.Errorf("Scenarios[%d].Tags = %v, want %v", i, got, want)
			}
		}
	}
}

func TestFeatureFilterByTag(t *testing.T) {
	f, err := ParseFeature(taggedFeatureDoc)
	if err != nil {
		t.Fatalf("ParseFeature: %v", err)
	}

	smoke := f.Filter([]string{"@smoke"})
	if len(smoke.Scenarios) != 1 || smoke.Scenarios[0].Name != "Successful login" {
		t.Errorf("Filter([@smoke]) = %+v, want just Successful login", smoke.Scenarios)
	}

	both := f.Filter([]string{"@smoke", "@login"})
	if len(both.Scenarios) != 1 {
		t.Errorf("Filter([@smoke,@login]) = %+v, want just the scenario carrying both tags", both.Scenarios)
	}

	none := f.Filter([]string{"@nonexistent"})
	if len(none.Scenarios) != 0 {
		t.Errorf("Filter([@nonexistent]) = %+v, want no scenarios", none.Scenarios)
	}

	unfiltered := f.Filter(nil)
	if len(unfiltered.Scenarios) != 3 {
		t.Errorf("Filter(nil) = %+v, want all 3 scenarios unchanged", unfiltered.Scenarios)
	}
}

func TestParseFeatureRequiresAtLeastOneScenario(t *testing.T) {
	bad := `---
name: no-scenarios
---

## Background
### Step 1: Given something
Goal: something is true.
Expect: something else is true.
`
	if _, err := ParseFeature(bad); err == nil {
		t.Fatal("expected an error for a Feature-style file with a Background but no Scenario")
	}
}
