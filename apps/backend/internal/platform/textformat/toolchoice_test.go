package textformat

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A model that refuses a forced tool_choice (Claude 5.5 answers 400 to it) is
// reported as such: the flag is set, the API's message is still not kept, and
// an unrelated 400 or a non-400 does not set it. The request itself stays the
// forced one, which the haiku default needs.
func TestAnthropic_ForcedToolChoiceRefusalIsFlagged(t *testing.T) {
	const msg = `tool_choice: type \"tool\" and \"any\" are not supported for this model`
	s := newStub(t, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"`+msg+`"}}`)
	a := NewAnthropic("k", "", s.srv.URL, nil)
	_, err := a.Format(context.Background(), Input{Text: "x", Kind: KindDescription})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 || !ae.ToolChoiceRejected {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "tool_choice") {
		t.Fatalf("the API's message is in the error: %v", err)
	}
	choice, _ := s.body["tool_choice"].(map[string]any)
	if choice["type"] != "tool" || choice["name"] != toolName {
		t.Errorf("the request must keep the forced tool_choice, got %v", s.body["tool_choice"])
	}

	s.answer = `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens too large"}}`
	_, err = a.Format(context.Background(), Input{Text: "x", Kind: KindDescription})
	if !errors.As(err, &ae) || ae.ToolChoiceRejected {
		t.Fatalf("an unrelated 400 must not be flagged: %v", err)
	}

	s.status = 500
	s.answer = `{"type":"error","error":{"type":"api_error","message":"tool_choice"}}`
	_, err = a.Format(context.Background(), Input{Text: "x", Kind: KindDescription})
	if !errors.As(err, &ae) || ae.ToolChoiceRejected {
		t.Fatalf("only a 400 may be flagged: %v", err)
	}
}
