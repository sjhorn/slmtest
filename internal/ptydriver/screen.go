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
	term   vt10x.Terminal
	filter csiFilter
}

// newScreenModel constructs a screenModel sized to match the PTY it will
// track. cols/rows must stay in sync with the real PTY size — see
// Driver.Resize, which resizes both together.
func newScreenModel(cols, rows int) *screenModel {
	return &screenModel{term: vt10x.New(vt10x.WithSize(cols, rows))}
}

// write feeds PTY bytes to the emulator, after csiFilter strips CSI
// sequences vt10x mishandles — see csiFilter's doc comment for why this
// is necessary, not cosmetic. vt10x.Terminal.Write locks its own state
// internally, so this does not (and must not) wrap it in an additional
// Lock/Unlock — the mutex vt10x uses is not reentrant.
func (s *screenModel) write(p []byte) {
	if len(p) == 0 {
		return
	}
	if filtered := s.filter.apply(p); len(filtered) > 0 {
		_, _ = s.term.Write(filtered)
	}
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

// csiFilter strips CSI (Control Sequence Introducer, `ESC [`) sequences
// carrying a '>', '=', or '<' private-marker prefix before bytes reach
// vt10x.
//
// This exists because of a real bug found running examples/tui-claude-
// chat-test.md against a real model: Claude Code's TUI opens with (among
// other things) `\x1b[>1u` — a Kitty keyboard protocol capability query,
// entirely standard among modern terminal apps, and a no-op on any
// terminal that doesn't understand it. vt10x's CSI parser
// (csiEscape.parse) only strips a leading '?' before parsing parameters;
// it does not know about '>', '=', or '<'. So for `\x1b[>1u`, it fails to
// parse ">1" as a number, ends up with zero parsed args, but still
// dispatches on the final byte 'u' — which vt10x maps to DECRC (restore
// cursor position), a legacy ANSI.SYS sequence that shares no relation to
// the Kitty protocol's use of the same final byte. Since no `ESC [s`
// (save cursor) was ever sent, the "restored" position is vt10x's own
// init-time default: (0,0). The cursor silently teleports to the
// top-left, and everything the TUI draws next overwrites/interleaves with
// whatever was already on screen there — exactly the garbled, word-
// interleaved "Current screen contents" text observed live. Reproduced
// deterministically offline (no model needed): write "hello world\r\n",
// write "\x1b[>1u", write more text — the more text lands on row 0,
// overwriting "hello world", instead of on row 1 where a real terminal
// (which just ignores this query) would put it. `<` is included for the
// same reason: it's the Kitty protocol's paired "pop keyboard protocol
// flags" marker (`CSI < u`), observed in the same real session, hitting
// the identical 'u' → DECRC mismapping.
//
// The fix lives here rather than in vt10x (a small external dependency
// this project doesn't otherwise need to patch) or in the diff buffer
// (which is unaffected and was never wrong) — dropping '>'/'='/'<'-
// prefixed CSI sequences entirely is safe for this screen model's
// purpose: they're capability negotiation/query sequences, never
// something a human reading the screen needs reflected in what's
// visible.
//
// This is a small stateful scanner, not a per-call regex, because a CSI
// sequence can be split across two write() calls (pump() reads in 4096-
// byte chunks) — state carries the "am I mid-escape-sequence" position
// across calls the same way vt10x's own parser does internally.
type csiFilter struct {
	pending []byte // bytes of a CSI sequence seen so far, not yet resolved
}

// CSI final bytes are 0x40-0x7E (ECMA-48); parameter/intermediate bytes
// are 0x20-0x3F. See csiEscape.put in vt10x for the equivalent boundary.
func isCSIFinalByte(b byte) bool { return b >= 0x40 && b <= 0x7E }

// apply scans p and returns a (possibly shorter) copy with any '>'/'='-
// prefixed CSI sequence removed — including one whose start or end fell
// in a previous/future call, tracked via pending. Bytes not part of such
// a sequence are passed through unchanged and in order.
func (f *csiFilter) apply(p []byte) []byte {
	out := make([]byte, 0, len(p))
	i := 0
	for i < len(p) {
		if len(f.pending) == 0 {
			// Look for the start of a CSI sequence: ESC '['.
			if p[i] == 0x1b && i+1 < len(p) && p[i+1] == '[' {
				f.pending = append(f.pending, p[i], p[i+1])
				i += 2
				continue
			}
			// A lone trailing ESC with no more bytes yet in this chunk:
			// hold it back in case '[' arrives in the next write() call.
			if p[i] == 0x1b && i+1 == len(p) {
				f.pending = append(f.pending, p[i])
				i++
				continue
			}
			out = append(out, p[i])
			i++
			continue
		}

		// We're mid-CSI-sequence (pending starts with at least ESC).
		if len(f.pending) == 1 {
			// pending is a lone ESC carried over from the previous call;
			// this byte decides whether it's actually CSI.
			if p[i] != '[' {
				out = append(out, f.pending...)
				out = append(out, p[i])
				f.pending = f.pending[:0]
				i++
				continue
			}
			f.pending = append(f.pending, p[i])
			i++
			continue
		}

		// pending is "ESC [" plus whatever parameter bytes have arrived
		// so far. Keep consuming until the final byte.
		b := p[i]
		f.pending = append(f.pending, b)
		i++
		if isCSIFinalByte(b) {
			marker := f.pending[2]
			if marker != '>' && marker != '=' && marker != '<' {
				out = append(out, f.pending...)
			}
			// else: drop the whole sequence — this is the fix.
			f.pending = f.pending[:0]
		}
	}
	return out
}
