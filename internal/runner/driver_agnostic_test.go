package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/driver"
	"github.com/sjhorn/slmtest/internal/nulldriver"
)

// These tests prove the turn loop genuinely dispatches through the
// driver.Driver interface rather than assuming a concrete *ptydriver.Driver
// — run_command/send_keys/wait all go through Dispatch/Observe, and the
// runner never imports internal/ptydriver at all (see this package's
// import list). nulldriver stands in as a second, dependency-free
// implementation, in the same spirit as internal/agent's fakeSLM
// standing in for a real model.

func TestRunDispatchesThroughDriverInterfaceNotConcretePTY(t *testing.T) {
	nd := nulldriver.NewScripted(
		driver.Observation{Text: "marker-abc appeared on screen"},
	)
	name := "null-scripted-run-command"
	driver.Register(name, func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return nd, nil
	})

	f := newFakeSLM(t, replyEcho, replyPass)
	report, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(), Options{DriverName: name})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Passed {
		t.Fatalf("Passed = false, want true; steps: %+v", report.Steps)
	}
	if len(nd.Calls) != 1 {
		t.Fatalf("nulldriver.Calls = %+v, want exactly one Dispatch call", nd.Calls)
	}
	if nd.Calls[0].Action != driver.ActionType(agent.ActionRunCommand) {
		t.Errorf("dispatched action = %q, want %q", nd.Calls[0].Action, agent.ActionRunCommand)
	}
	if report.Steps[0].Transcript[0].PTYOutput != "marker-abc appeared on screen" {
		t.Errorf("transcript output = %q, want the driver's scripted observation", report.Steps[0].Transcript[0].PTYOutput)
	}
}

func TestRunWaitGoesThroughObserve(t *testing.T) {
	nd := nulldriver.NewScripted(
		driver.Observation{Text: "still building..."},
	)
	name := "null-scripted-wait"
	driver.Register(name, func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return nd, nil
	})

	replyWait := `{"action":"wait","wait_ms":10}`
	f := newFakeSLM(t, replyWait, replyPass)
	report, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(), Options{DriverName: name})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Passed {
		t.Fatalf("Passed = false, want true; steps: %+v", report.Steps)
	}
	if report.Steps[0].Transcript[0].PTYOutput != "still building..." {
		t.Errorf("transcript output = %q, want the driver's scripted observation", report.Steps[0].Transcript[0].PTYOutput)
	}
}

func TestRunUnknownDriverIsAnError(t *testing.T) {
	f := newFakeSLM(t)
	_, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(), Options{DriverName: "does-not-exist"})
	if err == nil {
		t.Fatal("expected an error for an unregistered driver name")
	}
}

// TestRunGenericActionGoesThroughDefaultDispatch proves a driver-specific
// action beyond the original five (here, a nulldriver primitive) reaches
// Dispatch with its Params passed through verbatim — this is the path
// examples/browser-test.md exercises for real against a browser driver's
// "click".
func TestRunGenericActionGoesThroughDefaultDispatch(t *testing.T) {
	nd := nulldriver.NewScripted(
		driver.Observation{Text: "clicked the thing"},
	)
	name := "null-scripted-generic-action"
	driver.Register(name, func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return nd, nil
	})

	replyClick := `{"action":"click","params":{"target":"#submit"}}`
	f := newFakeSLM(t, replyClick, replyPass)
	report, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(), Options{DriverName: name})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Passed {
		t.Fatalf("Passed = false, want true; steps: %+v", report.Steps)
	}
	if len(nd.Calls) != 1 {
		t.Fatalf("nulldriver.Calls = %+v, want exactly one Dispatch call", nd.Calls)
	}
	if nd.Calls[0].Action != "click" {
		t.Errorf("dispatched action = %q, want click", nd.Calls[0].Action)
	}
	if string(nd.Calls[0].Params) != `{"target":"#submit"}` {
		t.Errorf("dispatched params = %s, want the model's params passed through verbatim", nd.Calls[0].Params)
	}
	if report.Steps[0].Transcript[0].PTYOutput != "clicked the thing" {
		t.Errorf("transcript output = %q, want the driver's scripted observation", report.Steps[0].Transcript[0].PTYOutput)
	}
}

