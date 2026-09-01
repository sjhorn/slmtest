package ptydriver

import (
	"context"
	"strings"
	"testing"
	"time"
)

// These tests exercise the persistent screen model (screen.go) against a
// real shell in a real PTY, matching the rest of this package's testing
// philosophy — the interesting behavior lives in how real ANSI/cursor
// sequences get interpreted, which a mock can't exercise honestly.

// startScreenTestDriver is like startTestDriver but sets TERM so
// clear/tput behave the way a real interactive terminal would.
func startScreenTestDriver(t *testing.T) *Driver {
	t.Helper()
	d, err := Start([]string{"/bin/sh"}, append(minimalTestEnv(), "TERM=xterm"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	_, _ = d.WaitAndSnapshot(context.Background(), 300*time.Millisecond)
	return d
}

func minimalTestEnv() []string {
	// PATH is all this needs; inherit nothing else so TERM above is
	// unambiguous.
	return []string{"PATH=/usr/bin:/bin"}
}

func TestCurrentScreenReflectsCursorPositionedContent(t *testing.T) {
	d := startScreenTestDriver(t)

	_, err := d.RunCommand(context.Background(), "clear; tput cup 5 10; printf hello-at-cursor", true, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}

	screen := d.CurrentScreen()
	if !strings.Contains(screen, "hello-at-cursor") {
		t.Fatalf("CurrentScreen() = %q, want it to contain the cursor-positioned text", screen)
	}

	lines := strings.Split(screen, "\n")
	// Row 5 (0-indexed) is the 6th line.
	if len(lines) < 6 || !strings.Contains(lines[5], "hello-at-cursor") {
		t.Errorf("expected the text on row 5 (line 6), got lines: %q", lines)
	}
}

// This is the regression test for the documented bug class: a raw-mode TUI
// can leave meaningful content on screen without re-emitting bytes, and
// the consuming diff (SinceLastSnapshot) only shows it once. CurrentScreen
// must still show it on a later call even after the diff has been drained.
func TestCurrentScreenSurvivesConsumingDiffBeingDrained(t *testing.T) {
	d := startScreenTestDriver(t)

	_, err := d.RunCommand(context.Background(), "clear; printf still-here-later", true, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}

	// Drain the consuming diff buffer — this is what a turn that doesn't
	// act on the output would still trigger via any subsequent Observe.
	drained := d.SinceLastSnapshot()
	if strings.Contains(drained, "still-here-later") {
		t.Fatalf("expected SinceLastSnapshot to have already been consumed by RunCommand, but found the marker again: %q", drained)
	}

	// Wait and take another snapshot, simulating a later turn where
	// nothing new was written.
	empty, err := d.WaitAndSnapshot(context.Background(), 200*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitAndSnapshot: %v", err)
	}
	if strings.Contains(empty, "still-here-later") {
		t.Fatalf("diff unexpectedly replayed old content: %q", empty)
	}

	screen := d.CurrentScreen()
	if !strings.Contains(screen, "still-here-later") {
		t.Errorf("CurrentScreen() = %q, want it to still show content the diff already consumed", screen)
	}
}

func TestCurrentScreenTracksResize(t *testing.T) {
	d := startScreenTestDriver(t)

	if err := d.Resize(10, 30); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if _, err := d.RunCommand(context.Background(), "clear; printf resized", true, 500*time.Millisecond); err != nil {
		t.Fatalf("RunCommand: %v", err)
	}

	// Must not panic, and the rendered screen must not exceed the new
	// geometry.
	screen := d.CurrentScreen()
	lines := strings.Split(screen, "\n")
	if len(lines) > 10 {
		t.Errorf("CurrentScreen() has %d lines after resizing to 10 rows: %q", len(lines), screen)
	}
	for i, line := range lines {
		if len(line) > 30 {
			t.Errorf("line %d is %d cols wide after resizing to 30 cols: %q", i, len(line), line)
		}
	}
	if !strings.Contains(screen, "resized") {
		t.Errorf("CurrentScreen() = %q, want it to contain content written after resize", screen)
	}
}

func TestScreenModelRendersEmptyWhenNothingWritten(t *testing.T) {
	s := newScreenModel(DefaultCols, DefaultRows)
	if got := s.render(); got != "" {
		t.Errorf("render() = %q on an unwritten screen model, want empty", got)
	}
}
