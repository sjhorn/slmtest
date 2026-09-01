package ptydriver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sjhorn/slmtest/internal/driver"
)

// This file adapts Driver to the driver.Driver interface. It is purely
// additive: every method here is built on top of the existing
// Start/RunCommand/WaitAndSnapshot/Resize/Alive/Close methods above,
// none of which change behavior. See internal/driver's doc comment for
// the interface this satisfies.
var _ driver.Driver = (*Driver)(nil)
var _ driver.Resizable = (*Driver)(nil)

func init() {
	driver.Register("tui", New)
}

// Bespoke, TUI-owned action types. run_command and send_keys stay
// first-class rather than being decomposed into shared primitives:
// shell-command-then-Enter is central enough to a terminal driver to
// keep its own verb, and raw control-byte injection (send_keys) has no
// shared-primitive equivalent.
const (
	ActionRunCommand driver.ActionType = "run_command"
	ActionSendKeys   driver.ActionType = "send_keys"
)

// RunCommandParams is run_command's Dispatch param shape.
type RunCommandParams struct {
	Command string `json:"command"`
	WaitMS  int    `json:"wait_ms,omitempty"`
}

// SendKeysParams is send_keys's Dispatch param shape.
type SendKeysParams struct {
	Command    string `json:"command"`
	PressEnter bool   `json:"press_enter,omitempty"`
	WaitMS     int    `json:"wait_ms,omitempty"`
}

const defaultWaitMS = 1500

// withScreen appends the persistent screen model's current contents to a
// diff-based observation, when there is anything meaningful to show. This
// is the fix for the documented "content vanishes before the model acts on
// it" bug class (CLAUDE.md's "Known gaps"): the diff-based out is left
// exactly as-is (it's still the right answer to "what's new"), and the
// screen block adds "what's on screen right now" alongside it,
// independent of whether it's new. Deliberately unconditional (no
// deduping against out) — simplicity beats a heuristic that could itself
// hide content again, which is exactly how the original bugs happened.
func withScreen(out, screen string) string {
	if screen == "" {
		return out
	}
	return out + "\n\nCurrent screen contents:\n" + screen
}

// New is this driver's driver.Factory, registered under "tui". cfg.Argv
// is the resolved launch command (shell, possibly wrapped in a sandbox
// or exec prefix); cfg.Env is the shell's environment.
func New(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
	return Start(cfg.Argv, cfg.Env)
}

// Name identifies this driver in spec frontmatter and the -driver flag.
func (d *Driver) Name() string { return "tui" }

// Actions lists run_command and send_keys (bespoke) plus
// driver.PrimitivePressKey — offering the shared press_key primitive
// removes the need for the model to know raw escape-sequence bytes
// itself (Escape, arrow keys) that it previously had to carry in its
// own head; see pressKeyBytes for the translation.
func (d *Driver) Actions() []driver.ActionSpec {
	return []driver.ActionSpec{
		{
			Type: ActionRunCommand,
			Description: "Type a shell command and ALWAYS press Enter, then wait and report new terminal output. " +
				"\"command\" may be \"\" to press Enter alone — e.g. to confirm a highlighted default option in a " +
				"menu, or submit text already sitting in the terminal's input line.",
			ParamSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "Shell text to type. May be empty to press Enter alone."},
					"wait_ms": {"type": "integer", "description": "How long to wait before reporting new output. Defaults to 1500."}
				},
				"required": ["command"]
			}`),
		},
		{
			Type: ActionSendKeys,
			Description: "Type text WITHOUT pressing Enter by default — for partial input, control characters " +
				"(e.g. \"\\u0003\" for Ctrl-C), or interactive prompts. Set \"press_enter\": true to also send a " +
				"newline; with press_enter true, \"command\" may be \"\" to press Enter alone.",
			ParamSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string"},
					"press_enter": {"type": "boolean", "description": "Defaults to false. If true, a newline is sent after command."},
					"wait_ms": {"type": "integer", "description": "How long to wait before reporting new output. Defaults to 1500."}
				},
				"required": ["command"]
			}`),
		},
		driver.PrimitivePressKey,
	}
}

