package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/runner"
)

// newTestPrinter builds a progressPrinter with an injected clock and a
// ticker whose period is long enough that no test relies on a real tick
// firing — every assertion here only needs the spinner's *first* draw
// (on ProgressTurnStart) and the clean stop/clear (on
// ProgressTurnDone/ProgressStepDone), never a second frame, so there's
// no need for a real time.Sleep-driven interval.
func newTestPrinter(w *bytes.Buffer, isTTY bool, now func() time.Time) *progressPrinter {
	p := newProgressPrinter(w, isTTY)
	p.now = now
	p.newTicker = func(time.Duration) *time.Ticker { return time.NewTicker(time.Hour) }
	return p
}

func TestProgressPrinterStepBoundaryLinesAlwaysPresent(t *testing.T) {
	for _, isTTY := range []bool{true, false} {
		var buf bytes.Buffer
		p := newTestPrinter(&buf, isTTY, time.Now)

		p.handle(runner.ProgressEvent{Kind: runner.ProgressStepStart, StepIndex: 1, StepTitle: "install nginx"})
		p.handle(runner.ProgressEvent{Kind: runner.ProgressStepDone, StepIndex: 1, StepTitle: "install nginx", Result: agent.ResultPass, Reason: "saw the version string"})
		p.handle(runner.ProgressEvent{Kind: runner.ProgressStepStart, StepIndex: 2, StepTitle: "start nginx"})
		p.handle(runner.ProgressEvent{Kind: runner.ProgressStepDone, StepIndex: 2, StepTitle: "start nginx", Result: agent.ResultFail, Reason: "port 80 refused"})

		got := buf.String()
		for _, want := range []string{
			"→ step 1: install nginx",
			"✓ step 1 passed",
			"→ step 2: start nginx",
			"✗ step 2 FAILED: port 80 refused",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("isTTY=%v: output missing %q, got:\n%s", isTTY, want, got)
			}
		}
	}
}

func TestProgressPrinterAbortedStep(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPrinter(&buf, false, time.Now)
	p.handle(runner.ProgressEvent{Kind: runner.ProgressStepStart, StepIndex: 1, StepTitle: "one"})
	p.handle(runner.ProgressEvent{Kind: runner.ProgressStepDone, StepIndex: 1, StepTitle: "one", Aborted: true, Reason: "pty died"})

	if got := buf.String(); !strings.Contains(got, "✗ step 1 ABORTED: pty died") {
		t.Errorf("output = %q, want an ABORTED line", got)
	}
}

func TestProgressPrinterNonTTYEmitsNoSpinnerOutput(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPrinter(&buf, false, time.Now)

	p.handle(runner.ProgressEvent{Kind: runner.ProgressTurnStart, StepIndex: 1, StepTitle: "one", Turn: 1, MaxTurns: 6})
	p.handle(runner.ProgressEvent{Kind: runner.ProgressTurnDone, StepIndex: 1, StepTitle: "one", Turn: 1, MaxTurns: 6})

	got := buf.String()
	if got != "" {
		t.Errorf("non-TTY turn events produced output, want none: %q", got)
	}
	if strings.Contains(got, "\r") {
		t.Errorf("non-TTY output contains \\r, must never happen for a piped/redirected log: %q", got)
	}
}

func TestProgressPrinterTTYSpinnerDrawsAndClears(t *testing.T) {
	var buf bytes.Buffer
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := newTestPrinter(&buf, true, func() time.Time { return base })

	p.handle(runner.ProgressEvent{Kind: runner.ProgressTurnStart, StepIndex: 1, StepTitle: "one", Turn: 2, MaxTurns: 6})
	// give the spinner goroutine's first draw a moment to land
	time.Sleep(20 * time.Millisecond)
	p.handle(runner.ProgressEvent{Kind: runner.ProgressTurnDone, StepIndex: 1, StepTitle: "one", Turn: 2, MaxTurns: 6})

	got := buf.String()
	if !strings.Contains(got, "turn 2/6") {
		t.Errorf("spinner output missing turn count: %q", got)
	}
	if !strings.Contains(got, "\r") {
		t.Errorf("TTY spinner output should use \\r for in-place redraw: %q", got)
	}
	// After stopSpinner, lineWidth resets and no live goroutine remains.
	if p.lineWidth != 0 {
		t.Errorf("lineWidth = %d after stop, want 0", p.lineWidth)
	}
	if p.stop != nil {
		t.Errorf("stop channel still set after stopSpinner")
	}
}

func TestProgressPrinterStepDoneClearsLiveSpinner(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPrinter(&buf, true, time.Now)

	p.handle(runner.ProgressEvent{Kind: runner.ProgressTurnStart, StepIndex: 1, StepTitle: "one", Turn: 1, MaxTurns: 6})
	time.Sleep(20 * time.Millisecond)
	// Step finishing mid-turn (e.g. a timeout) should still clean up the
	// spinner goroutine without needing an explicit ProgressTurnDone.
	p.handle(runner.ProgressEvent{Kind: runner.ProgressStepDone, StepIndex: 1, StepTitle: "one", Result: agent.ResultFail, Reason: "timed out"})

	if p.stop != nil {
		t.Errorf("spinner not stopped by ProgressStepDone")
	}
	if !strings.Contains(buf.String(), "✗ step 1 FAILED: timed out") {
		t.Errorf("output = %q, want the FAILED line", buf.String())
	}
}