// TestRunNewMouseActionGoesThroughDefaultDispatch proves the Phase B
// mouse primitives (double_click here) reach Dispatch through the exact
// same generic default-dispatch path click already exercises above —
// nothing about the runner needed to change to support a wider action
// vocabulary.
func TestRunNewMouseActionGoesThroughDefaultDispatch(t *testing.T) {
	nd := nulldriver.NewScripted(
		driver.Observation{Text: "double-clicked the thing"},
	)
	name := "null-scripted-double-click"
	driver.Register(name, func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return nd, nil
	})

	replyDoubleClick := `{"action":"double_click","params":{"target":"#item"}}`
	f := newFakeSLM(t, replyDoubleClick, replyPass)
	report, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(), Options{DriverName: name})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Passed {
		t.Fatalf("Passed = false, want true; steps: %+v", report.Steps)
	}
	if len(nd.Calls) != 1 {
		t.Fatalf("nulldriver.Calls = %+v, want exactly one Dispatch call", nd.Calls)
	}
	if nd.Calls[0].Action != "double_click" {
		t.Errorf("dispatched action = %q, want double_click", nd.Calls[0].Action)
	}
	if string(nd.Calls[0].Params) != `{"target":"#item"}` {
		t.Errorf("dispatched params = %s, want the model's params passed through verbatim", nd.Calls[0].Params)
	}
}

// TestRunPressKeyWithModifiersGoesThroughDefaultDispatch does the same
// for press_key's new "modifiers" field.
func TestRunPressKeyWithModifiersGoesThroughDefaultDispatch(t *testing.T) {
	nd := nulldriver.NewScripted(
		driver.Observation{Text: "pressed ctrl+c"},
	)
	name := "null-scripted-press-key-modifiers"
	driver.Register(name, func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return nd, nil
	})

	replyPressKey := `{"action":"press_key","params":{"key":"c","modifiers":["ctrl"]}}`
	f := newFakeSLM(t, replyPressKey, replyPass)
	report, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(), Options{DriverName: name})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Passed {
		t.Fatalf("Passed = false, want true; steps: %+v", report.Steps)
	}
	if len(nd.Calls) != 1 || nd.Calls[0].Action != driver.ActionPressKey {
		t.Fatalf("nulldriver.Calls = %+v, want exactly one press_key Dispatch call", nd.Calls)
	}
	if string(nd.Calls[0].Params) != `{"key":"c","modifiers":["ctrl"]}` {
		t.Errorf("dispatched params = %s, want the model's params passed through verbatim", nd.Calls[0].Params)
	}
}

// TestRunDifferentParamsNotTreatedAsRepeat proves the repeat-loop
// signature accounts for action.Params, not just action.Command — a
// press_key "up" followed by a genuinely different press_key "down" must
// not be misdetected as the model repeating itself (action.Command is
// always empty for a generic driver action; only Params carries its real
// content).
func TestRunDifferentParamsNotTreatedAsRepeat(t *testing.T) {
	nd := nulldriver.NewScripted(
		driver.Observation{Text: "moved up"},
		driver.Observation{Text: "moved down"},
	)
	name := "null-scripted-distinct-press-keys"
	driver.Register(name, func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return nd, nil
	})

	replyUp := `{"action":"press_key","params":{"key":"up"}}`
	replyDown := `{"action":"press_key","params":{"key":"down"}}`
	f := newFakeSLM(t, replyUp, replyDown, replyPass)
	report, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(), Options{DriverName: name})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Passed {
		t.Fatalf("Passed = false, want true; steps: %+v", report.Steps)
	}
	retry := f.request(1).Messages
	last := retry[len(retry)-1].Content
	if strings.Contains(last, "exact command") || strings.Contains(last, "repeating") {
		t.Errorf("two genuinely different press_key calls were misdetected as a repeat; prompt:\n%s", last)
	}
}

