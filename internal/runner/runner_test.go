package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/slmtest/internal/agent"
	"github.com/example/slmtest/internal/spec"
)

// fakeSLM serves a fixed script of replies, one per request, and records
// every request body so tests can assert on what the model was actually
// shown. Once the script is exhausted it fails the test rather than
// looping — a runner that asks for more turns than expected is a bug
// worth surfacing loudly.
type fakeSLM struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	replies []string
	n       int
	reqs    []chatRequest
}

type chatRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func newFakeSLM(t *testing.T, replies ...string) *fakeSLM {
	t.Helper()
	f := &fakeSLM{t: t, replies: replies}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSLM) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decoding request: %v", err)
	}
	f.reqs = append(f.reqs, req)

	if f.n >= len(f.replies) {
		f.t.Errorf("SLM called %d times, script only has %d replies", f.n+1, len(f.replies))
		http.Error(w, "script exhausted", http.StatusInternalServerError)
		return
	}
	reply := f.replies[f.n]
	f.n++

	resp, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": reply}}},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}

func (f *fakeSLM) client() *agent.Client { return agent.NewClient(f.srv.URL+"/v1", "fake", "") }

func (f *fakeSLM) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *fakeSLM) request(i int) chatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.reqs) {
		f.t.Fatalf("no request %d (only %d recorded)", i, len(f.reqs))
	}
	return f.reqs[i]
}

func testSpec(t *testing.T, steps ...spec.Step) *spec.Test {
	t.Helper()
	return &spec.Test{Name: "unit-test", Shell: "/bin/sh", MaxTurnsPerStep: 4, Steps: steps}
}

func step(index int, title string) spec.Step {
	return spec.Step{Index: index, Title: title, Goal: "goal " + title, Hint: "hint " + title, Expect: "expect " + title}
}

func run(t *testing.T, f *fakeSLM, ts *spec.Test) *Report {
	t.Helper()
	report, err := Run(context.Background(), ts, f.client(), Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return report
}

const (
	replyEcho  = `{"thought":"echo a marker","action":"run_command","command":"echo marker-abc","wait_ms":400}`
	replyPass  = `{"action":"finish_step","step_result":"pass","reason":"saw the marker"}`
	replyFail  = `{"action":"finish_step","step_result":"fail","reason":"marker never appeared"}`
	replyAbort = `{"action":"abort_test","reason":"shell is unusable"}`
)

func TestRunAllStepsPass(t *testing.T) {
	f := newFakeSLM(t, replyEcho, replyPass, replyEcho, replyPass)
	report := run(t, f, testSpec(t, step(1, "one"), step(2, "two")))

	if !report.Passed {
		t.Errorf("Passed = false, want true; steps: %+v", report.Steps)
	}
	if report.Aborted {
		t.Error("Aborted = true, want false")
	}
	if len(report.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(report.Steps))
	}
	for i, s := range report.Steps {
		if s.Result != agent.ResultPass {
			t.Errorf("step %d Result = %q, want pass", i+1, s.Result)
		}
		if s.Reason != "saw the marker" {
			t.Errorf("step %d Reason = %q", i+1, s.Reason)
		}
		if s.Turns != 2 {
			t.Errorf("step %d Turns = %d, want 2", i+1, s.Turns)
		}
	}
}

// The first prompt of a step must carry the Goal/Hint/Expect verbatim —
// that text is the entire specification the model is graded against.
func TestFirstPromptCarriesStepFields(t *testing.T) {
	f := newFakeSLM(t, replyPass)
	run(t, f, testSpec(t, step(1, "one")))

	msgs := f.request(0).Messages
	if len(msgs) < 2 {
		t.Fatalf("request had %d messages, want at least 2", len(msgs))
	}
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "finish_step") {
		t.Errorf("first message is not the system prompt: %+v", msgs[0])
	}
	first := msgs[len(msgs)-1].Content
	for _, want := range []string{"goal one", "hint one", "expect one", "STEP 1"} {
		if !strings.Contains(first, want) {
			t.Errorf("first user prompt missing %q; got:\n%s", want, first)
		}
	}
}