// PromptFragment carries the PTY-specific rules the core prompt doesn't
// know about: run_command vs. send_keys semantics, and the stranded-
// input warning send_keys needs (see notExecutedNote in
// internal/runner).
func (d *Driver) PromptFragment() string {
	return `- run_command: types the command and ALWAYS presses Enter, waits, then you'll be shown new terminal output. "command" may be "" to press Enter alone.
- send_keys: types the command WITHOUT pressing Enter by default — use for partial input, control characters (e.g. "\u0003" for Ctrl-C), or interactive prompts. If it doesn't press Enter, that text is now sitting in the terminal's input line, not yet run.
- press_key: sends a named logical key (enter, escape, tab, backspace, delete, up, down, left, right, back, select, a function key, or a plain character), optionally with modifiers (ctrl, alt, shift, meta), without you needing to know its raw byte sequence.
- If a command you ran produced no new output at all, it did not run. Use run_command (not send_keys) to execute something.`
}

// Dispatch executes one action against the PTY. See RunCommand's own
// doc comment for the Enter-key byte-translation rationale this builds on.
func (d *Driver) Dispatch(ctx context.Context, action driver.ActionType, params json.RawMessage) (driver.Observation, error) {
	switch action {
	case ActionRunCommand:
		var p RunCommandParams
		if err := json.Unmarshal(params, &p); err != nil {
			return driver.Observation{}, fmt.Errorf("run_command: bad params: %w", err)
		}
		wait := waitDuration(p.WaitMS)
		out, err := d.RunCommand(ctx, p.Command, true, wait)
		if err != nil {
			return driver.Observation{}, err
		}
		return driver.Observation{Text: withScreen(out, d.CurrentScreen())}, nil

	case ActionSendKeys:
		var p SendKeysParams
		if err := json.Unmarshal(params, &p); err != nil {
			return driver.Observation{}, fmt.Errorf("send_keys: bad params: %w", err)
		}
		wait := waitDuration(p.WaitMS)
		out, err := d.RunCommand(ctx, p.Command, p.PressEnter, wait)
		if err != nil {
			return driver.Observation{}, err
		}
		return driver.Observation{Text: withScreen(out, d.CurrentScreen())}, nil

	case driver.ActionPressKey:
		var p driver.PressKeyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return driver.Observation{}, fmt.Errorf("press_key: bad params: %w", err)
		}
		bytes, err := pressKeyBytes(p.Key, p.Modifiers)
		if err != nil {
			// An unknown/missing key name is a recoverable mistake, not
			// a broken driver — wrap it so the runner feeds it back for
			// a retry instead of aborting the run. See
			// driver.BadParamsError's doc comment.
			return driver.Observation{}, driver.NewBadParamsError(d.Name(), driver.ActionPressKey, err.Error())
		}
		if err := d.Write(bytes); err != nil {
			return driver.Observation{}, err
		}
		out, err := d.WaitAndSnapshot(ctx, waitDuration(0))
		if err != nil {
			return driver.Observation{}, err
		}
		return driver.Observation{Text: withScreen(out, d.CurrentScreen())}, nil

	default:
		return driver.Observation{}, driver.NewUnsupportedActionError(d.Name(), action)
	}
}

// Observe is the core "wait" action: no terminal input, just wait and
// report whatever new output has appeared.
func (d *Driver) Observe(ctx context.Context, wait time.Duration) (driver.Observation, error) {
	if wait <= 0 {
		wait = 2000 * time.Millisecond
	}
	out, err := d.WaitAndSnapshot(ctx, wait)
	if err != nil {
		return driver.Observation{}, err
	}
	return driver.Observation{Text: withScreen(out, d.CurrentScreen())}, nil
}

func waitDuration(waitMS int) time.Duration {
	if waitMS <= 0 {
		waitMS = defaultWaitMS
	}
	return time.Duration(waitMS) * time.Millisecond
}

// pressKeyBytes translates a logical key name (plus optional modifiers)
// into the actual bytes a real terminal would send. Verified empirically
// against Claude Code's own TUI (see CLAUDE.md, "Known gaps"): Enter needs
// a literal \r (not \n) for a raw-mode TUI, and menu navigation needs a
// real arrow-key escape sequence, not a digit.
func pressKeyBytes(key string, modifiers []string) (string, error) {
	base, err := namedKeyBytes(key)
	if err != nil {
		return "", err
	}
	return applyModifiers(base, key, modifiers)
}

