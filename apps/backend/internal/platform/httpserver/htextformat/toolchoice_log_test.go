package htextformat

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/textformat"
)

// A model that refuses the forced tool_choice is a configuration problem:
// the route still answers 200 with the plain conversion, and the log says so
// with a specific error line that holds no text.
func TestFormat_ToolChoiceRefusalIsLoggedAsAConfigProblem(t *testing.T) {
	secret := "ORGANIZER-SECRET-TEXT-4711"
	rg := newRig(t)
	rg.f.err = &textformat.APIError{Status: 400, Type: "invalid_request_error", ToolChoiceRejected: true}
	a := decode(t, rg.format("description", secret))
	if a.AIApplied || a.Reason != "model_error" {
		t.Fatalf("answer = %+v", a)
	}
	logs := rg.logs.String()
	if !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "TEXT_FORMAT_LLM_MODEL does not accept a forced tool_choice") {
		t.Errorf("no config-problem line in the log: %s", logs)
	}
	if !strings.Contains(logs, "model_error="+ModelErrToolChoiceRejected) {
		t.Errorf("the request line lacks the specific word: %s", logs)
	}
	if strings.Contains(logs, secret) {
		t.Errorf("the log holds the text: %s", logs)
	}
	// An ordinary model error does not raise the config-problem line.
	rg = newRig(t)
	rg.f.err = &textformat.APIError{Status: 400, Type: "invalid_request_error"}
	decode(t, rg.format("description", "Hello world"))
	if strings.Contains(rg.logs.String(), "forced tool_choice") {
		t.Errorf("an ordinary 400 was reported as a tool_choice problem: %s", rg.logs.String())
	}
}
