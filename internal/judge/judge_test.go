package judge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGradeParsesANoulAnswer(t *testing.T) {
	var gotBody map[string]any
	var gotAuth, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, `{"model":"jev-latest","answers":{"met":{"type":"noul","noul":0.32}}}`)
	}))
	defer srv.Close()

	c := &Client{Endpoint: srv.URL, Model: "jev-latest", APIKey: "secret"}
	v, err := c.Grade(context.Background(), "the count is exactly 500.", "bash-3.2$ wc -l\n1")
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if v.Probability != 0.32 {
		t.Fatalf("Probability = %v, want 0.32", v.Probability)
	}
	if v.Passed {
		t.Fatal("0.32 is below Threshold; Passed must be false")
	}
	if v.Model != "jev-latest" {
		t.Fatalf("Model = %q", v.Model)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotType != "application/json" {
		t.Fatalf("Content-Type = %q", gotType)
	}
	// The screen goes in state; the criterion goes in the question. A
	// backend caches on state, so putting the varying criterion there
	// would defeat it.
	if got := gotBody["state"]; got != "bash-3.2$ wc -l\n1" {
		t.Fatalf("state = %q, want the screen", got)
	}
	qs, _ := gotBody["questions"].(map[string]any)
	met, _ := qs["met"].(map[string]any)
	if met["type"] != "noul" {
		t.Fatalf("question type = %v, want noul", met["type"])
	}
	if instr, _ := met["instructions"].(string); !strings.Contains(instr, "the count is exactly 500.") {
		t.Fatalf("instructions missing the criterion: %q", instr)
	}
}

// A local backend needs no key, and an empty one must not send a header.
func TestGradeOmitsAuthWhenNoKey(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		io.WriteString(w, `{"answers":{"met":{"noul":0.9}}}`)
	}))
	defer srv.Close()

	if _, err := (&Client{Endpoint: srv.URL}).Grade(context.Background(), "x", "y"); err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if hadAuth {
		t.Fatal("no API key was set, so no Authorization header should be sent")
	}
}

func TestGradeThresholdBoundary(t *testing.T) {
	for _, tc := range []struct {
		p    float64
		want bool
	}{{0.49, false}, {0.5, true}, {0.51, true}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"answers": map[string]any{"met": map[string]any{"noul": tc.p}},
			})
		}))
		v, err := (&Client{Endpoint: srv.URL}).Grade(context.Background(), "x", "y")
		srv.Close()
		if err != nil {
			t.Fatalf("p=%v: %v", tc.p, err)
		}
		if v.Passed != tc.want {
			t.Fatalf("p=%v Passed=%v, want %v", tc.p, v.Passed, tc.want)
		}
	}
}

// Every one of these means "no opinion", and the runner must be able to
// tell them apart from a real fail. See runner.applyJudge.
func TestGradeErrorsAreErrorsNotVerdicts(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		want       string
	}{
		{"http error", `{"error":"model inference failed"}`, 500, "returned"},
		{"endpoint reported error", `{"error":"bad request"}`, 200, "endpoint reported"},
		{"not json", `<html>502</html>`, 200, "decoding"},
		{"no noul answer", `{"answers":{"met":{"type":"choice"}}}`, 200, "no noul answer"},
		{"missing question", `{"answers":{}}`, 200, "no noul answer"},
		{"out of range", `{"answers":{"met":{"noul":1.7}}}`, 200, "outside [0,1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := (&Client{Endpoint: srv.URL}).Grade(context.Background(), "x", "y")
			if err == nil {
				t.Fatal("expected an error, got a verdict")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestGradeRefusesWithoutEndpointOrScreen(t *testing.T) {
	if _, err := (&Client{}).Grade(context.Background(), "x", "screen"); err == nil {
		t.Fatal("expected an error with no endpoint")
	}
	// A step whose turns never dispatched anything has no screen; asking a
	// judge about nothing would invite a meaningless verdict.
	if _, err := (&Client{Endpoint: "http://example.invalid"}).Grade(context.Background(), "x", "  \n "); err == nil {
		t.Fatal("expected an error with a blank screen")
	}
}
