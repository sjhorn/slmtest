package runner

import "github.com/sjhorn/slmtest/internal/agent"

// ProgressKind identifies what happened in a ProgressEvent. This is
// separate from Options.Verbose (see its doc comment) — Verbose's exact
// string shapes are already load-bearing for cmd/slmtest-mcp's prefix
// matching, and it's opt-in by design, while progress feedback is
// opt-out (on by default, suppressed with -quiet).
type ProgressKind int

const (
	ProgressStepStart ProgressKind = iota
	ProgressStepDone
	ProgressTurnStart
	ProgressTurnDone
)

// ProgressEvent is delivered to Options.OnProgress as a run proceeds.
// Turn/MaxTurns are only meaningful for ProgressTurnStart/ProgressTurnDone.
type ProgressEvent struct {
	Kind      ProgressKind
	StepIndex int
	StepTitle string
	Turn      int
	MaxTurns  int

	// Result/Reason/Aborted are only populated for ProgressStepDone —
	// they mirror the fields printReport/log already surface for a
	// finished step, so a progress renderer can print the same verdict
	// without needing to see the full StepOutcome.
	Result  agent.StepResult
	Reason  string
	Aborted bool
}
