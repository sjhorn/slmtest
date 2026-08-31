package driver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeDriver is a minimal Driver used only to prove the interface's
// method set is implementable and Register/Get round-trip correctly.
type fakeDriver struct{ name string }

func (f *fakeDriver) Name() string          { return f.name }
func (f *fakeDriver) Actions() []ActionSpec { return []ActionSpec{PrimitivePressKey} }
func (f *fakeDriver) PromptFragment() string {
	return "fake driver fragment"
}
func (f *fakeDriver) Dispatch(ctx context.Context, action ActionType, params json.RawMessage) (Observation, error) {
	return Observation{Text: string(action)}, nil
}
func (f *fakeDriver) Observe(ctx context.Context, wait time.Duration) (Observation, error) {
	return Observation{Text: "observed"}, nil
}
func (f *fakeDriver) Alive() bool  { return true }
func (f *fakeDriver) Close() error { return nil }

var _ Driver = (*fakeDriver)(nil)

func TestRegisterGet(t *testing.T) {
	name := "fake-for-test"
	Register(name, func(ctx context.Context, cfg Config) (Driver, error) {
		return &fakeDriver{name: name}, nil
	})
	f, ok := Get(name)
	if !ok {
		t.Fatalf("Get(%q) not found after Register", name)
	}
	d, err := f(context.Background(), Config{})
	if err != nil {
		t.Fatalf("factory returned error: %v", err)
	}
	if d.Name() != name {
		t.Fatalf("Name() = %q, want %q", d.Name(), name)
	}
}

func TestGetUnknown(t *testing.T) {
	if _, ok := Get("does-not-exist-xyz"); ok {
		t.Fatalf("Get should report false for an unregistered driver")
	}
	err := ErrUnknownDriver("does-not-exist-xyz")
	if err == nil {
		t.Fatalf("ErrUnknownDriver should never return nil")
	}
}

func TestPrimitiveParamRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		params any
		target any
	}{
		{"press_key", PressKeyParams{Key: "escape"}, &PressKeyParams{}},
		{"navigate_direction", NavigateDirectionParams{Direction: "up"}, &NavigateDirectionParams{}},
		{"click", ClickParams{Target: "#submit"}, &ClickParams{}},
		{"type_text", TypeTextParams{Text: "hello"}, &TypeTextParams{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.params)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := json.Unmarshal(raw, c.target); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
		})
	}
}

func TestUnsupportedActionError(t *testing.T) {
	err := NewUnsupportedActionError("tui", ActionPressKey)
	var uae *UnsupportedActionError
	if !errors.As(err, &uae) {
		t.Fatalf("NewUnsupportedActionError did not produce an *UnsupportedActionError: %T", err)
	}
	if uae.DriverName != "tui" || uae.Action != ActionPressKey {
		t.Errorf("got %+v, want DriverName=tui Action=press_key", uae)
	}
	if !strings.Contains(err.Error(), "press_key") || !strings.Contains(err.Error(), "tui") {
		t.Errorf("Error() = %q, want it to name both the action and the driver", err.Error())
	}
}

func TestBadParamsError(t *testing.T) {
	err := NewBadParamsError("browser", ActionClick, `"target" is required`)
	var bpe *BadParamsError
	if !errors.As(err, &bpe) {
		t.Fatalf("NewBadParamsError did not produce a *BadParamsError: %T", err)
	}
	if bpe.DriverName != "browser" || bpe.Action != ActionClick {
		t.Errorf("got %+v, want DriverName=browser Action=click", bpe)
	}
	if !strings.Contains(err.Error(), "target") || !strings.Contains(err.Error(), "browser") {
		t.Errorf("Error() = %q, want it to name both the reason and the driver", err.Error())
	}
}

func TestPrimitiveSpecsHaveSchemaAndDescription(t *testing.T) {
	specs := []ActionSpec{PrimitivePressKey, PrimitiveDirectional, PrimitiveClick, PrimitiveTypeText}
	for _, s := range specs {
		if s.Type == "" {
			t.Errorf("primitive has empty Type")
		}
		if s.Description == "" {
			t.Errorf("%s: empty Description", s.Type)
		}
		var v any
		if err := json.Unmarshal(s.ParamSchema, &v); err != nil {
			t.Errorf("%s: ParamSchema is not valid JSON: %v", s.Type, err)
		}
	}
}
