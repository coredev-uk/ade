package ai

import (
	"encoding/json"
	"strings"
)

// classifyStop decides whether an HTTP error response is a budget stop. It
// looks at the body, never at the status code: LiteLLM answers a used-up
// budget with 400, 401, 422 or 429 depending on its version (docs/billing).
// It returns nil for any other error.
func classifyStop(source string, body []byte) *StopError {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Details struct {
				ErrorCode string `json:"error_code"`
			} `json:"details"`
		} `json:"error"`
		Detail string `json:"detail"` // old LiteLLM: {"detail": "...ExceededTokenBudget..."}
	}
	_ = json.Unmarshal(body, &env) // a non-JSON body is still matched on its text below
	msg := env.Error.Message
	if msg == "" {
		msg = env.Detail
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	switch {
	case env.Error.Type == "budget_exceeded",
		env.Error.Details.ErrorCode == "enforced_spend_limit_reached",
		env.Error.Type == "insufficient_quota",
		codeIs(env.Error.Code, "insufficient_quota", "exceeded_current_quota_error"),
		env.Error.Type == "exceeded_current_quota_error":
		return &StopError{Source: source, Message: oneLine(msg)}
	}
	low := strings.ToLower(msg)
	for _, needle := range []string{
		"budget has been exceeded", "exceededbudget", "exceededtokenbudget", "exceeded budget for model",
		"you have reached your specified api usage limits", "enforced_spend_limit_reached",
	} {
		if strings.Contains(low, needle) {
			return &StopError{Source: source, Message: oneLine(msg)}
		}
	}
	return nil
}

func codeIs(code any, want ...string) bool {
	s, ok := code.(string)
	if !ok {
		return false
	}
	for _, w := range want {
		if s == w {
			return true
		}
	}
	return false
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
