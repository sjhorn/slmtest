// Package judge grades a step's Expect criterion against the screen the
// model was looking at, using a "System One" decision endpoint — a model
// that answers a typed yes/no question with a probability instead of
// generating prose.
//
// It exists because a step's Expect is graded by the SAME model that
// produced the screen, and that model owns the verdict. A Verify: check
// closes the dangerous half of that (see internal/runner/assert.go), but
// only for steps with durable state behind them: a step whose Expect is
// pure screen output — "the output contains X" — has nothing for an
// external process to inspect. Those steps are exactly where a second,
// independent reader is worth having.
//
// What this is NOT: a defence against a staged screen. Measured against
// a corpus of real terminal screens, every model tested — Open-Jev 2B/9B,
// Kev 4B/9B, and hosted Jev — passed all four screens that had been
// deliberately faked with `echo`, because the expected text genuinely WAS
// on the screen. An honest reader verifies what is on the screen, not how
// it got there. Verify: remains the only answer to fabrication, and a
// judge verdict must never be read as evidence against it.
//
// The wire format is TypeSafe's System One shape, which both backends
// tested speak unchanged:
//
//	Open-Jev (local)  python -m jev.server --checkpoint ... --device mps
//	                  POST http://127.0.0.1:8011/v1/systemone
//	Jev (hosted)      POST https://api.typesafe.ai/v1/systemone
//	                  Authorization: Bearer <key>
package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout bounds one grading request. A judge is a convenience on
// top of a run, never the run's purpose: measured medians were 456ms
// (Open-Jev-9B, local) and ~1s (hosted Jev), so this is generous enough
// for a cold first request while staying far below a step's own budget.
const DefaultTimeout = 30 * time.Second

// outputLimit caps what a judge's Output field keeps for the report.
const outputLimit = 512

// Verdict is one graded criterion.
type Verdict struct {
	// Probability is the model's calibrated probability that the criterion
	// was satisfied, in [0,1].
	Probability float64
	// Passed is Probability >= Threshold, precomputed for the report.
	Passed bool
	// Model is what the endpoint reported answering as, for the audit
	// trail — a judge backend can be swapped without the spec changing,
	// so a report must record which one actually answered.
	Model string
}

// Threshold splits Probability into pass/fail.
//
// Deliberately a plain 0.5 and deliberately not configurable: this verdict
// carries NO authority over a step's result (see internal/runner/assert.go),
// so the split only decides how a report line reads. Tuning it would imply
// a precision the measurements do not support — across 48 hand-labelled
// cases the local backends produced almost entirely saturated
// probabilities (0.00/1.00), so no threshold separated their errors from
// their correct answers. Hosted Jev did use the middle of the range, but
// on a sample with zero errors there was nothing to fit a threshold to.
const Threshold = 0.5

// Client grades criteria against a System One endpoint.
type Client struct {
	// Endpoint is the full URL of the systemone route, e.g.
	// "http://127.0.0.1:8011/v1/systemone".
	Endpoint string
	// Model names the checkpoint to answer with. Local Open-Jev accepts
	// "open-jev"; hosted Jev takes "jev-latest"/"jev-preview".
	Model string
	// APIKey, when non-empty, is sent as a bearer token. Local backends
	// need none.
	APIKey string
	// HTTP is the client used for requests; nil means a default one
	// bounded by Timeout.
	HTTP *http.Client
	// Timeout bounds one request; zero means DefaultTimeout.
	Timeout time.Duration
}

// instructions is the question put to the judge. It names the criterion
// and asks only about what is visible, matching the wording the runner's
// own system prompt uses on the acting model ("judge only by output you
// can actually see") so the two are asking the same question.
const instructions = "A tester is checking a step of a terminal test. The success criterion is: %s\n" +
	"Does the terminal output above show that this criterion was satisfied?"

type request struct {
	State     string              `json:"state"`
	Model     string              `json:"model,omitempty"`
	Questions map[string]question `json:"questions"`
}

type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type response struct {
	Model   string `json:"model"`
	Answers map[string]struct {
		Type string   `json:"type"`
		Noul *float64 `json:"noul"`
	} `json:"answers"`
	Error string `json:"error"`
}

// Grade asks whether screen satisfies expect.
//
// An error means the judge could not answer — an unreachable endpoint, a
// malformed reply, a timeout. Callers must treat that as "no opinion" and
// never as evidence about the step: see runner.runJudgeAssertion, which
// records it as a harness fault the same way an unrunnable Verify: is.
func (c *Client) Grade(ctx context.Context, expect, screen string) (Verdict, error) {
	if c.Endpoint == "" {
		return Verdict{}, fmt.Errorf("judge: no endpoint configured")
	}
	if strings.TrimSpace(screen) == "" {
		return Verdict{}, fmt.Errorf("judge: nothing on screen to grade")
	}

	body, err := json.Marshal(request{
		State: screen,
		Model: c.Model,
		Questions: map[string]question{
			"met": {Type: "noul", Instructions: fmt.Sprintf(instructions, expect)},
		},
	})
	if err != nil {
		return Verdict{}, fmt.Errorf("judge: encoding request: %w", err)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return Verdict{}, fmt.Errorf("judge: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Verdict{}, fmt.Errorf("judge: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Verdict{}, fmt.Errorf("judge: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Verdict{}, fmt.Errorf("judge: endpoint returned %s: %s", resp.Status, firstLine(string(raw)))
	}

	var parsed response
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Verdict{}, fmt.Errorf("judge: decoding response: %w", err)
	}
	if parsed.Error != "" {
		return Verdict{}, fmt.Errorf("judge: endpoint reported: %s", parsed.Error)
	}
	answer, ok := parsed.Answers["met"]
	if !ok || answer.Noul == nil {
		return Verdict{}, fmt.Errorf("judge: reply carried no noul answer")
	}
	p := *answer.Noul
	if p < 0 || p > 1 {
		return Verdict{}, fmt.Errorf("judge: probability %v outside [0,1]", p)
	}
	return Verdict{Probability: p, Passed: p >= Threshold, Model: parsed.Model}, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > outputLimit {
		s = s[:outputLimit] + "..."
	}
	return s
}