// rejectingDriver is a minimal driver.Driver whose Dispatch always
// rejects the action it's given with driver.UnsupportedActionError —
// used to test that the runner recovers from this the way it recovers
// from a JSON parse error, instead of aborting the whole run. This is
// the regression test for the bug a live run against
// examples/browser-test.md surfaced: the model picked "run_command"
// (all it had available before agent.Action gained a generic "params"
// field), the browser driver correctly rejected it, and the runner
// treated that rejection as fatal instead of feeding it back for a
// retry.
type rejectingDriver struct{ calls int }

func (r *rejectingDriver) Name() string                 { return "rejecting" }
func (r *rejectingDriver) Actions() []driver.ActionSpec { return nil }
func (r *rejectingDriver) PromptFragment() string       { return "" }
func (r *rejectingDriver) Dispatch(ctx context.Context, action driver.ActionType, params json.RawMessage) (driver.Observation, error) {
	r.calls++
	return driver.Observation{}, driver.NewUnsupportedActionError("rejecting", action)
}
func (r *rejectingDriver) Observe(ctx context.Context, wait time.Duration) (driver.Observation, error) {
	return driver.Observation{}, nil
}
func (r *rejectingDriver) Alive() bool  { return true }
func (r *rejectingDriver) Close() error { return nil }

func TestRunUnsupportedActionIsRecoveredNotAborted(t *testing.T) {
	rd := &rejectingDriver{}
	name := "rejecting-driver-test"
	driver.Register(name, func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return rd, nil
	})

	replyBadAction := `{"action":"run_command","command":"echo hi"}`
	f := newFakeSLM(t, replyBadAction, replyPass)
	report, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(), Options{DriverName: name})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Aborted {
		t.Fatalf("Aborted = true, want the run to recover; steps: %+v", report.Steps)
	}
	if !report.Passed {
		t.Fatalf("Passed = false, want true; steps: %+v", report.Steps)
	}
	if rd.calls != 1 {
		t.Fatalf("Dispatch was called %d times, want exactly 1 (the rejected attempt)", rd.calls)
	}
	if f.calls() != 2 {
		t.Fatalf("SLM was called %d times, want 2 (the rejected attempt + a recovery turn)", f.calls())
	}
	retry := f.request(1).Messages
	if last := retry[len(retry)-1].Content; !strings.Contains(last, "not available") {
		t.Errorf("retry prompt did not explain the unsupported action; got:\n%s", last)
	}
}

// badParamsDriver is a minimal driver.Driver whose Dispatch always
// rejects the params it's given with driver.BadParamsError — the
// regression test for a bug found live: a model sent
// {"action":"navigate","url":"..."} (a flat "url" field instead of
// nesting it under "params"), which agent.Action silently dropped
// (Params stayed nil), and the browser driver resolved the resulting
// empty URL leniently — a silent no-op with no error, giving the model
// no signal to self-correct. driver.BadParamsError exists so this class
// of mistake is loud and recoverable instead.
type badParamsDriver struct{ calls int }

func (b *badParamsDriver) Name() string                 { return "bad-params" }
func (b *badParamsDriver) Actions() []driver.ActionSpec { return nil }
func (b *badParamsDriver) PromptFragment() string       { return "" }
func (b *badParamsDriver) Dispatch(ctx context.Context, action driver.ActionType, params json.RawMessage) (driver.Observation, error) {
	b.calls++
	return driver.Observation{}, driver.NewBadParamsError("bad-params", action, `"url" is required`)
}
func (b *badParamsDriver) Observe(ctx context.Context, wait time.Duration) (driver.Observation, error) {
	return driver.Observation{}, nil
}
func (b *badParamsDriver) Alive() bool  { return true }
func (b *badParamsDriver) Close() error { return nil }