// Real PTY output must reach the model — a harness that summarizes or
// drops it would leave the model grading something it cannot see.
func TestPTYOutputIsShownToTheModel(t *testing.T) {
	f := newFakeSLM(t, replyEcho, replyPass)
	report := run(t, f, testSpec(t, step(1, "one")))

	second := f.request(1).Messages
	last := second[len(second)-1].Content
	if !strings.Contains(last, "marker-abc") {
		t.Errorf("second turn's prompt did not include PTY output; got:\n%s", last)
	}

	if got := report.Steps[0].Transcript[0].PTYOutput; !strings.Contains(got, "marker-abc") {
		t.Errorf("transcript PTYOutput = %q, want it to contain marker-abc", got)
	}
}

// Documented invariant (see agent.Action.Thought): the model's own
// reasoning is kept for logs but never replayed into its own context, so
// it reasons from terminal state rather than from its prior commitments.
func TestThoughtIsNotReplayedIntoModelContext(t *testing.T) {
	f := newFakeSLM(t, replyEcho, replyPass)
	run(t, f, testSpec(t, step(1, "one")))

	for _, m := range f.request(1).Messages {
		if strings.Contains(m.Content, "echo a marker") {
			t.Errorf("model's thought was replayed back to it in a %q message: %s", m.Role, m.Content)
		}
	}

	// ...but it is still captured for the human-facing transcript.
	if got := run(t, newFakeSLM(t, replyEcho, replyPass), testSpec(t, step(1, "one"))); got.Steps[0].Transcript[0].Action.Thought != "echo a marker" {
		t.Errorf("transcript lost the thought: %q", got.Steps[0].Transcript[0].Action.Thought)
	}
}

// A malformed reply costs a turn, not the run: the exact parse error goes
// back to the model, which usually self-corrects. This is the single most
// load-bearing behavior for small-model reliability.
func TestMalformedReplyIsFedBackAndRecovers(t *testing.T) {
	f := newFakeSLM(t, "Sure, I'll check that for you!", replyPass)
	report := run(t, f, testSpec(t, step(1, "one")))

	if !report.Passed {
		t.Errorf("Passed = false, want true after recovery; reason: %s", report.Steps[0].Reason)
	}
	if report.Steps[0].Turns != 2 {
		t.Errorf("Turns = %d, want 2 (the bad turn plus the corrected one)", report.Steps[0].Turns)
	}

	retry := f.request(1).Messages
	last := retry[len(retry)-1].Content
	if !strings.Contains(last, "could not be parsed") || !strings.Contains(last, "not valid JSON") {
		t.Errorf("retry prompt did not carry the parse error; got:\n%s", last)
	}
	// The model must see its own bad reply to understand what to fix.
	if !strings.Contains(retry[len(retry)-2].Content, "Sure, I'll check that") {
		t.Errorf("retry context dropped the malformed reply: %+v", retry)
	}
	if got := report.Steps[0].Transcript[0].Err; !strings.Contains(got, "not valid JSON") {
		t.Errorf("transcript Err = %q, want the parse error", got)
	}
}

// A schema-valid-JSON-but-wrong-action reply gets the same treatment.
func TestInvalidActionIsFedBackAndRecovers(t *testing.T) {
	f := newFakeSLM(t, `{"action":"reboot_machine"}`, replyPass)
	report := run(t, f, testSpec(t, step(1, "one")))

	if !report.Passed {
		t.Fatalf("Passed = false, want true; reason: %s", report.Steps[0].Reason)
	}
	retry := f.request(1).Messages
	if last := retry[len(retry)-1].Content; !strings.Contains(last, "unknown action type") {
		t.Errorf("retry prompt did not explain the bad action; got:\n%s", last)
	}
}

func TestRunStopsAtFirstFailingStep(t *testing.T) {
	f := newFakeSLM(t, replyFail)
	report := run(t, f, testSpec(t, step(1, "one"), step(2, "two"), step(3, "three")))

	if report.Passed {
		t.Error("Passed = true, want false")
	}
	if len(report.Steps) != 1 {
		t.Errorf("len(Steps) = %d, want 1 — later steps must not be attempted after a failure", len(report.Steps))
	}
	if f.calls() != 1 {
		t.Errorf("SLM called %d times, want 1", f.calls())
	}
	if report.Steps[0].Reason != "marker never appeared" {
		t.Errorf("Reason = %q", report.Steps[0].Reason)
	}
}

