package ptydriver

import (
	"fmt"
	"strings"

	"github.com/hinshun/vt10x"
)

// screenModel maintains a persistent view of "what's on screen" by feeding
// every PTY byte through a real VT100 emulator (github.com/hinshun/vt10x),
// alongside — not instead of — Driver's own consuming byte-diff buffer.
//
// This exists because SinceLastSnapshot's diff is a one-shot view: a
// raw-mode TUI can leave meaningful content on screen indefinitely without
// re-emitting bytes (no cursor movement, no redraw), and a model that
// doesn't act on it the turn it arrives never sees it again. A real
// terminal emulator tracks cursor position and a persistent grid of cells,
// so render() always reflects current screen contents regardless of
// whether anything "new" was written since the last call. See CLAUDE.md's
// "Known gaps" (now resolved) for the two documented real-model failures
// this fixes.
type screenModel struct {
	term vt10x.Terminal
}

// newScreenModel constructs a screenModel sized to match the PTY it will
// track. cols/rows must stay in sync with the real PTY size — see
// Driver.Resize, which resizes both together.
func newScreenModel(cols, rows int) *screenModel {
	return &screenModel{term: vt10x.New(vt10x.WithSize(cols, rows))}
}

// write feeds PTY bytes to the emulator. vt10x.Terminal.Write locks its own
// state internally, so this does not (and must not) wrap it in an
// additional Lock/Unlock — the mutex vt10x uses is not reentrant.
func (s *screenModel) write(p []byte) {
	if len(p) == 0 {
		return
	}
	_, _ = s.term.Write(p)
}

// resize matches the emulator's geometry to the real PTY's. Like write,
// vt10x.Terminal.Resize locks internally.
func (s *screenModel) resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	s.term.Resize(cols, rows)
}

// render returns the terminal's current visible contents: every cell,
// trailing whitespace trimmed per line, trailing blank lines dropped, with
// a cursor-position marker appended only when the cursor is visible.
// Returns "" when there is nothing meaningful to show (a blank screen, or
// one that hasn't received any output yet).
//
// Lock/Unlock (not String(), which locks internally itself and would
// deadlock if called while already holding the lock) is used here so the
// cursor position and cell grid are read as one atomic snapshot rather
// than two separately-locked reads that could observe different moments.
func (s *screenModel) render() string {
	s.term.Lock()
	defer s.term.Unlock()

	cols, rows := s.term.Size()
	lines := make([]string, 0, rows)
	for y := 0; y < rows; y++ {
		var b strings.Builder
		for x := 0; x < cols; x++ {
			c := s.term.Cell(x, y).Char
			if c == 0 {
				c = ' '
			}
			b.WriteRune(c)
		}
		lines = append(lines, strings.TrimRight(b.String(), " \t"))
	}

	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}

	out := strings.Join(lines, "\n")
	if s.term.CursorVisible() {
		cur := s.term.Cursor()
		out += fmt.Sprintf("\n[cursor: row %d, col %d]", cur.Y+1, cur.X+1)
	}
	return out
}
