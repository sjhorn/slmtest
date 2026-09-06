package cliops

import (
	"fmt"
	"os"

	"github.com/sjhorn/slmtest/internal/runner"
)

// writeJUnitFile renders reports as one JUnit XML document (one
// <testsuite> per Report) and writes it to path. Used by both Run (a
// single-report slice) and RunFeature (one entry per scenario), so a
// Feature's whole run lands in one JUnit file, one suite per scenario.
func writeJUnitFile(path string, reports []*runner.Report) error {
	out, err := runner.MarshalJUnit(reports...)
	if err != nil {
		return fmt.Errorf("marshaling JUnit XML: %w", err)
	}
	if err := os.WriteFile(path, out, 0644); err != nil {
		return fmt.Errorf("writing JUnit XML to %s: %w", path, err)
	}
	return nil
}
