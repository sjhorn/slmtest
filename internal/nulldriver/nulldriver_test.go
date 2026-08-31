package nulldriver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sjhorn/slmtest/internal/driver"
)

func TestScriptedDispatchAndObserve(t *testing.T) {
	d := NewScripted(
		driver.Observation{Text: "first"},
		driver.Observation{Text: "second"},
	)
	obs, err := d.Dispatch(context.Background(), "noop", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if obs.Text != "first" {
		t.Fatalf("Dispatch = %q, want first", obs.Text)
	}
	obs, err = d.Observe(context.Background(), 0)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Text != "second" {
		t.Fatalf("Observe = %q, want second", obs.Text)
	}
}

func TestScriptExhaustedErrors(t *testing.T) {
	d := NewScripted()
	if _, err := d.Observe(context.Background(), 0); err == nil {
		t.Fatal("expected an error when the script is exhausted")
	}
}

func TestCallsRecorded(t *testing.T) {
	d := NewScripted(driver.Observation{}, driver.Observation{})
	params, _ := json.Marshal(driver.PressKeyParams{Key: "enter"})
	if _, err := d.Dispatch(context.Background(), driver.ActionPressKey, params); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(d.Calls) != 1 || d.Calls[0].Action != driver.ActionPressKey {
		t.Fatalf("Calls = %+v, want one press_key call", d.Calls)
	}
}

func TestAliveAndClose(t *testing.T) {
	d := NewScripted()
	if !d.Alive() {
		t.Fatal("fresh driver should be alive")
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if d.Alive() {
		t.Fatal("closed driver should not be alive")
	}
}

func TestKill(t *testing.T) {
	d := NewScripted()
	d.Kill()
	if d.Alive() {
		t.Fatal("killed driver should not be alive")
	}
}

func TestRegisteredUnderNull(t *testing.T) {
	f, ok := driver.Get("null")
	if !ok {
		t.Fatal("null driver not registered")
	}
	d, err := f(context.Background(), driver.Config{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if d.Name() != "null" {
		t.Fatalf("Name() = %q, want null", d.Name())
	}
}

func TestImplementsInterface(t *testing.T) {
	var _ driver.Driver = (*Driver)(nil)
}
