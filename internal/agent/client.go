package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client talks to any OpenAI-compatible /v1/chat/completions endpoint —
// this covers llama.cpp's server, Ollama (OpenAI-compat mode), vLLM,
// LM Studio, and hosted APIs alike, so swapping the SLM under test is a
// config change, not a code change.
type Client struct {
	BaseURL string // e.g. http://localhost:8080/v1
	Model   string
	APIKey  string // optional; sent as Bearer token if set
	HTTP    *http.Client
	Retry   Retry
}

// Retry bounds how hard Complete tries before giving up. Retrying belongs
// here rather than in the runner so that by the time the runner sees an
// error, it genuinely means "this endpoint is unusable" — which is what
// the runner's abort branch reports it as.
type Retry struct {
	MaxAttempts int           // total attempts including the first; <1 means 1
	BaseDelay   time.Duration // first backoff; doubles each attempt
	MaxDelay    time.Duration // ceiling for a single backoff
}

// DefaultRetry is deliberately modest. A local llama.cpp or Ollama server
// that is genuinely down will not come back within a few seconds, and a
// long retry ladder would just delay a failure the operator needs to see.
func DefaultRetry() Retry {
	return Retry{MaxAttempts: 3, BaseDelay: 500 * time.Millisecond, MaxDelay: 8 * time.Second}
}

// DefaultRequestTimeout bounds a single chat-completions request. It is
// generous because the floor is set by how long a model takes to answer,
// not by the network: a large context, a CPU-only local model, or a cold
// first request can all take tens of seconds legitimately. Raise it with
// SetRequestTimeout when the model under test is slower still.
const DefaultRequestTimeout = 120 * time.Second

func NewClient(baseURL, model, apiKey string) *Client {
	return &Client{
		BaseURL: baseURL,
		Model:   model,
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: DefaultRequestTimeout},
		Retry:   DefaultRetry(),
	}
}

// SetRequestTimeout bounds each individual request. Note this multiplies
// with Retry.MaxAttempts: a request that always times out costs roughly
// timeout × attempts before the run aborts, so raising one is a reason to
// look at the other.
func (c *Client) SetRequestTimeout(d time.Duration) {
	if c.HTTP == nil {
		c.HTTP = &http.Client{}
	}
	c.HTTP.Timeout = d
}

// Message is one turn of chat history, exported so callers (the runner)
// can build up per-step history without reaching into client internals.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatMessage = Message

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	// response_format is honored by llama.cpp/vLLM/newer OpenAI-compatible
	// servers to force valid JSON output; harmless to send even if the
	// server ignores it, since ParseAction() also tolerates stray fencing.
	ResponseFormat map[string]string `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Turn is one exchange: system prompt (sent every call, stateless server
// side), the running transcript of user/assistant turns for this step, and
// the newest user message appended before sending.
type Turn struct {
	System   string
	History  []Message // alternating user/assistant, prior turns THIS STEP only
	UserText string
}

// Complete sends one chat-completions request and returns the raw
// assistant text (not yet parsed as an Action — see ParseAction).
//
// Transient failures (connection refused, 5xx, 429, 408) are retried with
// exponential backoff. A failure the endpoint is telling us is our fault
// — any other 4xx, a well-formed error body, a response with no choices —
// is returned immediately: retrying a rejected request just delays the
// same answer.
func (c *Client) Complete(ctx context.Context, t Turn) (string, error) {
	body, err := c.encodeRequest(t)
	if err != nil {
		return "", err
	}

	attempts := c.Retry.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	var serverDelay time.Duration
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			delay := serverDelay // honor Retry-After when the server sent one
			if delay <= 0 {
				delay = c.backoff(attempt - 1)
			}
			if err := sleep(ctx, delay); err != nil {
				return "", err
			}
		}

		content, res, err := c.attempt(ctx, body)
		if err == nil {
			return content, nil
		}
		lastErr, serverDelay = err, res.retryAfter

		// A cancelled context is the caller giving up (step timeout, whole
		// test timeout); never burn further attempts on it.
		if !res.retryable || ctx.Err() != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("SLM endpoint still failing after %d attempts: %w", attempts, lastErr)
}

// attemptResult describes how a failed attempt should be treated. It is
// separate from the error itself so classification stays in one place.
type attemptResult struct {
	retryable  bool
	retryAfter time.Duration // from a Retry-After header, if any
}

func (c *Client) attempt(ctx context.Context, body []byte) (string, attemptResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", attemptResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Transport-level: refused, reset, DNS, timeout. Worth retrying —
		// a local model server being restarted looks exactly like this.
		return "", attemptResult{retryable: true}, fmt.Errorf("calling SLM endpoint: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", attemptResult{retryable: true}, fmt.Errorf("reading SLM response: %w", err)
	}

	if retryableStatus(resp.StatusCode) {
		return "", attemptResult{retryable: true, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))},
			fmt.Errorf("SLM endpoint returned HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", attemptResult{}, fmt.Errorf("SLM endpoint returned non-JSON response (status %d): %s", resp.StatusCode, truncate(string(raw), 300))
	}
	if cr.Error != nil {
		return "", attemptResult{}, fmt.Errorf("SLM endpoint error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", attemptResult{}, fmt.Errorf("SLM endpoint returned no choices (status %d)", resp.StatusCode)
	}
	return cr.Choices[0].Message.Content, attemptResult{}, nil
}

func (c *Client) encodeRequest(t Turn) ([]byte, error) {
	msgs := make([]chatMessage, 0, len(t.History)+2)
	msgs = append(msgs, chatMessage{Role: "system", Content: t.System})
	msgs = append(msgs, t.History...)
	msgs = append(msgs, chatMessage{Role: "user", Content: t.UserText})

	return json.Marshal(chatRequest{
		Model:          c.Model,
		Messages:       msgs,
		Temperature:    0.1, // low temperature: this is a control loop, not creative writing
		ResponseFormat: map[string]string{"type": "json_object"},
	})
}

// retryableStatus reports whether an HTTP status is worth another try.
// 5xx and 429 are the server saying "not now"; 408 is a request timeout.
// Every other 4xx means the request itself was rejected, and sending it
// again unchanged would get the same answer.
func retryableStatus(code int) bool {
	switch {
	case code >= 500:
		return true
	case code == http.StatusTooManyRequests, code == http.StatusRequestTimeout:
		return true
	default:
		return false
	}
}

// parseRetryAfter reads the delay-seconds form of Retry-After. The
// HTTP-date form is ignored rather than half-supported: falling back to
// normal backoff is a safe default, and the date form is vanishingly rare
// from model servers.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs < 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	// Don't let a server park the run for minutes on a single header.
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// backoff returns the delay before attempt n+1, doubling each time and
// capped at MaxDelay. Half the delay is jittered so that a runner and its
// retries don't march in lockstep with anything else hitting the endpoint.
func (c *Client) backoff(n int) time.Duration {
	base := c.Retry.BaseDelay
	if base <= 0 {
		return 0
	}
	d := base
	for i := 1; i < n; i++ {
		d *= 2
		if c.Retry.MaxDelay > 0 && d >= c.Retry.MaxDelay {
			d = c.Retry.MaxDelay
			break
		}
	}
	if c.Retry.MaxDelay > 0 && d > c.Retry.MaxDelay {
		d = c.Retry.MaxDelay
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