func TestAbortEndsTheRunAndIsDistinctFromFailure(t *testing.T) {
	f := newFakeSLM(t, replyAbort)
	report := run(t, f, testSpec(t, step(1, "one"), step(2, "two")))

	if !report.Aborted {
		t.Error("Aborted = false, want true")
	}
	if report.Passed {
		t.Error("Passed = true, want false")
	}
	if len(report.Steps) != 1 {
		t.Errorf("len(Steps) = %d, want 1", len(report.Steps))
	}
	if !report.Steps[0].Aborted {
		t.Error("step Aborted = false, want true")
	}
	if report.Steps[0].Reason != "shell is unusable" {
		t.Errorf("Reason = %q", report.Steps[0].Reason)
	}
}

// Running out of turns is a step failure with a distinguishable reason —
// "the model never decided" is a different problem from "the model
// decided it failed", and the report has to tell them apart.
func TestTurnBudgetExhaustionFailsTheStep(t *testing.T) {
	ts := testSpec(t, step(1, "one"))
	ts.MaxTurnsPerStep = 3
	f := newFakeSLM(t, replyEcho, replyEcho, replyEcho)

	report := run(t, f, ts)
	if report.Passed {
		t.Error("Passed = true, want false")
	}
	if got := report.Steps[0].Turns; got != 3 {
		t.Errorf("Turns = %d, want 3", got)
	}
	if !strings.Contains(report.Steps[0].Reason, "used all 3 turns") {
		t.Errorf("Reason = %q, want it to mention the exhausted turn budget", report.Steps[0].Reason)
	}
	if f.calls() != 3 {
		t.Errorf("SLM called %d times, want exactly the 3-turn budget", f.calls())
	}
}

func TestDefaultTurnBudgetIsSix(t *testing.T) {
	ts := testSpec(t, step(1, "one"))
	ts.MaxTurnsPerStep = 0 // unset

	replies := make([]string, 6)
	for i := range replies {
		replies[i] = replyEcho
	}
	f := newFakeSLM(t, replies...)

	report := run(t, f, ts)
	if got := report.Steps[0].Turns; got != 6 {
		t.Errorf("Turns = %d, want the documented default of 6", got)
	}
}

