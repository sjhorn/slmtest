package cliops

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"
)

// junitDoc mirrors just enough of runner's own junitTestsuites shape to
// parse the written file back and check testcase count/names — proving
// the cliops wiring (not just runner.MarshalJUnit in isolation) produces
// a real, parseable JUnit file end-to-end via a nulldriver + fake-SLM run.
type junitDoc struct {
	XMLName xml.Name `xml:"testsuites"`
	Suites  []struct {
		Name  string `xml:"name,attr"`
		Cases []struct {
			Name string `xml:"name,attr"`
		} `xml:"testcase"`
	} `xml:"testsuite"`
}

func TestRunWritesJUnitFile(t *testing.T) {
	endpoint := scriptedSLM(t,
		`{"action":"finish_step","step_result":"pass","reason":"done"}`,
	)
	path := filepath.Join(t.TempDir(), "out.junit.xml")
	_, err := Run(context.Background(), RunParams{
		SpecPath:   echoTestSpecPath,
		Endpoint:   endpoint,
		DriverName: "null",
		JUnitPath:  path,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written JUnit file: %v", err)
	}
	var doc junitDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing written JUnit XML: %v", err)
	}
	if len(doc.Suites) != 1 {
		t.Fatalf("len(Suites) = %d, want 1", len(doc.Suites))
	}
	if len(doc.Suites[0].Cases) != 1 {
		t.Fatalf("len(Cases) = %d, want 1", len(doc.Suites[0].Cases))
	}
}
