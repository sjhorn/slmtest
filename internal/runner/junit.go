package runner

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// This file renders one or more Reports as a JUnit XML document — the
// de-facto CI ingestion format (GitHub Actions test-report annotations,
// GitLab, Jenkins, etc all understand it), so a slmtest run can slot into
// existing CI dashboards without a bespoke parser. See
// docs/roadmap-reporting-and-agents.md, Phase B.
//
// Hand-rolled encoding/xml-tagged structs, mirroring this file's own
// jsonReport/jsonStep/jsonTurn convention: an explicit wire-shape struct,
// not whatever the domain Report/StepOutcome happen to look like.

type junitTestsuites struct {
	XMLName  xml.Name         `xml:"testsuites"`
	Tests    int              `xml:"tests,attr"`
	Failures int              `xml:"failures,attr"`
	Errors   int              `xml:"errors,attr"`
	Time     string           `xml:"time,attr"`
	Suites   []junitTestsuite `xml:"testsuite"`
}

type junitTestsuite struct {
	Name     string          `xml:"name,attr"`
	Tests    int             `xml:"tests,attr"`
	Failures int             `xml:"failures,attr"`
	Errors   int             `xml:"errors,attr"`
	Time     string          `xml:"time,attr"`
	Cases    []junitTestcase `xml:"testcase"`
}

type junitTestcase struct {
	Classname string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	Error     *junitError   `xml:"error,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

type junitError struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// MarshalJUnit renders one <testsuite> per Report (one per Test/Scenario)
// and one <testcase> per step, as a single <testsuites> document.
//
// Status mapping mirrors StepOutcome.Status()'s own documented meaning
// (see CLAUDE.md, "The -json report shape"): StatusFail is a <failure>
// (the system under test didn't do what was expected); StatusTimeout and
// StatusAbort are both <error> (the harness gave up or the environment
// broke — neither says the system under test failed), distinguished only
// by their message text.
func MarshalJUnit(reports ...*Report) ([]byte, error) {
	doc := junitTestsuites{}
	var totalTime float64
	for _, r := range reports {
		suite := junitTestsuite{
			Time: fmt.Sprintf("%.3f", r.Duration.Seconds()),
		}
		if r.Test != nil {
			suite.Name = r.Test.Name
		}
		totalTime += r.Duration.Seconds()
		for _, s := range r.Steps {
			tc := junitTestcase{
				Classname: suite.Name,
				Name:      s.Step.Title,
				Time:      fmt.Sprintf("%.3f", s.Duration.Seconds()),
				SystemOut: junitSystemOut(s),
			}
			doc.Tests++
			suite.Tests++
			switch s.Status() {
			case StatusFail:
				tc.Failure = &junitFailure{Message: s.Reason}
				doc.Failures++
				suite.Failures++
			case StatusTimeout:
				tc.Error = &junitError{Message: "step timed out: " + s.Reason}
				doc.Errors++
				suite.Errors++
			case StatusAbort:
				tc.Error = &junitError{Message: s.Reason}
				doc.Errors++
				suite.Errors++
			}
			suite.Cases = append(suite.Cases, tc)
		}
		doc.Suites = append(doc.Suites, suite)
	}
	doc.Time = fmt.Sprintf("%.3f", totalTime)

	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling JUnit XML: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

// junitSystemOut renders a compact per-turn summary (action + truncated
// PTYOutput) for <system-out> — enough to see what happened without
// bloating the file with the full transcript already available in the
// -json report / trace bundle.
func junitSystemOut(s StepOutcome) string {
	if len(s.Transcript) == 0 {
		return ""
	}
	var b strings.Builder
	for i, tl := range s.Transcript {
		action := string(tl.Action.Action)
		if action == "" {
			action = "(unparsed reply)"
		}
		out := tl.PTYOutput
		const maxLen = 500
		if len(out) > maxLen {
			out = out[:maxLen] + "...(truncated)"
		}
		fmt.Fprintf(&b, "turn %d: %s\n", i+1, action)
		if out != "" {
			fmt.Fprintf(&b, "  %s\n", strings.ReplaceAll(out, "\n", "\n  "))
		}
		if tl.Err != "" {
			fmt.Fprintf(&b, "  error: %s\n", tl.Err)
		}
	}
	return b.String()
}
