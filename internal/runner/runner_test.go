package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjhorn/slmtest/internal/agent"
	"github.com/sjhorn/slmtest/internal/ptydriver"
	"github.com/sjhorn/slmtest/internal/spec"
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

// A step that takes many turns (e.g. repeatedly waiting on genuinely slow
// real work) must not grow its own request size without bound — see
// maxStepHistoryTurns's doc comment in runner.go for why this matters
// against a real full-screen TUI. Script more wait turns than the cap
// allows and confirm the request sent to the model stops growing once the
// cap kicks in, and that the oldest retained message carries a note
// explaining older turns were dropped.
// A single turn's own PTY output can be large enough to blow a context
// window on its own, independent of how many turns have accumulated — a
// real full-screen TUI redraw is dense enough with ANSI escape codes that
// this happened live against a 32768-token context in exactly one turn.
// The model should be shown the most recent (tail) portion, since that is
// the current state, with a note explaining older bytes from the SAME
// turn were dropped.
func TestSingleTurnOutputIsTruncated(t *testing.T) {
	// The command itself stays short — it's the *output* that's huge, read
	// back from the PTY rather than typed into it, since typing a string
	// this large in one go can itself exceed the terminal's own input
	// queue. yes+head is a small, portable way to generate a lot of output
	// fast; the marker confirms the tail specifically survived.
	n := maxSingleTurnOutputChars * 3
	tail := "END-OF-OUTPUT-MARKER"
	cmd := fmt.Sprintf("yes x | head -c %d; printf '%%s' %s", n, tail)
	f := newFakeSLM(t,
		fmt.Sprintf(`{"action":"run_command","command":%q,"wait_ms":800}`, cmd),
		replyPass,
	)
	report := run(t, f, testSpec(t, step(1, "one")))
	if !report.Passed {
		t.Fatalf("Passed = false, want true; reason: %s", report.Steps[0].Reason)
	}

	// The transcript (human-facing evidence) must keep the FULL output —
	// only what the model is shown gets truncated.
	full := report.Steps[0].Transcript[0].PTYOutput
	if !strings.Contains(full, tail) || len(full) < n {
		t.Fatalf("transcript PTYOutput looks truncated or too short (len=%d, want >= %d, contains tail: %v)",
			len(full), n, strings.Contains(full, tail))
	}

	retryPrompt := f.request(1).Messages[len(f.request(1).Messages)-1].Content
	if !strings.Contains(retryPrompt, tail) {
		t.Error("the tail of a large single turn's output should survive truncation")
	}
	if !strings.Contains(retryPrompt, "were dropped to control context size") {
		t.Error("no truncation note found in a prompt built from oversized single-turn output")
	}
	if len(retryPrompt) >= n {
		t.Errorf("prompt len = %d, want well under the untruncated output's %d", len(retryPrompt), n)
	}
}

