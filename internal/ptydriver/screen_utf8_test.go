package ptydriver

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A multibyte rune split across two write() calls must survive. pump()
// reads fixed 4096-byte chunks, so where a rune lands in the stream is
// arbitrary; vt10x drops an incomplete trailing sequence rather than
// holding it, which silently removed one character from a captured screen
// while the program under test had written it correctly.
func TestScreenModelRejoinsRuneSplitAcrossWrites(t *testing.T) {
	cases := []struct{ name, text string }{
		{"middot U+00B7 (2 bytes)", "abc·def"},
		{"emdash U+2014 (3 bytes)", "abc—def"},
		{"bullet U+2022 (3 bytes)", "abc•def"},
		{"single guillemet U+203A (3 bytes)", "abc›def"},
		{"o-slash U+00F8 (2 bytes)", "abcødef"},
		{"emoji U+1F600 (4 bytes)", "abc😀def"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := []byte(tc.text)
			for split := 1; split < len(b); split++ {
				s := newScreenModel(80, 24)
				s.write(b[:split])
				s.write(b[split:])
				if got := firstLineOfScreen(s.render()); got != tc.text {
					t.Errorf("split at byte %d: got %q, want %q", split, got, tc.text)
				}
			}
		})
	}
}

// The pathological case: every byte delivered in its own write.
func TestScreenModelRejoinsRuneSplitByteByByte(t *testing.T) {
	const text = "a·b—c•d😀e"
	s := newScreenModel(80, 24)
	for _, b := range []byte(text) {
		s.write([]byte{b})
	}
	if got := firstLineOfScreen(s.render()); got != text {
		t.Fatalf("got %q, want %q", got, text)
	}
}

// Holding back a partial rune must never stall the screen: a malformed
// byte that can never be completed has to reach the emulator, not sit in
// the buffer forever.
func TestScreenModelDoesNotStallOnMalformedBytes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts [][]byte
	}{
		{"lone continuation byte", [][]byte{{0x80}, []byte("ok")}},
		{"invalid lead byte 0xFF", [][]byte{{0xFF}, []byte("ok")}},
		{"truncated rune then ASCII", [][]byte{{0xE2, 0x80}, []byte("ok")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScreenModel(80, 24)
			for _, p := range tc.parts {
				s.write(p)
			}
			if got := s.render(); !strings.Contains(got, "ok") {
				t.Fatalf("later output was swallowed: %q", got)
			}
		})
	}
}

// splitTrailingPartialRune holds back only a genuinely incomplete trailing
// sequence — never a complete rune, ASCII, or an uncompletable byte.
func TestSplitTrailingPartialRune(t *testing.T) {
	cases := []struct {
		name      string
		in        []byte
		wantHeldN int
	}{
		{"pure ASCII", []byte("hello"), 0},
		{"complete 2-byte rune at end", []byte("ab·"), 0},
		{"complete 3-byte rune at end", []byte("ab—"), 0},
		{"complete 4-byte rune at end", []byte("ab😀"), 0},
		{"ASCII after a rune", []byte("·x"), 0},
		{"lead byte of 2-byte rune", []byte{'a', 0xC2}, 1},
		{"lead byte of 3-byte rune", []byte{'a', 0xE2}, 1},
		{"2 of 3 bytes", []byte{'a', 0xE2, 0x80}, 2},
		{"3 of 4 bytes", []byte{'a', 0xF0, 0x9F, 0x98}, 3},
		{"invalid lead byte is not held", []byte{'a', 0xFF}, 0},
		{"orphan continuation is not held", []byte{'a', 0x80}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, hold := splitTrailingPartialRune(tc.in)
			if len(hold) != tc.wantHeldN {
				t.Fatalf("held %d bytes (%x), want %d", len(hold), hold, tc.wantHeldN)
			}
			if len(emit)+len(hold) != len(tc.in) {
				t.Fatalf("bytes lost: emit=%d hold=%d input=%d", len(emit), len(hold), len(tc.in))
			}
		})
	}
}

// End to end through a REAL PTY, with the multibyte rune positioned to
// straddle pump()'s actual 4096-byte read boundary — the condition that
// produced the original report, rather than a hand-split buffer.
func TestScreenSurvivesRuneAtRealChunkBoundary(t *testing.T) {
	d, err := Start([]string{"/bin/sh"}, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Close()
	time.Sleep(300 * time.Millisecond)
	_ = d.Resize(40, 110)

	// Pad so that the "·" lands across a 4096-byte boundary. The prompt and
	// the echoed command also occupy bytes, so sweep a window of offsets:
	// one of them will straddle it, and none of them may lose a character.
	for pad := 4080; pad < 4100; pad++ {
		cmd := "printf '%s\\xc2\\xb7END\\n' \"$(head -c " + itoa(pad) + " < /dev/zero | tr '\\0' x)\""
		if _, err := d.RunCommand(context.Background(), cmd, true, 400*time.Millisecond); err != nil {
			t.Fatalf("run: %v", err)
		}
		screen := d.CurrentScreen()
		if !strings.Contains(screen, "·END") {
			t.Fatalf("pad=%d: the middle dot was dropped from the screen; tail=%q",
				pad, lastRunes(screen, 40))
		}
	}
}

func firstLineOfScreen(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return string(r)
	}
	return string(r[len(r)-n:])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