func TestRunBadParamsIsRecoveredNotAborted(t *testing.T) {
	bd := &badParamsDriver{}
	name := "bad-params-driver-test"
	driver.Register(name, func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return bd, nil
	})

	replyBadParams := `{"action":"navigate","url":"somewhere.html"}`
	f := newFakeSLM(t, replyBadParams, replyPass)
	report, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(), Options{DriverName: name})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Aborted {
		t.Fatalf("Aborted = true, want the run to recover; steps: %+v", report.Steps)
	}
	if !report.Passed {
		t.Fatalf("Passed = false, want true; steps: %+v", report.Steps)
	}
	if bd.calls != 1 {
		t.Fatalf("Dispatch was called %d times, want exactly 1 (the rejected attempt)", bd.calls)
	}
	retry := f.request(1).Messages
	if last := retry[len(retry)-1].Content; !strings.Contains(last, "params were invalid") {
		t.Errorf("retry prompt did not explain the bad params; got:\n%s", last)
	}
}

// TestRunRepeatedBadParamsGetsNudged is the regression test for a real
// gap found running examples/nano-edit-test.md and examples/tui-claude-
// chat-test.md against a real model: it sent press_key with the exact
// same invalid (flat, unnested) params on every turn of a step's budget
// and never self-corrected — because dispatchErrorNote's message was fed
// back on its own, without repeatNudge/repeatedMistakeNudge ever being
// appended on that path, unlike the successful-dispatch path. This
// proves the nudge now fires there too.
func TestRunRepeatedBadParamsGetsNudged(t *testing.T) {
	bd := &badParamsDriver{}
	name := "bad-params-repeat-test"
	driver.Register(name, func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return bd, nil
	})

	sameBadReply := `{"action":"press_key","key":""}`
	f := newFakeSLM(t, sameBadReply, sameBadReply, sameBadReply, replyPass)
	ts := testSpec(t, step(1, "one"))
	ts.MaxTurnsPerStep = 4
	report, err := Run(context.Background(), ts, f.client(), Options{DriverName: name})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Aborted {
		t.Fatalf("Aborted = true, want the run to recover; steps: %+v", report.Steps)
	}
	if !report.Passed {
		t.Fatalf("Passed = false, want true; steps: %+v", report.Steps)
	}
	if bd.calls != 3 {
		t.Fatalf("Dispatch was called %d times, want exactly 3 (the three identical rejected attempts)", bd.calls)
	}
	// request(2) is the third call to the SLM — the prompt sent after the
	// second identical rejection, i.e. the first turn where repeats >= 1.
	retry := f.request(2).Messages
	last := retry[len(retry)-1].Content
	if !strings.Contains(last, "params were invalid") {
		t.Errorf("retry prompt did not explain the bad params; got:\n%s", last)
	}
	if !strings.Contains(last, "exact same mistake") {
		t.Errorf("retry prompt did not nudge about the repeated mistake; got:\n%s", last)
	}
}

func TestSpecDriverFieldSelectsDriver(t *testing.T) {
	nd := nulldriver.NewScripted(driver.Observation{Text: "marker-abc via spec field"})
	name := "null-scripted-spec-field"
	driver.Register(name, func(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
		return nd, nil
	})

	ts := testSpec(t, step(1, "one"))
	ts.Driver = name
	f := newFakeSLM(t, replyEcho, replyPass)
	report, err := Run(context.Background(), ts, f.client(), Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Passed {
		t.Fatalf("Passed = false, want true; steps: %+v", report.Steps)
	}
	if len(nd.Calls) != 1 {
		t.Fatalf("expected the spec's Driver field to route to the scripted driver, got %d calls", len(nd.Calls))
	}
}