func TestStepHistoryIsTrimmedAfterManyTurns(t *testing.T) {
	const waitTurns = maxStepHistoryTurns + 4
	replyWait := `{"action":"wait","wait_ms":10}`
	replies := make([]string, 0, waitTurns+1)
	for i := 0; i < waitTurns; i++ {
		replies = append(replies, replyWait)
	}
	replies = append(replies, replyPass)

	f := newFakeSLM(t, replies...)
	ts := testSpec(t, step(1, "one"))
	ts.MaxTurnsPerStep = waitTurns + 1
	report := run(t, f, ts)

	if !report.Passed {
		t.Fatalf("Passed = false, want true; reason: %s", report.Steps[0].Reason)
	}

	// Once the cap has kicked in, every later request's history should sit
	// at the same bounded size rather than keep growing turn over turn.
	last := f.request(f.calls() - 1).Messages
	secondToLast := f.request(f.calls() - 2).Messages
	if len(last) != len(secondToLast) {
		t.Errorf("request size still growing near the end of a long step: %d vs %d messages",
			len(secondToLast), len(last))
	}
	// system + at most maxStepHistoryTurns*2 history messages + the current
	// user turn.
	maxExpected := 1 + maxStepHistoryTurns*2 + 1
	if len(last) > maxExpected {
		t.Errorf("len(Messages) = %d, want at most %d", len(last), maxExpected)
	}

	foundNote := false
	for _, m := range last {
		if strings.Contains(m.Content, "earlier turn(s) in this step were dropped") {
			foundNote = true
			break
		}
	}
	if !foundNote {
		t.Error("no trimmed-history note found in a request that should have exceeded the cap")
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

func TestFirstStepHasNoPriorSummary(t *testing.T) {
	f := newFakeSLM(t, replyPass)
	run(t, f, testSpec(t, step(1, "one")))

	msgs := f.request(0).Messages
	if got := msgs[len(msgs)-1].Content; !strings.HasPrefix(got, "STEP 1:") {
		t.Errorf("first step's prompt has a preamble it should not:\n%s", got)
	}
}

// Per-step history is reset, so without this a step like "restart the
// service you configured earlier" would be unanswerable.
func TestLaterStepsSeePriorOutcomes(t *testing.T) {
	f := newFakeSLM(t, replyEcho, replyPass, replyPass)
	run(t, f, testSpec(t, step(1, "one"), step(2, "two")))

	msgs := f.request(2).Messages
	prompt := msgs[len(msgs)-1].Content
	if !strings.Contains(prompt, "Step 1 (one): pass — saw the marker") {
		t.Errorf("step 2's prompt is missing step 1's outcome:\n%s", prompt)
	}
	if !strings.Contains(prompt, "STEP 2: two") {
		t.Errorf("step 2's prompt lost its own step text:\n%s", prompt)
	}
	// Verdicts and reasons only. Replaying terminal output would defeat
	// the per-step reset this summary is an exception to.
	if strings.Contains(prompt, "marker-abc") {
		t.Errorf("prior-step terminal output leaked into step 2's prompt:\n%s", prompt)
	}
}

func TestPriorSummaryCarriesFailuresAndAborts(t *testing.T) {
	f := newFakeSLM(t, replyFail, replyPass)
	_, err := Run(context.Background(), testSpec(t, step(1, "one"), step(2, "two")),
		f.client(), Options{ContinueOnFail: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	msgs := f.request(1).Messages
	prompt := msgs[len(msgs)-1].Content
	if !strings.Contains(prompt, "Step 1 (one): fail — marker never appeared") {
		t.Errorf("step 2's prompt did not report step 1's failure:\n%s", prompt)
	}
}

// The summary must stay bounded, or a long spec grows every prompt without
// limit.
func TestPriorSummaryIsCapped(t *testing.T) {
	const steps = 8
	replies := make([]string, steps)
	specSteps := make([]spec.Step, steps)
	for i := range replies {
		replies[i] = replyPass
		specSteps[i] = step(i+1, fmt.Sprintf("s%d", i+1))
	}
	f := newFakeSLM(t, replies...)
	run(t, f, testSpec(t, specSteps...))

	msgs := f.request(steps - 1).Messages
	prompt := msgs[len(msgs)-1].Content

	// The last step should see steps 3-7 and not the earlier ones.
	for _, want := range []string{"Step 3 (s3)", "Step 7 (s7)"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("final prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"Step 1 (s1)", "Step 2 (s2)"} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("final prompt still carries %q beyond the cap:\n%s", unwanted, prompt)
		}
	}
	if got := strings.Count(prompt, "  Step "); got != maxPriorOutcomes {
		t.Errorf("summary lists %d outcomes, want the cap of %d", got, maxPriorOutcomes)
	}
}

// The summary is the deliberate exception to the per-step reset; it must
// not become a way for full transcripts to leak forward.
func TestPriorSummaryDoesNotGrowTheMessageCount(t *testing.T) {
	f := newFakeSLM(t, replyEcho, replyPass, replyPass)
	run(t, f, testSpec(t, step(1, "one"), step(2, "two")))

	if got := len(f.request(2).Messages); got != 2 {
		t.Errorf("step 2's first request had %d messages, want 2 (system + one user prompt)", got)
	}
}

func TestShellEnv(t *testing.T) {
	if got := shellEnv(""); got != nil {
		t.Errorf("shellEnv(\"\") = %v, want nil (inherit the parent environment)", got)
	}

	got := shellEnv("xterm-256color")
	if len(got) == 0 {
		t.Fatal("shellEnv returned an empty environment")
	}
	var terms []string
	for _, kv := range got {
		if strings.HasPrefix(kv, "TERM=") {
			terms = append(terms, kv)
		}
	}
	// Setting exec.Cmd.Env replaces the environment wholesale, so the
	// parent's other variables must survive — and exactly one TERM must
	// remain, even if the parent already had one.
	if len(terms) != 1 || terms[0] != "TERM=xterm-256color" {
		t.Errorf("TERM entries = %v, want exactly [TERM=xterm-256color]", terms)
	}
	if len(got) < len(os.Environ()) {
		t.Errorf("shellEnv dropped parent variables: %d entries vs %d in os.Environ()", len(got), len(os.Environ()))
	}
}

// A step's Size applies to that step only — a single TUI step must not
// silently reshape the rest of the run.
func TestPerStepSizeAppliesAndReverts(t *testing.T) {
	sizeProbe := `{"action":"run_command","command":"stty size","wait_ms":400}`
	f := newFakeSLM(t, sizeProbe, replyPass, sizeProbe, replyPass, sizeProbe, replyPass)

	ts := testSpec(t, step(1, "one"), step(2, "two"), step(3, "three"))
	ts.Size = spec.Size{Rows: 40, Cols: 200}
	ts.Steps[1].Size = spec.Size{Rows: 24, Cols: 80}

	report := run(t, f, ts)
	if !report.Passed {
		t.Fatalf("Passed = false: %+v", report.Steps)
	}

	want := []string{"40 200", "24 80", "40 200"}
	for i, w := range want {
		got := report.Steps[i].Transcript[0].PTYOutput
		if !strings.Contains(got, w) {
			t.Errorf("step %d terminal size = %q, want it to report %q", i+1, got, w)
		}
	}
}

func TestTestWideSizeApplies(t *testing.T) {
	sizeProbe := `{"action":"run_command","command":"stty size","wait_ms":400}`
	f := newFakeSLM(t, sizeProbe, replyPass)

	ts := testSpec(t, step(1, "one"))
	ts.Size = spec.Size{Rows: 30, Cols: 100}

	report := run(t, f, ts)
	if got := report.Steps[0].Transcript[0].PTYOutput; !strings.Contains(got, "30 100") {
		t.Errorf("terminal size = %q, want 30 100", got)
	}
}

// An unspecified size falls back to the driver's default rather than to
// a zero-sized terminal.
func TestUnspecifiedSizeUsesDriverDefault(t *testing.T) {
	sizeProbe := `{"action":"run_command","command":"stty size","wait_ms":400}`
	f := newFakeSLM(t, sizeProbe, replyPass)

	report := run(t, f, testSpec(t, step(1, "one")))
	want := fmt.Sprintf("%d %d", ptydriver.DefaultRows, ptydriver.DefaultCols)
	if got := report.Steps[0].Transcript[0].PTYOutput; !strings.Contains(got, want) {
		t.Errorf("terminal size = %q, want the %s default", got, want)
	}
}

// The prefix wraps the shell rather than replacing it. Using env keeps
// this test hermetic — the mechanism is identical for `docker run`, which
// is just a longer argv.
func TestExecPrefixWrapsTheShell(t *testing.T) {
	probe := `{"action":"run_command","command":"echo prefix=$SLMTEST_PREFIX","wait_ms":400}`
	f := newFakeSLM(t, probe, replyPass)

	report, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(),
		Options{ExecPrefix: []string{"/usr/bin/env", "SLMTEST_PREFIX=applied"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Passed {
		t.Fatalf("Passed = false: %s", report.Steps[0].Reason)
	}
	if got := report.Steps[0].Transcript[0].PTYOutput; !strings.Contains(got, "prefix=applied") {
		t.Errorf("output = %q, want the prefixed shell to have run", got)
	}
}

// The spec's own shell still decides what runs inside the sandbox.
func TestExecPrefixKeepsTheSpecShell(t *testing.T) {
	probe := `{"action":"run_command","command":"echo shell=$0","wait_ms":400}`
	f := newFakeSLM(t, probe, replyPass)

	ts := testSpec(t, step(1, "one"))
	ts.Shell = "/bin/sh"
	report, err := Run(context.Background(), ts, f.client(),
		Options{ExecPrefix: []string{"/usr/bin/env"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := report.Steps[0].Transcript[0].PTYOutput; !strings.Contains(got, "/bin/sh") {
		t.Errorf("output = %q, want the spec's shell to be what ran", got)
	}
}

func TestExecPrefixFailureIsReported(t *testing.T) {
	f := newFakeSLM(t)
	_, err := Run(context.Background(), testSpec(t, step(1, "one")), f.client(),
		Options{ExecPrefix: []string{"/nonexistent/sandbox"}})
	if err == nil {
		t.Fatal("Run succeeded with an unstartable exec prefix, want error")
	}
}

// A real 1.5B model set press_enter:false on a run_command, which used to
// be honored: the text was typed, nothing ran, no output ever appeared,
// and the model burned its whole turn budget waiting for a result that
// could not arrive. run_command means "type and press Enter", so the
// field is ignored there.
//
// These tests use a command whose output differs from its own text
// (`echo $((6*7))` prints 42) because the terminal echoes typed input and
// redraws it after a prompt — so counting occurrences of the command text
// cannot distinguish "typed" from "executed", but the presence of 42 can.
const arithmeticProbe = `echo $((6*7))`

func TestRunCommandAlwaysPressesEnter(t *testing.T) {
	refusesEnter := `{"action":"run_command","command":"` + arithmeticProbe + `","press_enter":false,"wait_ms":500}`
	f := newFakeSLM(t, refusesEnter, replyPass)

	report := run(t, f, testSpec(t, step(1, "one")))
	if !report.Passed {
		t.Fatalf("Passed = false: %s", report.Steps[0].Reason)
	}
	if got := report.Steps[0].Transcript[0].PTYOutput; !strings.Contains(got, "42") {
		t.Errorf("command did not execute despite run_command; PTY output = %q", got)
	}
}

// send_keys is where withholding Enter is meaningful, so the field still
// works there — in both directions.
func TestSendKeysHonorsPressEnter(t *testing.T) {
	withhold := `{"action":"send_keys","command":"` + arithmeticProbe + `","wait_ms":500}`
	f := newFakeSLM(t, withhold, replyPass)
	report := run(t, f, testSpec(t, step(1, "one")))
	if got := report.Steps[0].Transcript[0].PTYOutput; strings.Contains(got, "42") {
		t.Errorf("send_keys executed the command without being asked to: %q", got)
	}

	send := `{"action":"send_keys","command":"` + arithmeticProbe + `","press_enter":true,"wait_ms":500}`
	f2 := newFakeSLM(t, send, replyPass)
	report2 := run(t, f2, testSpec(t, step(1, "one")))
	if got := report2.Steps[0].Transcript[0].PTYOutput; !strings.Contains(got, "42") {
		t.Errorf("send_keys with press_enter:true did not execute: %q", got)
	}
}

// A 1.5B model was observed running the same correct command four times
// against unchanged output, with the Expect criterion plainly satisfied,
// until its budget ran out. The harness cannot judge the step for it, but
// it can point out what the model is demonstrably not noticing.
func TestRepeatedActionIsCalledOut(t *testing.T) {
	same := `{"action":"run_command","command":"echo marker-abc","wait_ms":400}`
	f := newFakeSLM(t, same, same, same, replyPass)

	ts := testSpec(t, step(1, "one"))
	ts.MaxTurnsPerStep = 4
	run(t, f, ts)

	// Turn 2's prompt follows the first repeat.
	second := f.request(1).Messages
	if got := second[len(second)-1].Content; strings.Contains(got, "NOTE: you have now run") {
		t.Errorf("nudged on the first run of a command:\n%s", got)
	}

	third := f.request(2).Messages
	got := third[len(third)-1].Content
	if !strings.Contains(got, "run that exact command 2 times in a row") {
		t.Errorf("no nudge after one repeat:\n%s", got)
	}
	if !strings.Contains(got, "finish_step") {
		t.Errorf("nudge does not say what to do instead:\n%s", got)
	}
	// A nudge must never name the verdict — see repeatNudge's doc comment.
	if strings.Contains(got, `step_result "pass"`) {
		t.Errorf("nudge supplies a verdict for the model:\n%s", got)
	}

	fourth := f.request(3).Messages
	if !strings.Contains(fourth[len(fourth)-1].Content, "3 times in a row") {
		t.Errorf("repeat count did not increase:\n%s", fourth[len(fourth)-1].Content)
	}
}

// Varying the command must reset the counter — otherwise a model working
// through a legitimate sequence would be told it is looping.
func TestVaryingCommandsAreNotNudged(t *testing.T) {
	a := `{"action":"run_command","command":"echo one","wait_ms":300}`
	b := `{"action":"run_command","command":"echo two","wait_ms":300}`
	f := newFakeSLM(t, a, b, a, replyPass)

	ts := testSpec(t, step(1, "one"))
	ts.MaxTurnsPerStep = 4
	run(t, f, ts)

	for i := 1; i < 4; i++ {
		msgs := f.request(i).Messages
		if got := msgs[len(msgs)-1].Content; strings.Contains(got, "NOTE: you have now run") {
			t.Errorf("turn %d was nudged despite the command changing:\n%s", i+1, got)
		}
	}
}

// The action must still execute, with the correction delivered alongside
// its output — rejecting instead was tried against a real 1.5B model and
// stopped any command from running at all.
func TestStrayVerdictIsAnnotatedNotRejected(t *testing.T) {
	stray := `{"action":"run_command","command":"` + arithmeticProbe + `","step_result":"pass","wait_ms":500}`
	f := newFakeSLM(t, stray, replyPass)

	report := run(t, f, testSpec(t, step(1, "one")))
	if !report.Passed {
		t.Fatalf("Passed = false: %s", report.Steps[0].Reason)
	}
	if got := report.Steps[0].Transcript[0].PTYOutput; !strings.Contains(got, "42") {
		t.Errorf("the command did not run; PTY output = %q", got)
	}
	if got := report.Steps[0].Transcript[0].Err; got != "" {
		t.Errorf("turn recorded an error %q, want the action tolerated", got)
	}

	msgs := f.request(1).Messages
	prompt := msgs[len(msgs)-1].Content
	if !strings.Contains(prompt, "only finish_step carries a verdict") {
		t.Errorf("next prompt did not name the mistake:\n%s", prompt)
	}
	if !strings.Contains(prompt, "finish_step") {
		t.Errorf("next prompt did not name the correct action:\n%s", prompt)
	}
	// It must explain the mechanism without answering the question: an
	// earlier version echoed the model's own claimed verdict back as the
	// suggested reply and coached a 0.5B model into a false pass.
	if strings.Contains(prompt, `"step_result": "pass"`) {
		t.Errorf("stray-verdict note supplies a verdict for the model:\n%s", prompt)
	}
}

func TestNoStrayVerdictNoteWhenVerdictIsAbsent(t *testing.T) {
	f := newFakeSLM(t, replyEcho, replyPass)
	run(t, f, testSpec(t, step(1, "one")))

	msgs := f.request(1).Messages
	if got := msgs[len(msgs)-1].Content; strings.Contains(got, "only finish_step ends a step") {
		t.Errorf("annotated a well-formed action:\n%s", got)
	}
}

// No harness nudge may ever name a verdict. The one judgement this tool
// delegates is pass/fail, and a 0.5B model was coached into a false pass
// by a note that echoed its own claimed step_result back as the reply to
// send.
func TestNudgesNeverSupplyAVerdict(t *testing.T) {
	notes := []string{
		strayVerdictNote(agent.Action{Action: agent.ActionSendKeys, Command: "x", StepResult: agent.ResultPass}),
		strayVerdictNote(agent.Action{Action: agent.ActionRunCommand, Command: "x", StepResult: agent.ResultFail}),
		repeatNudge(1),
		repeatNudge(4),
		notExecutedNote(agent.Action{Action: agent.ActionSendKeys, Command: "x"}, false),
	}
	for _, n := range notes {
		if n == "" {
			t.Error("expected a note, got none")
			continue
		}
		// Naming both options is fine; naming one is the harness deciding.
		hasPass := strings.Contains(n, `"pass"`)
		hasFail := strings.Contains(n, `"fail"`)
		if hasPass != hasFail {
			t.Errorf("note names one verdict but not the other, which leans the model:\n%s", n)
		}
	}
}

// send_keys types without executing. A 0.5B model used it for a whole
// command, saw the terminal echo its own input, and called that output.
func TestSendKeysWithoutEnterIsCalledOut(t *testing.T) {
	typed := `{"action":"send_keys","command":"` + arithmeticProbe + `","wait_ms":500}`
	f := newFakeSLM(t, typed, replyPass)
	run(t, f, testSpec(t, step(1, "one")))

	msgs := f.request(1).Messages
	prompt := msgs[len(msgs)-1].Content
	if !strings.Contains(prompt, "has NOT run") {
		t.Errorf("model was not told the text never executed:\n%s", prompt)
	}
	if !strings.Contains(prompt, "echoing your input") {
		t.Errorf("model was not warned the echo is not output:\n%s", prompt)
	}
}

func TestNoNotExecutedNoteWhenEnterWasPressed(t *testing.T) {
	for _, reply := range []string{
		`{"action":"run_command","command":"echo hi","wait_ms":400}`,
		`{"action":"send_keys","command":"echo hi","press_enter":true,"wait_ms":400}`,
	} {
		f := newFakeSLM(t, reply, replyPass)
		run(t, f, testSpec(t, step(1, "one")))
		msgs := f.request(1).Messages
		if got := msgs[len(msgs)-1].Content; strings.Contains(got, "has NOT run") {
			t.Errorf("warned about non-execution after Enter was pressed (%s):\n%s", reply, got)
		}
	}
}
