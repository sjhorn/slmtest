package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
			name: "non-JSON body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
			},
			wantErr: "non-JSON response",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			_, err := NewClient(srv.URL+"/v1", "m", "").Complete(context.Background(), Turn{})
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

	_, err := NewClient(srv.URL+"/v1", "m", "").Complete(context.Background(), Turn{})
	if err == nil {
		t.Fatal("Complete succeeded against a closed server, want error")
	}
	if !strings.Contains(err.Error(), "calling SLM endpoint") {
		t.Errorf("error = %q, want it to mention calling the SLM endpoint", err)
	}
}
