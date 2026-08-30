package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseActionRunCommand(t *testing.T) {
	got, err := ParseAction(`{"thought":"try echo","action":"run_command","command":"echo hi","wait_ms":500}`)
	if err != nil {
		t.Fatalf("ParseAction: %v", err)
	}
	if got.Action != ActionRunCommand {
		t.Errorf("Action = %q, want run_command", got.Action)
	}
	if got.Command != "echo hi" {
		t.Errorf("Command = %q", got.Command)
	}
	if got.WaitMS != 500 {
		t.Errorf("WaitMS = %d, want 500", got.WaitMS)
	}
	if got.Thought != "try echo" {
		t.Errorf("Thought = %q", got.Thought)
	}
}

// PressEnter is a *bool precisely so "absent" and "explicitly false" are
// distinguishable — the runner's default differs per action type, so
// collapsing them into a plain bool would silently break send_keys.
func TestParseActionPressEnterTristate(t *testing.T) {
	absent, err := ParseAction(`{"action":"run_command","command":"ls"}`)
	if err != nil {
		t.Fatalf("ParseAction: %v", err)
	}
	if absent.PressEnter != nil {
		t.Errorf("PressEnter = %v, want nil when the field is absent", *absent.PressEnter)
	}

	explicit, err := ParseAction(`{"action":"run_command","command":"ls","press_enter":false}`)
	if err != nil {
		t.Fatalf("ParseAction: %v", err)
	}
	if explicit.PressEnter == nil {
		t.Fatal("PressEnter = nil, want a non-nil false")
	}
	if *explicit.PressEnter {
		t.Error("PressEnter = true, want false")
	}
}

// Small models wrap JSON in a code fence even when told not to; stripping
// it is cheaper than burning a retry turn on cosmetics.
func TestParseActionStripsCodeFences(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"bare", `{"action":"wait","wait_ms":1000}`},
		{"json fence", "```json\n{\"action\":\"wait\",\"wait_ms\":1000}\n```"},
		{"plain fence", "```\n{\"action\":\"wait\",\"wait_ms\":1000}\n```"},
		{"fence with surrounding whitespace", "\n\n```json\n{\"action\":\"wait\",\"wait_ms\":1000}\n```\n\n"},
		{"uppercase language tag", "```JSON\n{\"action\":\"wait\",\"wait_ms\":1000}\n```"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAction(tc.raw)
			if err != nil {
				t.Fatalf("ParseAction: %v", err)
			}
			if got.Action != ActionWait || got.WaitMS != 1000 {
				t.Errorf("got {%q, %d}, want {wait, 1000}", got.Action, got.WaitMS)
			}
		})
	}
}

func TestParseActionValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"prose instead of JSON", "Sure! I'll run `ls` for you.", "not valid JSON"},
		{"empty reply", "", "not valid JSON"},
		{"unknown action", `{"action":"reboot"}`, "unknown action type"},
		{"run_command without command", `{"action":"run_command"}`, `requires a non-empty "command"`},
		{"send_keys without command", `{"action":"send_keys"}`, `requires a non-empty "command"`},
		{"finish_step without result", `{"action":"finish_step","reason":"done"}`, `"step_result"`},
		{"finish_step with bad result", `{"action":"finish_step","step_result":"maybe","reason":"done"}`, `must be "pass" or "fail"`},
		{"finish_step without reason", `{"action":"finish_step","step_result":"pass"}`, `requires a non-empty "reason"`},
		{"abort_test without reason", `{"action":"abort_test"}`, `requires a non-empty "reason"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAction(tc.raw)
			if err == nil {
				t.Fatalf("ParseAction(%q) succeeded, want error", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
			// The runner feeds this string straight back to the model as
			// the next user turn, so it must be a plain actionable message.
			if _, ok := err.(*SchemaError); !ok {
				t.Errorf("error type = %T, want *SchemaError", err)
			}
		})
	}
}

func TestParseActionValidVerdicts(t *testing.T) {
	for _, result := range []StepResult{ResultPass, ResultFail} {
		raw := `{"action":"finish_step","step_result":"` + string(result) + `","reason":"because"}`
		got, err := ParseAction(raw)
		if err != nil {
			t.Fatalf("ParseAction(%s): %v", result, err)
		}
		if got.StepResult != result {
			t.Errorf("StepResult = %q, want %q", got.StepResult, result)
		}
	}
}

func TestCompleteBuildsRequestAndReturnsContent(t *testing.T) {
	var gotReq chatRequest
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"REPLY"}}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/v1", "test-model", "secret-token")
	got, err := c.Complete(context.Background(), Turn{
		System:   "SYS",
		History:  []Message{{Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"}},
		UserText: "u2",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "REPLY" {
		t.Errorf("Complete = %q, want REPLY", got)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotReq.Model != "test-model" {
		t.Errorf("model = %q, want test-model", gotReq.Model)
	}
	// The system prompt is prepended and the new user text appended, with
	// this step's history sandwiched between in order.
	wantRoles := []string{"system", "user", "assistant", "user"}
	if len(gotReq.Messages) != len(wantRoles) {
		t.Fatalf("len(messages) = %d, want %d", len(gotReq.Messages), len(wantRoles))
	}
	for i, want := range wantRoles {
		if gotReq.Messages[i].Role != want {
			t.Errorf("messages[%d].Role = %q, want %q", i, gotReq.Messages[i].Role, want)
		}
	}
	if gotReq.Messages[0].Content != "SYS" || gotReq.Messages[3].Content != "u2" {
		t.Errorf("messages = %+v", gotReq.Messages)
	}
	// json_object mode is what keeps compliant servers on-contract.
	if gotReq.ResponseFormat["type"] != "json_object" {
		t.Errorf("response_format = %v, want type=json_object", gotReq.ResponseFormat)
	}
}

func TestCompleteOmitsAuthHeaderWithoutKey(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL+"/v1", "m", "").Complete(context.Background(), Turn{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if hadAuth {
		t.Error("Authorization header was sent even though no API key is configured")
	}
}

func TestCompleteEndpointErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{
			name: "error object in body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"model not loaded"}}`))
			},
			wantErr: "model not loaded",
		},
		{
			name: "no choices",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"choices":[]}`))
			},
			wantErr: "no choices",
		},
		{
			// A 200 whose body isn't JSON: a proxy error page, or a
			// truncated response. The server answered, so this is not
			// retried — 5xx and 429 are the transient cases, covered in
			// TestCompleteRetries.
			name: "non-JSON body with an OK status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("<html>hello</html>"))
			},
			wantErr: "non-JSON response",
		},
		{
			name: "bad request is not retried",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"unknown model"}}`))
			},
			wantErr: "unknown model",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			_, err := fastRetryClient(srv.URL).Complete(context.Background(), Turn{})
			if err == nil {
				t.Fatal("Complete succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestCompleteReportsTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nothing is listening now

	c := NewClient(srv.URL+"/v1", "m", "")
	c.Retry.MaxAttempts = 1 // retry behavior has its own tests below

	_, err := c.Complete(context.Background(), Turn{})
	if err == nil {
		t.Fatal("Complete succeeded against a closed server, want error")
	}
	if !strings.Contains(err.Error(), "calling SLM endpoint") {
		t.Errorf("error = %q, want it to mention calling the SLM endpoint", err)
	}
}

// fastRetryClient keeps the real retry ladder but shrinks the delays so
// tests exercise the logic without sleeping through it.
func fastRetryClient(baseURL string) *Client {
	c := NewClient(baseURL+"/v1", "m", "")
	c.Retry.BaseDelay = time.Millisecond
	c.Retry.MaxDelay = 2 * time.Millisecond
	return c
}

func TestCompleteRetries(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		retryAfter  string
		wantCalls   int
		wantSuccess bool
	}{
		{name: "server error is retried", status: http.StatusInternalServerError, wantCalls: 3, wantSuccess: true},
		{name: "bad gateway is retried", status: http.StatusBadGateway, wantCalls: 3, wantSuccess: true},
		{name: "rate limit is retried", status: http.StatusTooManyRequests, wantCalls: 3, wantSuccess: true},
		{name: "request timeout is retried", status: http.StatusRequestTimeout, wantCalls: 3, wantSuccess: true},
		{name: "honors Retry-After", status: http.StatusTooManyRequests, retryAfter: "0", wantCalls: 3, wantSuccess: true},
		// The request itself was rejected; sending it again unchanged gets
		// the same answer, so it must fail on the first attempt.
		{name: "unauthorized is not retried", status: http.StatusUnauthorized, wantCalls: 1},
		{name: "not found is not retried", status: http.StatusNotFound, wantCalls: 1},
		{name: "bad request is not retried", status: http.StatusBadRequest, wantCalls: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			var mu sync.Mutex
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				n := calls
				mu.Unlock()

				// Succeed on the third attempt so a retried status ends up
				// returning real content rather than just failing slower.
				if tc.wantSuccess && n >= 3 {
					_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"recovered"}}]}`))
					return
				}
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("upstream is unhappy"))
			}))
			defer srv.Close()

			got, err := fastRetryClient(srv.URL).Complete(context.Background(), Turn{})

			mu.Lock()
			gotCalls := calls
			mu.Unlock()
			if gotCalls != tc.wantCalls {
				t.Errorf("endpoint called %d times, want %d", gotCalls, tc.wantCalls)
			}
			if tc.wantSuccess {
				if err != nil {
					t.Fatalf("Complete: %v", err)
				}
				if got != "recovered" {
					t.Errorf("Complete = %q, want recovered", got)
				}
				return
			}
			if err == nil {
				t.Fatal("Complete succeeded, want error")
			}
		})
	}
}

// When every attempt fails, the error must say so — an operator reading
// "connection refused" should know it was not a one-off blip.
func TestCompleteExhaustsAttempts(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := fastRetryClient(srv.URL).Complete(context.Background(), Turn{})
	if err == nil {
		t.Fatal("Complete succeeded, want error")
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("error = %q, want it to report the exhausted attempts", err)
	}
	if !strings.Contains(err.Error(), "HTTP 503") {
		t.Errorf("error = %q, want it to preserve the underlying failure", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Errorf("endpoint called %d times, want 3", calls)
	}
}

// A cancelled context is the caller giving up — a step or whole-test
// timeout. Retrying past it would blow the very budget that fired.
func TestCompleteStopsRetryingOnContextCancellation(t *testing.T) {
	var calls int
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		cancel() // the budget fires while the first attempt is in flight
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := fastRetryClient(srv.URL)
	c.Retry.BaseDelay = time.Second // a retry would be plainly visible
	if _, err := c.Complete(ctx, Turn{}); err == nil {
		t.Fatal("Complete succeeded, want error")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("endpoint called %d times, want 1 — cancellation must stop the ladder", calls)
	}
}

func TestRetryDisabledWithSingleAttempt(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := fastRetryClient(srv.URL)
	c.Retry.MaxAttempts = 1
	if _, err := c.Complete(context.Background(), Turn{}); err == nil {
		t.Fatal("Complete succeeded, want error")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("endpoint called %d times, want 1", calls)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"2", 2 * time.Second},
		{" 5 ", 5 * time.Second},
		{"-1", 0},
		{"Wed, 21 Oct 2015 07:28:00 GMT", 0}, // HTTP-date form: fall back to backoff
		{"nonsense", 0},
		{"600", 30 * time.Second}, // capped, so one header can't park the run
	}
	for _, tc := range tests {
		if got := parseRetryAfter(tc.in); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	c := NewClient("http://x/v1", "m", "")
	c.Retry.BaseDelay = 100 * time.Millisecond
	c.Retry.MaxDelay = 400 * time.Millisecond

	// Jitter makes each delay a range, so assert the bounds rather than an
	// exact value: half the nominal delay, plus up to half again.
	for _, tc := range []struct {
		n       int
		nominal time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{9, 400 * time.Millisecond}, // capped
	} {
		for i := 0; i < 50; i++ {
			got := c.backoff(tc.n)
			if got < tc.nominal/2 || got > tc.nominal {
				t.Fatalf("backoff(%d) = %v, want within [%v, %v]", tc.n, got, tc.nominal/2, tc.nominal)
			}
		}
	}

	c.Retry.BaseDelay = 0
	if got := c.backoff(1); got != 0 {
		t.Errorf("backoff with no BaseDelay = %v, want 0", got)
	}
}

func TestRequestTimeoutIsConfigurable(t *testing.T) {
	c := NewClient("http://x/v1", "m", "")
	if c.HTTP.Timeout != DefaultRequestTimeout {
		t.Errorf("default timeout = %v, want %v", c.HTTP.Timeout, DefaultRequestTimeout)
	}

	c.SetRequestTimeout(5 * time.Second)
	if c.HTTP.Timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", c.HTTP.Timeout)
	}

	// Must not panic on a client built without an http.Client.
	bare := &Client{BaseURL: "http://x/v1"}
	bare.SetRequestTimeout(time.Second)
	if bare.HTTP == nil || bare.HTTP.Timeout != time.Second {
		t.Errorf("SetRequestTimeout did not initialize a nil HTTP client")
	}
}

// A model that is merely slow must not be mistaken for a broken endpoint.
func TestSlowResponseWithinTimeoutSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"slow but fine"}}]}`))
	}))
	defer srv.Close()

	c := fastRetryClient(srv.URL)
	c.SetRequestTimeout(5 * time.Second)

	got, err := c.Complete(context.Background(), Turn{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "slow but fine" {
		t.Errorf("Complete = %q", got)
	}
}

// A stray verdict is reported, not rejected — rejecting it was tried
// against a real 1.5B model and made things strictly worse.
func TestStrayVerdictIsDetectedNotRejected(t *testing.T) {
	for _, raw := range []string{
		`{"action":"run_command","command":"ls","step_result":"pass"}`,
		`{"action":"send_keys","command":"y","step_result":"fail"}`,
		`{"action":"wait","wait_ms":1000,"step_result":"pass"}`,
	} {
		got, err := ParseAction(raw)
		if err != nil {
			t.Errorf("ParseAction(%s) = %v, want it tolerated so the action still runs", raw, err)
			continue
		}
		if !StrayVerdict(got) {
			t.Errorf("StrayVerdict(%s) = false, want it flagged", raw)
		}
	}

	ok, err := ParseAction(`{"action":"finish_step","step_result":"pass","reason":"done"}`)
	if err != nil {
		t.Fatalf("ParseAction: %v", err)
	}
	if StrayVerdict(ok) {
		t.Error("StrayVerdict flagged a verdict on finish_step, where it belongs")
	}
}

// A null step_result is what a large model sent unprompted; it must stay
// harmless rather than becoming a rejected turn.
func TestNullStepResultOnRunCommandIsFine(t *testing.T) {
	got, err := ParseAction(`{"action":"run_command","command":"ls","step_result":null,"reason":null}`)
	if err != nil {
		t.Fatalf("ParseAction: %v", err)
	}
	if got.Action != ActionRunCommand {
		t.Errorf("Action = %q", got.Action)
	}
}

// --- native tool-calling ---

func TestNativeToolsSendsToolsWhenEnabled(t *testing.T) {
	var gotReq struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/v1", "m", "")
	c.NativeTools = true
	if _, err := c.Complete(context.Background(), Turn{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(gotReq.Tools) != 5 {
		t.Fatalf("tools sent = %d, want all 5 actions", len(gotReq.Tools))
	}
	names := map[string]bool{}
	for _, tl := range gotReq.Tools {
		if tl.Type != "function" {
			t.Errorf("tool type = %q, want function", tl.Type)
		}
		names[tl.Function.Name] = true
	}
	for _, want := range []string{"run_command", "send_keys", "wait", "finish_step", "abort_test"} {
		if !names[want] {
			t.Errorf("tools missing %q: %v", want, names)
		}
	}
}

func TestNativeToolsOffByDefaultOmitsTools(t *testing.T) {
	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL+"/v1", "m", "").Complete(context.Background(), Turn{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, ok := raw["tools"]; ok {
		t.Error(`request included "tools" though NativeTools defaults to off`)
	}
}

// A tool_calls response is normalized into exactly the shape ParseAction
// already expects, so nothing downstream needs to know tool-calling was
// used at all.
func TestToolCallResponseIsNormalizedForParseAction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[
			{"function":{"name":"run_command","arguments":"{\"command\":\"echo hi\",\"wait_ms\":500}"}}
		]}}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/v1", "m", "")
	c.NativeTools = true
	reply, err := c.Complete(context.Background(), Turn{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	action, err := ParseAction(reply)
	if err != nil {
		t.Fatalf("ParseAction(%q): %v", reply, err)
	}
	if action.Action != ActionRunCommand || action.Command != "echo hi" || action.WaitMS != 500 {
		t.Errorf("action = %+v", action)
	}
}

// finish_step's tool call must round-trip its required fields too, since
// that is the path a real verdict arrives on.
func TestToolCallFinishStepRoundTrips(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[
			{"function":{"name":"finish_step","arguments":"{\"step_result\":\"pass\",\"reason\":\"it worked\"}"}}
		]}}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/v1", "m", "")
	c.NativeTools = true
	reply, err := c.Complete(context.Background(), Turn{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	action, err := ParseAction(reply)
	if err != nil {
		t.Fatalf("ParseAction(%q): %v", reply, err)
	}
	if action.Action != ActionFinishStep || action.StepResult != ResultPass || action.Reason != "it worked" {
		t.Errorf("action = %+v", action)
	}
}

// A model whose server can't (or wasn't asked to) normalize its own
// native tool-call format into tool_calls — confirmed live against
// xLAM, which emits a bare `[{"name":...,"arguments":{...}}]` array —
// must still produce a usable Action.
func TestNativeArrayContentIsRecognized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := json.Marshal(map[string]string{
			"content": `[{"name": "run_command", "arguments": {"command": "echo hi"}}]`,
		})
		_, _ = w.Write([]byte(`{"choices":[{"message":` + string(body) + `}]}`))
	}))
	defer srv.Close()

	reply, err := NewClient(srv.URL+"/v1", "m", "").Complete(context.Background(), Turn{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	action, err := ParseAction(reply)
	if err != nil {
		t.Fatalf("ParseAction(%q): %v", reply, err)
	}
	if action.Action != ActionRunCommand || action.Command != "echo hi" {
		t.Errorf("action = %+v", action)
	}
}

// Content that already matches our own schema must not be reinterpreted
// by the array-recognition heuristic — it doesn't start with '[' at all,
// but this pins the precedence explicitly.
func TestOwnSchemaContentIsNotTouchedByArrayRecognition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"wait\",\"wait_ms\":1000}"}}]}`))
	}))
	defer srv.Close()

	reply, err := NewClient(srv.URL+"/v1", "m", "").Complete(context.Background(), Turn{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if reply != `{"action":"wait","wait_ms":1000}` {
		t.Errorf("reply = %q, want it unchanged", reply)
	}
}

// A server that recognizes but cannot correctly parse a model's native
// tool-call format fails deterministically on every attempt containing
// tools (confirmed live: llama-server 500s on xLAM every single time).
// Complete must fall back to a request without tools rather than
// exhausting the retry ladder on a failure retrying will never fix, and
// remember the outcome so later turns skip straight to the fast path.
// This is the xLAM case: toolsCalling hard-fails (the server can shape
// the prompt but can't parse this model's own response back into
// tool_calls), and toolsPromptOnly — tools still present, tool_choice
// explicitly "none" — succeeds, landing the model's own native format in
// plain content instead. This is the point of the middle tier: it stays
// on the model's own template rather than falling all the way back to
// the prose schema, which is a different system prompt entirely.
func TestFallsBackToToolsPromptOnlyWhenToolCallsFail(t *testing.T) {
	var callsCalling, callsPromptOnly int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools      []json.RawMessage `json:"tools"`
			ToolChoice string            `json:"tool_choice"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)

		mu.Lock()
		defer mu.Unlock()
		if len(req.Tools) > 0 && req.ToolChoice != "none" {
			callsCalling++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"does not match the expected peg-native format"}}`))
			return
		}
		if len(req.Tools) == 0 {
			t.Errorf("toolsPromptOnly request is missing tools entirely: %+v", req)
		}
		callsPromptOnly++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[{\"name\": \"wait\", \"arguments\": {}}]"}}]}`))
	}))
	defer srv.Close()

	c := fastRetryClient(srv.URL)
	c.NativeTools = true

	reply, err := c.Complete(context.Background(), Turn{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if reply != `{"action":"wait"}` {
		t.Errorf("reply = %q, want the model's own native array shape recognized and normalized", reply)
	}

	mu.Lock()
	firstCalling, firstPromptOnly := callsCalling, callsPromptOnly
	mu.Unlock()
	if firstCalling != c.Retry.MaxAttempts {
		t.Errorf("toolsCalling attempts = %d, want the full retry ladder (%d) exhausted first", firstCalling, c.Retry.MaxAttempts)
	}
	if firstPromptOnly != 1 {
		t.Errorf("toolsPromptOnly attempts = %d, want exactly 1", firstPromptOnly)
	}

	// A second call must skip straight to toolsPromptOnly — the
	// toolsCalling failure was deterministic, so repeating its whole
	// retry ladder every turn would waste it for nothing every time.
	if _, err := c.Complete(context.Background(), Turn{}); err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if callsCalling != firstCalling {
		t.Errorf("second call re-attempted toolsCalling: attempts = %d, want unchanged at %d", callsCalling, firstCalling)
	}
	if callsPromptOnly != firstPromptOnly+1 {
		t.Errorf("toolsPromptOnly attempts = %d, want exactly one more", callsPromptOnly)
	}
}

// If a server hard-fails on tools in ANY shape — toolsCalling and
// toolsPromptOnly alike — Complete falls all the way back to the
// original prose schema as a last resort.
func TestFallsBackAllTheWayToProseWhenAllToolTiersFail(t *testing.T) {
	var callsWithTools, callsWithoutTools int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []json.RawMessage `json:"tools"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)

		mu.Lock()
		defer mu.Unlock()
		if len(req.Tools) > 0 {
			callsWithTools++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"tools not supported at all"}}`))
			return
		}
		callsWithoutTools++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"wait\"}"}}]}`))
	}))
	defer srv.Close()

	c := fastRetryClient(srv.URL)
	c.NativeTools = true

	reply, err := c.Complete(context.Background(), Turn{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if reply != `{"action":"wait"}` {
		t.Errorf("reply = %q", reply)
	}

	mu.Lock()
	firstWith, firstWithout := callsWithTools, callsWithoutTools
	mu.Unlock()
	// Both tool tiers send `tools`, so both get rejected here, and each
	// gets its own full retry ladder before Complete moves to the next.
	if want := c.Retry.MaxAttempts * 2; firstWith != want {
		t.Errorf("calls with tools = %d, want %d (toolsCalling's and toolsPromptOnly's full ladders)", firstWith, want)
	}
	if firstWithout != 1 {
		t.Errorf("calls without tools = %d, want exactly 1 final fallback attempt", firstWithout)
	}

	// A second call must skip straight to the prose path — both tiers
	// were proven deterministically broken on the first call.
	if _, err := c.Complete(context.Background(), Turn{}); err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if callsWithTools != firstWith {
		t.Errorf("second call re-attempted a tools tier: calls with tools = %d, want unchanged at %d", callsWithTools, firstWith)
	}
	if callsWithoutTools != firstWithout+1 {
		t.Errorf("calls without tools = %d, want exactly one more", callsWithoutTools)
	}
}

// If a server hard-fails on tools ALSO fails without them, the more
// specific tools-related error is the more informative one to report.
func TestFallbackFailureReportsOriginalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"does not match the expected peg-native format"}}`))
	}))
	defer srv.Close()

	c := fastRetryClient(srv.URL)
	c.NativeTools = true

	_, err := c.Complete(context.Background(), Turn{})
	if err == nil {
		t.Fatal("Complete succeeded, want error")
	}
	if !strings.Contains(err.Error(), "peg-native") {
		t.Errorf("error = %q, want the tools-related failure reported", err)
	}
}

// The tools path REPLACES the caller's system prompt rather than
// supplementing it. Reproduced live and with certainty (see
// docs/model-runs.md): sending both together measurably broke
// Qwen2.5-1.5B's tool selection — instead of a proper tool_calls
// response, it produced a degraded {"name":...,"arguments":{...}} object
// in plain content, because the prose schema's "reply with EXACTLY this
// JSON shape" competes with the tool definitions' own shape.
func TestToolsPathReplacesSystemPromptEntirely(t *testing.T) {
	var gotReq chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/v1", "m", "")
	c.NativeTools = true
	if _, err := c.Complete(context.Background(), Turn{System: "reply with EXACTLY this JSON shape: ..."}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(gotReq.Messages) == 0 || gotReq.Messages[0].Role != "system" {
		t.Fatalf("messages = %+v, want a leading system message", gotReq.Messages)
	}
	if strings.Contains(gotReq.Messages[0].Content, "reply with EXACTLY this JSON shape") {
		t.Error("the caller's prose-schema system prompt leaked into a tools-enabled request")
	}
	if gotReq.Messages[0].Content != toolSystemPrompt {
		t.Errorf("system message = %q, want toolSystemPrompt", gotReq.Messages[0].Content)
	}
}
