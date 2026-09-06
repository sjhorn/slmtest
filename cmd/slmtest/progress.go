package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/runner"
)

// spinnerFrames cycles while a turn is in flight. Plain ASCII — no
// assumption the terminal supports Unicode braille spinners.
var spinnerFrames = []rune{'|', '/', '-', '\\'}

// spinnerInterval is how often the in-place spinner line redraws.
const spinnerInterval = 250 * time.Millisecond

// progressPrinter renders runner.ProgressEvent as default-on progress
// feedback on stderr — never stdout, so -json's machine-readable report
// is unaffected regardless of whether progress is active.
//
// Constructed with explicit io.Writer/nowFunc/ticker-source so it's
// testable without a real terminal or real elapsed time.
type progressPrinter struct {
	w         io.Writer
	now       func() time.Time
	newTicker func(time.Duration) *time.Ticker
	isTTY     bool

	// spinning/stop track a live spinner goroutine across
	// ProgressTurnStart..ProgressTurnDone, so ProgressTurnDone (or a
	// step ending mid-turn) can cleanly stop it and clear the line.
	stop      chan struct{}
	done      chan struct{}
	lineWidth int
}

// isTerminal reports whether f is attached to a real terminal (not a
// pipe or redirected file) — stdlib-only, no new dependency.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func newProgressPrinter(w io.Writer, isTTY bool) *progressPrinter {
	return &progressPrinter{
		w:         w,
		now:       time.Now,
		newTicker: time.NewTicker,
		isTTY:     isTTY,
	}
}

// handle is the runner.Options.OnProgress-shaped method.
func (p *progressPrinter) handle(ev runner.ProgressEvent) {
	switch ev.Kind {
	case runner.ProgressStepStart:
		fmt.Fprintf(p.w, "→ step %d: %s\n", ev.StepIndex, ev.StepTitle)
	case runner.ProgressTurnStart:
		p.startSpinner(ev)
	case runner.ProgressTurnDone:
		p.stopSpinner()
	case runner.ProgressStepDone:
		p.stopSpinner()
		if ev.Aborted {
			fmt.Fprintf(p.w, "✗ step %d ABORTED: %s\n", ev.StepIndex, ev.Reason)
			return
		}
		if ev.Result == agent.ResultPass {
			fmt.Fprintf(p.w, "✓ step %d passed\n", ev.StepIndex)
		} else {
			fmt.Fprintf(p.w, "✗ step %d FAILED: %s\n", ev.StepIndex, ev.Reason)
		}
	}
}

// startSpinner begins an in-place, ticking spinner line. A no-op when
// not attached to a TTY — piped/redirected stderr never sees a \r, and
// (matching -verbose's own step-boundary-only shape) gets no per-turn
// output at all.
func (p *progressPrinter) startSpinner(ev runner.ProgressEvent) {
	if !p.isTTY {
		return
	}
	p.stopSpinner() // defensive: clear any stray prior spinner first

	stop := make(chan struct{})
	done := make(chan struct{})
	p.stop = stop
	p.done = done

	start := p.now()
	ticker := p.newTicker(spinnerInterval)
	go func() {
		defer close(done)
		defer ticker.Stop()
		frame := 0
		draw := func() {
			elapsed := p.now().Sub(start).Round(time.Second)
			line := fmt.Sprintf("  %c waiting on model (turn %d/%d, %s)", spinnerFrames[frame%len(spinnerFrames)], ev.Turn, ev.MaxTurns, elapsed)
			p.clearLine()
			fmt.Fprint(p.w, "\r"+line)
			p.lineWidth = len(line)
			frame++
		}
		draw()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				draw()
			}
		}
	}()
}

// stopSpinner stops any live spinner goroutine and clears its line. Safe
// to call when no spinner is running.
func (p *progressPrinter) stopSpinner() {
	if p.stop == nil {
		return
	}
	close(p.stop)
	<-p.done
	p.clearLine()
	p.stop, p.done = nil, nil
}

// clearLine overwrites the current in-place spinner line with spaces and
// returns the cursor to column 0, so the next fmt.Fprint doesn't leave
// stray trailing characters from a longer previous frame.
func (p *progressPrinter) clearLine() {
	if p.lineWidth == 0 {
		return
	}
	fmt.Fprint(p.w, "\r"+strings.Repeat(" ", p.lineWidth)+"\r")
	p.lineWidth = 0
}
