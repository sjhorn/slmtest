// Native tool-calling support.
//
// Instead of only describing the Action schema in prose and parsing
// whatever text comes back, Client also offers the five actions as
// standard OpenAI `tools`. Most current OpenAI-compatible servers honor
// this using the model's own native chat template (confirmed live
// against Qwen2.5 — see docs/model-runs.md, "Using the OpenAI
// tools/tool_calls API"), which sidesteps the fence-stripping and
// malformed-JSON tolerance the prose path needs entirely: the model
// isn't asked to freeform-generate JSON, it's asked to call a function,
// which is a narrower, better-constrained thing for a model to do.
//
// This is deliberately additive, not a replacement. A server that
// doesn't recognize `tools` just ignores the field and the prose path
// behaves exactly as before; see Client.Complete's doc comment for what
// happens when a server recognizes but mishandles it (confirmed against
// one real model — see docs/model-runs.md).
package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// tool is one entry in an OpenAI-shaped `tools` request array.
type tool struct {
	Type     string       `json:"type"` // always "function"
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// toolSystemPrompt replaces the caller's prose-schema system prompt
// entirely when a request uses native tool-calling — it is NOT appended
// alongside it. Confirmed live and reproduced with certainty (see
// docs/model-runs.md): sending the full prose-schema prompt (which
// dictates "reply with EXACTLY one JSON object matching this schema" and
// shows the literal shape) together with `tools` measurably broke
// Qwen2.5-1.5B — instead of a proper `tool_calls` response, it produced a
// degraded hybrid, a bare `{"name":...,"arguments":{...}}` object in
// plain content, bypassing tool_calls entirely. The tool definitions
// already carry the schema (names, parameter shapes, per-action rules in
// their descriptions); restating it as a second, conflicting instruction
// source confuses the model rather than reinforcing it. This prompt
// carries only the rules a tool definition can't express — turn
// discipline, hint semantics, and the pass/fail judgement rules — safe
// to send precisely because it does not also dictate a competing shape.
const toolSystemPrompt = `You are operating a real Linux shell through a pseudo-terminal to complete one step of a test script, using the tools you have been given. You are not chatting with a user.

Rules:
- Call exactly one tool per turn.
- A Hint is a suggestion, not a requirement. If it doesn't work, reason about why (missing package? wrong path? needs sudo?) and try something else before failing the step.
- finish_step is the only way a step ends. Use "pass" only if the Expect criterion is clearly satisfied by output you have actually seen. Use "fail" if you're confident it cannot be satisfied — don't guess "pass".
- abort_test is only if the environment itself is broken (shell died, container unusable) — not for a step simply failing.
- Judge only by terminal output you can see in this conversation, never by assumption.`

// actionTools mirrors the five ActionType values. Parameter names are
// deliberately identical to Action's own JSON field names — see
// mergeActionName — and each description carries the same rule the
// system prompt states for the prose path, so the two stay consistent
// with whichever one a given model actually reads.
//
// run_command's parameters deliberately omit press_enter: offering it
// there is exactly what let a model silently no-op run_command with
// press_enter:false before that was fixed (see CLAUDE.md, "What running
// against real models has shown") — native tool-calling gets that fix
// for free by construction, since the model cannot set a parameter that
// doesn't exist in the schema.
var actionTools = []tool{
	{Type: "function", Function: toolFunction{
		Name:        string(ActionRunCommand),
		Description: "Type a shell command and press Enter, then wait and report new terminal output. Enter is always sent — there is no way to suppress it here; use send_keys for that.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "The shell command to type."},
				"wait_ms": {"type": "integer", "description": "How long to wait before observing new output. Defaults to 1500."}
			},
			"required": ["command"]
		}`),
	}},
	{Type: "function", Function: toolFunction{
		Name:        string(ActionSendKeys),
		Description: "Type text or a control character into the terminal WITHOUT pressing Enter by default. Use for partial input, control characters (e.g. \\u001b for Escape, \\u0003 for Ctrl-C), or interactive prompts (a TUI, a password prompt, an editor's insert mode).",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "Raw text or a single control character to send."},
				"press_enter": {"type": "boolean", "description": "Whether to press Enter after typing. Defaults to false — the point of send_keys is usually NOT pressing Enter."},
				"wait_ms": {"type": "integer", "description": "How long to wait before observing new output. Defaults to 1500."}
			},
			"required": ["command"]
		}`),
	}},
	{Type: "function", Function: toolFunction{
		Name:        string(ActionWait),
		Description: "Take no terminal action; wait and re-observe. Use when a previous command (a build, an install, a service starting) is likely still running.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"wait_ms": {"type": "integer", "description": "How long to wait before observing new output. Defaults to 2000."}
			}
		}`),
	}},
	{Type: "function", Function: toolFunction{
		Name:        string(ActionFinishStep),
		Description: "End the current step with a verdict. This is the ONLY way a step ends. Use \"pass\" only if the Expect criterion is clearly satisfied by output you have actually seen. Use \"fail\" if you are confident it cannot be satisfied — never guess pass.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"step_result": {"type": "string", "enum": ["pass", "fail"]},
				"reason": {"type": "string", "description": "Why this verdict, citing the terminal output that supports it."}
			},
			"required": ["step_result", "reason"]
		}`),
	}},
	{Type: "function", Function: toolFunction{
		Name:        string(ActionAbortTest),
		Description: "End the ENTIRE test run immediately. Reserved for a genuinely broken environment (the shell died, the container is unusable) — never for a step simply failing or for a reply that keeps getting rejected.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"reason": {"type": "string"}
			},
			"required": ["reason"]
		}`),
	}},
}

// wireMessage is the actual JSON shape of one outgoing request message —
// richer than the plain Message type callers build history from, so that
// buildToolMessages has somewhere to put a synthetic tool_calls/
// tool_call_id without those OpenAI-specific fields leaking into
// agent.Message, which the runner deals in and knows nothing about
// tool-calling at all.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// buildToolMessages rebuilds Turn's plain user/assistant history into the
// OpenAI multi-turn tool-calling shape — required, not cosmetic.
// Reproduced live: sending a model's own past turn back as plain
// assistant *content* (today's simple shape, which is what the runner
// actually stores) measurably pulled Qwen2.5-1.5B back OUT of proper
// tool-calling from the second turn of a step onward, degrading to the
// same broken content-object shape toolSystemPrompt exists to prevent —
// because the model's own prior reply no longer looked like a tool call
// at all. Rebuilding it as {assistant: tool_calls} + {tool: result} on
// every request, purely at the wire-encoding layer, fixed it.
//
// History alternates user/assistant in pairs, where each assistant
// message is Action.ReplayJSON's canonical text and the FOLLOWING
// message (either the next history entry, or — for the most recent
// assistant entry — Turn.UserText, which is not yet stored in History)
// is that action's own terminal-output result: the natural place for a
// tool_call_id-keyed `tool` role message. The very first history entry
// is the original step prompt with no preceding action and is left as a
// plain user message.
//
// An assistant entry that isn't our own canonical JSON — the one case
// this can happen is a parse-error retry, where the runner stores the
// model's raw (possibly malformed) reply rather than replaying it — is
// passed through as plain text instead of guessed at; a plain
// assistant/user pair is still valid alongside tool-calling messages in
// the same array, just not shaped as one itself.
func buildToolMessages(t Turn) []wireMessage {
	msgs := []wireMessage{{Role: "system", Content: toolSystemPrompt}}

	history := t.History
	i := 0
	if len(history) > 0 {
		msgs = append(msgs, wireMessage{Role: history[0].Role, Content: history[0].Content})
		i = 1
	}

	consumedUserText := false
	for ; i < len(history); i += 2 {
		assistantMsg := history[i]
		result := t.UserText
		haveNext := i+1 < len(history)
		if haveNext {
			result = history[i+1].Content
		} else {
			consumedUserText = true
		}

		name, args, ok := splitActionJSON(assistantMsg.Content)
		if !ok {
			msgs = append(msgs, wireMessage{Role: assistantMsg.Role, Content: assistantMsg.Content})
			if haveNext {
				msgs = append(msgs, wireMessage{Role: history[i+1].Role, Content: history[i+1].Content})
			} else {
				msgs = append(msgs, wireMessage{Role: "user", Content: t.UserText})
			}
			continue
		}

		id := fmt.Sprintf("call_%d", i)
		var tc wireToolCall
		tc.ID, tc.Type = id, "function"
		tc.Function.Name, tc.Function.Arguments = name, string(args)
		msgs = append(msgs,
			wireMessage{Role: "assistant", ToolCalls: []wireToolCall{tc}},
			wireMessage{Role: "tool", ToolCallID: id, Content: result},
		)
	}

	if !consumedUserText {
		msgs = append(msgs, wireMessage{Role: "user", Content: t.UserText})
	}
	return msgs
}

// splitActionJSON extracts the action name and remaining fields from our
// own canonical Action JSON (see Action.ReplayJSON), for reconstructing a
// synthetic tool call from history the runner stored as plain text.
func splitActionJSON(content string) (name string, argsJSON []byte, ok bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &fields); err != nil {
		return "", nil, false
	}
	rawName, present := fields["action"]
	if !present {
		return "", nil, false
	}
	if err := json.Unmarshal(rawName, &name); err != nil || name == "" {
		return "", nil, false
	}
	delete(fields, "action")
	args, err := json.Marshal(fields)
	if err != nil {
		return "", nil, false
	}
	return name, args, true
}

// toolCall is one entry in an OpenAI-shaped `tool_calls` response array.
// Arguments is a JSON-encoded *string* per the OpenAI spec (the model's
// function-call arguments, serialized), not a nested object.
type toolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// normalizeToolCall converts one OpenAI-shaped tool call into the exact
// raw JSON text ParseAction expects.
func normalizeToolCall(tc toolCall) (string, error) {
	return mergeActionName(tc.Function.Name, []byte(tc.Function.Arguments))
}

// normalizeContent recognizes a model's own native tool-call format when
// the server could not, or was never asked to, normalize it into the
// standard `tool_calls` field — e.g. xLAM's own
// `[{"name": "...", "arguments": {...}}]`, confirmed against Salesforce's
// model card (see docs/model-runs.md). Applied regardless of whether this
// request used native tools: it's an interpretation of whatever text came
// back, not a consequence of how it was asked for.
//
// Only attempted when content looks like a JSON array at all, and only
// used if it actually parses as one — anything else is returned
// unchanged and falls through to ParseAction's normal error-feedback
// path, same as before this existed.
func normalizeContent(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "[") {
		return content
	}
	var calls []struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(trimmed), &calls); err != nil || len(calls) == 0 {
		return content
	}
	normalized, err := mergeActionName(calls[0].Name, calls[0].Arguments)
	if err != nil {
		return content
	}
	return normalized
}

// mergeActionName re-marshals a tool call's arguments with the action
// name injected as the "action" field, producing the exact shape
// ParseAction expects. This works with no per-field translation because
// actionTools' parameter names are identical to Action's own JSON field
// names by construction.
func mergeActionName(name string, argumentsJSON []byte) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(argumentsJSON, &fields); err != nil {
		return "", fmt.Errorf("tool call arguments were not a JSON object: %w", err)
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	actionJSON, err := json.Marshal(name)
	if err != nil {
		return "", err
	}
	fields["action"] = actionJSON
	merged, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return string(merged), nil
}
