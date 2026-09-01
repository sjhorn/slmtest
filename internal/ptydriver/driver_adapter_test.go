package ptydriver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sjhorn/slmtest/internal/driver"
)

func TestDriverImplementsInterface(t *testing.T) {
	var _ driver.Driver = (*Driver)(nil)
	var _ driver.Resizable = (*Driver)(nil)
}

func TestPressKeyBytesKnownKeys(t *testing.T) {
	cases := map[string]string{
		"enter":  "\r",
		"Enter":  "\r",
		"escape": "\x1b",
		"esc":    "\x1b",
		"up":     "\x1b[A",
		"down":   "\x1b[B",
		"left":   "\x1b[D",
		"right":  "\x1b[C",
		"select": "\r",
		"back":   "\x1b",
	}
	for key, want := range cases {
		got, err := pressKeyBytes(key, nil)
		if err != nil {
			t.Errorf("pressKeyBytes(%q): unexpected error: %v", key, err)
		}
		if got != want {
			t.Errorf("pressKeyBytes(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestPressKeyBytesUnknown(t *testing.T) {
	if _, err := pressKeyBytes("banana", nil); err == nil {
		t.Fatal("expected an error for an unknown key name")
	}
}

func TestPressKeyBytesNamedKeysAndCharacters(t *testing.T) {
	cases := map[string]string{
		"tab":       "\t",
		"backspace": "\x7f",
		"space":     " ",
		"delete":    "\x1b[3~",
		"insert":    "\x1b[2~",
		"home":      "\x1b[H",
		"end":       "\x1b[F",
		"pageup":    "\x1b[5~",
		"pagedown":  "\x1b[6~",
		"f1":        "\x1bOP",
		"f4":        "\x1bOS",
		"f5":        "\x1b[15~",
		"f12":       "\x1b[24~",
		"a":         "a",
		"Z":         "Z",
	}
	for key, want := range cases {
		got, err := pressKeyBytes(key, nil)
		if err != nil {
			t.Errorf("pressKeyBytes(%q): unexpected error: %v", key, err)
		}
		if got != want {
			t.Errorf("pressKeyBytes(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestPressKeyBytesModifiers(t *testing.T) {
	cases := []struct {
		key       string
		modifiers []string
		want      string
	}{
		{"c", []string{"ctrl"}, "\x03"},
		{"a", []string{"ctrl"}, "\x01"},
		{"tab", []string{"shift"}, "\x1b[Z"},
		{"a", []string{"shift"}, "A"},
		{"b", []string{"alt"}, "\x1bb"},
	}
	for _, tc := range cases {
		got, err := pressKeyBytes(tc.key, tc.modifiers)
		if err != nil {
			t.Errorf("pressKeyBytes(%q, %v): unexpected error: %v", tc.key, tc.modifiers, err)
		}
		if got != tc.want {
			t.Errorf("pressKeyBytes(%q, %v) = %q, want %q", tc.key, tc.modifiers, got, tc.want)
		}
	}
}

func TestPressKeyBytesUnsupportedModifier(t *testing.T) {
	if _, err := pressKeyBytes("a", []string{"meta"}); err == nil {
		t.Fatal("expected an error for the unsupported meta modifier")
	}
	if _, err := pressKeyBytes("enter", []string{"ctrl"}); err == nil {
		t.Fatal("expected an error for ctrl on a non-letter key")
	}
	if _, err := pressKeyBytes("a", []string{"banana"}); err == nil {
		t.Fatal("expected an error for an unknown modifier name")
	}
}

func TestDispatchRunCommand(t *testing.T) {
	d := startTestDriver(t)
	params, _ := json.Marshal(RunCommandParams{Command: "echo dispatch-hello", WaitMS: 500})
	obs, err := d.Dispatch(context.Background(), ActionRunCommand, params)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !strings.Contains(obs.Text, "dispatch-hello") {
		t.Fatalf("Dispatch output = %q, want it to contain dispatch-hello", obs.Text)
	}
}

func TestDispatchSendKeysNoEnter(t *testing.T) {
	d := startTestDriver(t)
	params, _ := json.Marshal(SendKeysParams{Command: "echo not-run", PressEnter: false, WaitMS: 300})
	obs, err := d.Dispatch(context.Background(), ActionSendKeys, params)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// Typed but not executed: the shell prompt never produced the
	// command's actual output (no newline after it). Check only the
	// diff portion — the appended "Current screen contents" block is
	// expected to show the typed-but-not-yet-run text sitting on the
	// prompt line, which is not the same as it having run.
	diff, _, _ := strings.Cut(obs.Text, "\n\nCurrent screen contents:\n")
	if strings.Contains(diff, "not-run\n") {
		t.Fatalf("expected the command not to have run, got %q", obs.Text)
	}
}

// TestDispatchPressKeyCtrlCInterruptsRealCommand proves ctrl+c isn't just a
// byte-translation test but actually interrupts a real running command,
// the same rigor pressKeyBytes' other cases get via TestDispatchRunCommand.
func TestDispatchPressKeyCtrlCInterruptsRealCommand(t *testing.T) {
	d := startTestDriver(t)
	startParams, _ := json.Marshal(SendKeysParams{Command: "sleep 30; echo slept-fully", PressEnter: true, WaitMS: 300})
	if _, err := d.Dispatch(context.Background(), ActionSendKeys, startParams); err != nil {
		t.Fatalf("Dispatch(send_keys sleep): %v", err)
	}

	params, _ := json.Marshal(driver.PressKeyParams{Key: "c", Modifiers: []string{"ctrl"}})
	if _, err := d.Dispatch(context.Background(), driver.ActionPressKey, params); err != nil {
		t.Fatalf("Dispatch(press_key ctrl+c): %v", err)
	}

	// Confirm the shell is responsive again rather than still blocked in
	// sleep — and that "slept-fully" never appears, i.e. sleep was
	// actually interrupted rather than merely losing the race.
	out, err := d.RunCommand(context.Background(), "echo still-alive", true, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if !strings.Contains(out, "still-alive") {
		t.Fatalf("shell did not respond after ctrl+c; output = %q", out)
	}
	if strings.Contains(out, "slept-fully") {
		t.Fatalf("sleep was not interrupted by ctrl+c; output = %q", out)
	}
}

func TestDispatchPressKeyEscape(t *testing.T) {
	d := startTestDriver(t)
	params, _ := json.Marshal(driver.PressKeyParams{Key: "escape"})
	if _, err := d.Dispatch(context.Background(), driver.ActionPressKey, params); err != nil {
		t.Fatalf("Dispatch(press_key escape): %v", err)
	}
}

func TestDispatchPressKeyUnknown(t *testing.T) {
	d := startTestDriver(t)
	params, _ := json.Marshal(driver.PressKeyParams{Key: "nonsense"})
	if _, err := d.Dispatch(context.Background(), driver.ActionPressKey, params); err == nil {
		t.Fatal("expected an error for an unknown press_key key")
	}
}

func TestDispatchUnsupportedAction(t *testing.T) {
	d := startTestDriver(t)
	if _, err := d.Dispatch(context.Background(), "click", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected an error for an action this driver doesn't support")
	}
}

func TestObserveWaitsAndSnapshots(t *testing.T) {
	d := startTestDriver(t)
	params, _ := json.Marshal(RunCommandParams{Command: "echo observe-me", WaitMS: 300})
	if _, err := d.Dispatch(context.Background(), ActionRunCommand, params); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	obs, err := d.Observe(context.Background(), 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	// Dispatch already consumed the snapshot, so the diff portion of
	// Observe should see nothing new rather than replaying it. The
	// appended "Current screen contents" block is expected to still show
	// it — that's the persistent screen model working as designed, not a
	// diff replay.
	diff, _, _ := strings.Cut(obs.Text, "\n\nCurrent screen contents:\n")
	if strings.Contains(diff, "observe-me") {
		t.Fatalf("Observe replayed already-consumed output: %q", obs.Text)
	}
}

func TestActionsAndPromptFragmentNonEmpty(t *testing.T) {
	d := startTestDriver(t)
	if d.Name() != "tui" {
		t.Fatalf("Name() = %q, want tui", d.Name())
	}
	actions := d.Actions()
	if len(actions) < 3 {
		t.Fatalf("expected at least 3 actions, got %d", len(actions))
	}
	if d.PromptFragment() == "" {
		t.Fatal("PromptFragment should not be empty")
	}
}

func TestFactoryRegistered(t *testing.T) {
	f, ok := driver.Get("tui")
	if !ok {
		t.Fatal("tui driver not registered")
	}
	drv, err := f(context.Background(), driver.Config{Argv: []string{"/bin/sh"}})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer drv.Close()
	if !drv.Alive() {
		t.Fatal("expected freshly-started driver to be alive")
	}
}
