package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
}

func NewClient(baseURL, model, apiKey string) *Client {
	return &Client{
		BaseURL: baseURL,
		Model:   model,
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
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
func (c *Client) Complete(ctx context.Context, t Turn) (string, error) {
	msgs := make([]chatMessage, 0, len(t.History)+2)
	msgs = append(msgs, chatMessage{Role: "system", Content: t.System})
	msgs = append(msgs, t.History...)
	msgs = append(msgs, chatMessage{Role: "user", Content: t.UserText})

	body, err := json.Marshal(chatRequest{
		Model:          c.Model,
		Messages:       msgs,
		Temperature:    0.1, // low temperature: this is a control loop, not creative writing
		ResponseFormat: map[string]string{"type": "json_object"},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling SLM endpoint: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("SLM endpoint returned non-JSON response (status %d): %s", resp.StatusCode, truncate(string(raw), 300))
	}
	if cr.Error != nil {
		return "", fmt.Errorf("SLM endpoint error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("SLM endpoint returned no choices (status %d)", resp.StatusCode)
	}
	return cr.Choices[0].Message.Content, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
