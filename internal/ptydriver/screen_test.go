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

// TestScreenModelIgnoresKittyKeyboardProtocolQuery is the regression test
// for a real bug found running examples/tui-claude-chat-test.md against a
// real model: Claude Code's TUI sends "\x1b[>1u" (a Kitty keyboard
// protocol capability query, standard among modern terminal apps and a
// no-op on any terminal that doesn't understand it) on startup. vt10x's
// CSI parser doesn't recognize the '>' private marker, fails to parse the
// parameter, but still dispatches on the final byte 'u' — which it maps
// to DECRC (restore cursor position), silently teleporting the cursor to
// (0,0) and corrupting everything drawn afterward. See csiFilter's doc
// comment in screen.go for the full diagnosis.
func TestScreenModelIgnoresKittyKeyboardProtocolQuery(t *testing.T) {
	s := newScreenModel(80, 24)
	s.write([]byte("hello world\r\n"))
	s.write([]byte("\x1b[>1u")) // capability query — must be a no-op
	s.write([]byte("second line"))

	got := s.render()
	if !strings.Contains(got, "hello world") {
		t.Errorf("render() = %q, want it to still contain \"hello world\" (not overwritten by the cursor teleport)", got)
	}
	if !strings.Contains(got, "second line") {
		t.Errorf("render() = %q, want it to contain \"second line\"", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "hello world") || !strings.Contains(lines[1], "second line") {
		t.Errorf("expected \"hello world\" on row 0 and \"second line\" on row 1 (unaffected by the CSI sequence), got: %q", lines)
	}
}

// TestScreenModelIgnoresKittyKeyboardProtocolPop is the same class of bug
// as TestScreenModelIgnoresKittyKeyboardProtocolQuery but for the Kitty
// protocol's paired "pop keyboard protocol flags" marker (`CSI < u`),
// observed alongside the push query in the same real session.
func TestScreenModelIgnoresKittyKeyboardProtocolPop(t *testing.T) {
	s := newScreenModel(80, 24)
	s.write([]byte("hello world\r\n"))
	s.write([]byte("\x1b[<u")) // pop query — must be a no-op
	s.write([]byte("second line"))

	got := s.render()
	lines := strings.Split(got, "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "hello world") || !strings.Contains(lines[1], "second line") {
		t.Errorf("expected \"hello world\" on row 0 and \"second line\" on row 1, got: %q", lines)
	}
}

// TestCSIFilterHandlesSequenceSplitAcrossWrites confirms the filter's
// state (not just its per-call output) survives a Kitty query split
// across two write() calls — pump() reads the PTY in 4096-byte chunks, so
// a sequence landing on a chunk boundary is a real possibility, not just
// a theoretical one.
func TestCSIFilterHandlesSequenceSplitAcrossWrites(t *testing.T) {
	s := newScreenModel(80, 24)
	s.write([]byte("hello world\r\n"))
	s.write([]byte("\x1b[>1")) // split mid-sequence
	s.write([]byte("u"))       // final byte arrives in the next chunk
	s.write([]byte("still here"))

	got := s.render()
	if !strings.Contains(got, "hello world") || !strings.Contains(got, "still here") {
		t.Errorf("render() = %q, want both lines intact despite the split query", got)
	}
}

// TestCSIFilterPassesThroughOrdinaryCSISequences confirms the filter is
// narrowly scoped to '>'/'='-marker sequences — it must not interfere
// with ordinary CSI sequences (including '?'-marked private-mode ones,
// which vt10x already handles correctly on its own).
func TestCSIFilterPassesThroughOrdinaryCSISequences(t *testing.T) {
	s := newScreenModel(80, 24)
	s.write([]byte("\x1b[?25l"))           // hide cursor — a '?'-marker sequence, must pass through
	s.write([]byte("\x1b[2;5Hpositioned")) // absolute cursor move — must actually move the cursor
	got := s.render()
	lines := strings.Split(got, "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "positioned") {
		t.Errorf("expected \"positioned\" on row 1 (col 4) per the CUP sequence, got: %q", lines)
	}
}