// An endpoint failure aborts rather than silently scoring the step as a
// failure of the system under test — the two mean very different things.
func TestEndpointErrorAbortsTheRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"model crashed"}}`))
	}))
	defer srv.Close()

	report, err := Run(context.Background(), testSpec(t, step(1, "one")), agent.NewClient(srv.URL+"/v1", "m", ""), Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Aborted || report.Passed {
		t.Errorf("Aborted = %v, Passed = %v; want true, false", report.Aborted, report.Passed)
	}
	if !strings.Contains(report.Steps[0].Reason, "SLM endpoint error") {
		t.Errorf("Reason = %q, want it to identify this as an endpoint problem", report.Steps[0].Reason)
	}
}

func TestPerStepTimeoutFailsWithTimedOutFlag(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer slow.Close()

	report, err := Run(context.Background(), testSpec(t, step(1, "one")), agent.NewClient(slow.URL+"/v1", "m", ""),
		Options{StepTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Passed {
		t.Error("Passed = true, want false")
	}
	// Either the in-flight request is cancelled or the next turn's budget
	// check trips; both must end the step, and neither may report a pass.
	if report.Steps[0].Result == agent.ResultPass {
		t.Errorf("step passed despite the timeout: %+v", report.Steps[0])
	}
}

// Each step starts with a fresh history so step N's fumbling does not
// pollute step N+1's reasoning.
func TestHistoryIsResetBetweenSteps(t *testing.T) {
	f := newFakeSLM(t, replyEcho, replyPass, replyPass)
	run(t, f, testSpec(t, step(1, "one"), step(2, "two")))

	// Requests 0 and 1 belong to step 1 (2 and 4 messages incl. system);
	// request 2 is step 2's opening turn and must be back down to 2.
	third := f.request(2).Messages
	if len(third) != 2 {
		t.Errorf("step 2's first request had %d messages, want 2 (system + first prompt): %+v", len(third), third)
	}
	if !strings.Contains(third[1].Content, "goal two") {
		t.Errorf("step 2's first prompt = %q", third[1].Content)
	}
	for _, m := range third {
		if strings.Contains(m.Content, "marker-abc") {
			t.Errorf("step 1's terminal output leaked into step 2's context: %s", m.Content)
		}
	}
}

func TestOptionsShellOverridesSpecShell(t *testing.T) {
	ts := testSpec(t, step(1, "one"))
	ts.Shell = "/nonexistent/shell"

	// If the override were ignored, Run would fail to start the PTY.
	f := newFakeSLM(t, replyPass)
	report, err := Run(context.Background(), ts, f.client(), Options{Shell: "/bin/sh"})
	if err != nil {
		t.Fatalf("Run with shell override: %v", err)
	}
	if !report.Passed {
		t.Errorf("Passed = false, want true")
	}
}

func TestUnstartableShellIsAnError(t *testing.T) {
	ts := testSpec(t, step(1, "one"))
	ts.Shell = "/nonexistent/shell"

	if _, err := Run(context.Background(), ts, newFakeSLM(t).client(), Options{}); err == nil {
		t.Fatal("Run succeeded with an unstartable shell, want error")
	}
}

func TestStepStatus(t *testing.T) {
	tests := []struct {
		name    string
		outcome StepOutcome
		want    StepStatus
	}{
		{"passed", StepOutcome{Result: agent.ResultPass}, StatusPass},
		{"model judged it failed", StepOutcome{Result: agent.ResultFail}, StatusFail},
		{"no verdict at all", StepOutcome{}, StatusFail},
		{"timed out", StepOutcome{Result: agent.ResultFail, TimedOut: true}, StatusTimeout},
		{"aborted", StepOutcome{Aborted: true}, StatusAbort},
		// An abort outranks a timeout: a dead endpoint is the thing to
		// report, even if the step also ran out of clock.
		{"aborted and timed out", StepOutcome{Aborted: true, TimedOut: true}, StatusAbort},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.outcome.Status(); got != tc.want {
				t.Errorf("Status() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The -json output is a CI contract, so its shape is asserted explicitly
// rather than left to whatever the Go structs happen to look like.
func TestReportJSONShape(t *testing.T) {
	f := newFakeSLM(t, replyEcho, replyPass)
	ts := testSpec(t, step(1, "one"))
	ts.Description = "a described test"
	report := run(t, f, ts)

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got["name"] != "unit-test" {
		t.Errorf("name = %v", got["name"])
	}
	if got["description"] != "a described test" {
		t.Errorf("description = %v", got["description"])
	}
	if got["passed"] != true {
		t.Errorf("passed = %v, want true", got["passed"])
	}
	if got["aborted"] != false {
		t.Errorf("aborted = %v, want false", got["aborted"])
	}
	// The full parsed Test must not be echoed: its Steps would duplicate
	// everything already under "steps".
	if _, ok := got["Test"]; ok {
		t.Error(`JSON contains a "Test" key; the spec should not be echoed wholesale`)
	}

	steps, ok := got["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("steps = %v, want 1 entry", got["steps"])
	}
	s0 := steps[0].(map[string]any)
	// Step spec fields are flattened into the outcome for consumers.
	for k, want := range map[string]any{
		"index": float64(1), "title": "one", "goal": "goal one",
		"hint": "hint one", "expect": "expect one",
		"status": "pass", "reason": "saw the marker", "turns": float64(2),
	} {
		if s0[k] != want {
			t.Errorf("steps[0].%s = %v, want %v", k, s0[k], want)
		}
	}

	transcript, ok := s0["transcript"].([]any)
	if !ok || len(transcript) != 2 {
		t.Fatalf("transcript = %v, want 2 turns", s0["transcript"])
	}
	turn0 := transcript[0].(map[string]any)
	if !strings.Contains(turn0["pty_output"].(string), "marker-abc") {
		t.Errorf("transcript[0].pty_output = %v", turn0["pty_output"])
	}
	action, ok := turn0["action"].(map[string]any)
	if !ok {
		t.Fatalf("transcript[0].action = %v, want an object", turn0["action"])
	}
	if action["action"] != "run_command" || action["command"] != "echo marker-abc" {
		t.Errorf("transcript[0].action = %v", action)
	}
}

// A turn whose reply never parsed has no action; emitting a zero-valued
// one would read as a real, empty action.
func TestReportJSONOmitsActionForUnparsedTurn(t *testing.T) {
	f := newFakeSLM(t, "not json at all", replyPass)
	report := run(t, f, testSpec(t, step(1, "one")))

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got jsonReport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	first := got.Steps[0].Transcript[0]
	if first.Action != nil {
		t.Errorf("action = %+v, want omitted for an unparsed reply", first.Action)
	}
	if first.Err == "" {
		t.Error("error field is empty, want the parse error")
	}
	if first.RawReply != "not json at all" {
		t.Errorf("raw_reply = %q", first.RawReply)
	}
}

func TestReportJSONStatusForTimeoutAndAbort(t *testing.T) {
	f := newFakeSLM(t, replyAbort)
	report := run(t, f, testSpec(t, step(1, "one")))

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got jsonReport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.Aborted || got.Passed {
		t.Errorf("aborted = %v, passed = %v; want true, false", got.Aborted, got.Passed)
	}
	if got.Steps[0].Status != StatusAbort {
		t.Errorf("steps[0].status = %q, want abort", got.Steps[0].Status)
	}
}

// CommandWaitMS is the fallback when the model omits wait_ms; a model that
// specifies one must still win.
func TestCommandWaitMSIsAFallbackNotAnOverride(t *testing.T) {
	noWait := `{"action":"run_command","command":"echo marker-abc"}`
	f := newFakeSLM(t, noWait, replyPass)

	start := time.Now()
	report, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(),
		Options{CommandWaitMS: 300})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	if !report.Passed {
		t.Fatalf("Passed = false: %s", report.Steps[0].Reason)
	}
	// The built-in default is 1500ms; a respected 300ms override finishes
	// comfortably under that.
	if elapsed > time.Second {
		t.Errorf("run took %v, want the 300ms CommandWaitMS override to apply", elapsed)
	}
}

func TestContinueOnFailAttemptsEveryStep(t *testing.T) {
	f := newFakeSLM(t, replyFail, replyPass, replyFail)
	report, err := Run(context.Background(), testSpec(t, step(1, "one"), step(2, "two"), step(3, "three")),
		f.client(), Options{ContinueOnFail: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(report.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want all 3 attempted", len(report.Steps))
	}
	if report.Passed {
		t.Error("Passed = true, want false — a later pass must not clear an earlier failure")
	}
	want := []StepStatus{StatusFail, StatusPass, StatusFail}
	for i, w := range want {
		if got := report.Steps[i].Status(); got != w {
			t.Errorf("step %d status = %q, want %q", i+1, got, w)
		}
	}
}

// An abort means the environment is unusable, so it ends the run even
// under ContinueOnFail — continuing would just produce noise.
func TestContinueOnFailStillStopsAtAbort(t *testing.T) {
	f := newFakeSLM(t, replyFail, replyAbort)
	report, err := Run(context.Background(), testSpec(t, step(1, "one"), step(2, "two"), step(3, "three")),
		f.client(), Options{ContinueOnFail: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(report.Steps) != 2 {
		t.Errorf("len(Steps) = %d, want 2 — the run must stop at the abort", len(report.Steps))
	}
	if !report.Aborted {
		t.Error("Aborted = false, want true")
	}
}

// Default behavior is unchanged: without the option, the first failure
// still ends the run.
func TestWithoutContinueOnFailFirstFailureStops(t *testing.T) {
	f := newFakeSLM(t, replyFail)
	report := run(t, f, testSpec(t, step(1, "one"), step(2, "two")))

	if len(report.Steps) != 1 {
		t.Errorf("len(Steps) = %d, want 1", len(report.Steps))
	}
}