// namedKeyBytes translates the bare key name, ignoring modifiers.
func namedKeyBytes(key string) (string, error) {
	switch k := strings.ToLower(strings.TrimSpace(key)); k {
	case "enter", "return":
		return "\r", nil
	case "escape", "esc":
		return "\x1b", nil
	case "up":
		return "\x1b[A", nil
	case "down":
		return "\x1b[B", nil
	case "right":
		return "\x1b[C", nil
	case "left":
		return "\x1b[D", nil
	case "back":
		// No universal "back" key in a terminal; Escape is the closest
		// general-purpose equivalent (cancels a menu/prompt).
		return "\x1b", nil
	case "select":
		return "\r", nil
	case "tab":
		return "\t", nil
	case "backspace":
		return "\x7f", nil
	case "space":
		return " ", nil
	case "delete":
		return "\x1b[3~", nil
	case "insert":
		return "\x1b[2~", nil
	case "home":
		return "\x1b[H", nil
	case "end":
		return "\x1b[F", nil
	case "pageup":
		return "\x1b[5~", nil
	case "pagedown":
		return "\x1b[6~", nil
	case "f1", "f2", "f3", "f4":
		// Standard xterm SS3 sequences.
		codes := map[string]string{"f1": "P", "f2": "Q", "f3": "R", "f4": "S"}
		return "\x1bO" + codes[k], nil
	case "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12":
		codes := map[string]string{
			"f5": "15", "f6": "17", "f7": "18", "f8": "19",
			"f9": "20", "f10": "21", "f11": "23", "f12": "24",
		}
		return "\x1b[" + codes[k] + "~", nil
	default:
		if utf8.RuneCountInString(key) == 1 {
			// A single printable character passes through as itself.
			return key, nil
		}
		return "", fmt.Errorf("press_key: unknown key %q (known: enter, escape, tab, backspace, delete, insert, home, end, pageup, pagedown, space, up, down, left, right, back, select, f1-f12, or a single character)", key)
	}
}

// applyModifiers encodes ctrl/alt/shift/meta on top of a base key's bytes.
// Only the combinations a real terminal can actually express are
// supported; an unsupported combination is a recoverable error, not a
// silent no-op.
func applyModifiers(base, key string, modifiers []string) (string, error) {
	var ctrl, alt, shift, meta bool
	for _, m := range modifiers {
		switch strings.ToLower(strings.TrimSpace(m)) {
		case "ctrl", "control":
			ctrl = true
		case "alt", "option":
			alt = true
		case "shift":
			shift = true
		case "meta", "cmd", "command", "super":
			meta = true
		case "":
			// ignore
		default:
			return "", fmt.Errorf("press_key: unknown modifier %q (known: ctrl, alt, shift, meta)", m)
		}
	}
	if meta {
		// No portable terminal encoding for a bare Meta/Cmd chord.
		return "", fmt.Errorf("press_key: modifier \"meta\" is not supported by a terminal")
	}

	out := base
	if ctrl {
		k := strings.ToLower(strings.TrimSpace(key))
		if len(k) != 1 || k[0] < 'a' || k[0] > 'z' {
			return "", fmt.Errorf("press_key: modifier \"ctrl\" is only supported with a single letter key, got %q", key)
		}
		out = string(rune(k[0] & 0x1f))
	}
	if alt {
		// Alt is conventionally encoded as an ESC prefix before the
		// key's own bytes.
		out = "\x1b" + out
	}
	if shift {
		k := strings.ToLower(strings.TrimSpace(key))
		switch k {
		case "tab":
			// Special-cased: xterm's Shift-Tab (back-tab) sequence.
			out = "\x1b[Z"
		default:
			if utf8.RuneCountInString(key) == 1 {
				// A plain shift+<letter> is just the uppercase letter —
				// no separate encoding needed.
				out = strings.ToUpper(key)
			}
			// Shift on a named non-letter key (e.g. shift+up) has no
			// universal terminal encoding; left as the base key's bytes.
		}
	}
	return out, nil
}
