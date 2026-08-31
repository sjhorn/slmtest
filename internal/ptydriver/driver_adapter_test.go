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
		got, err := pressKeyBytes(key)
		if err != nil {
			t.Errorf("pressKeyBytes(%q): unexpected error: %v", key, err)
		}
		if got != want {
			t.Errorf("pressKeyBytes(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestPressKeyBytesUnknown(t *testing.T) {
	if _, err := pressKeyBytes("banana"); err == nil {
		t.Fatal("expected an error for an unknown key name")
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
	// command's actual output (no newline after it).
	if strings.Contains(obs.Text, "not-run\n") {
		t.Fatalf("expected the command not to have run, got %q", obs.Text)
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
	// Dispatch already consumed the snapshot, so Observe should see
	// nothing new rather than replaying it.
	if strings.Contains(obs.Text, "observe-me") {
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
