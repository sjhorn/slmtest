package agent

import "strings"

// stripCodeFence removes a surrounding ```json ... ``` or ``` ... ``` fence
// if present, and trims whitespace. Small models very reliably wrap JSON
// in a fence even when told not to, so the harness accommodates it instead
// of burning a retry turn on a cosmetic issue.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.Index(s, "\n"); i != -1 {
		firstLine := strings.TrimSpace(s[:i])
		if firstLine == "" || strings.EqualFold(firstLine, "json") {
			s = s[i+1:]
		}
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}
