package agent

import (
	"context"
	"encoding/json"
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
