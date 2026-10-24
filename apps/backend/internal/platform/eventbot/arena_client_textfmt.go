package eventbot

// arena_client_textfmt.go — the bot's side of "format my text"
// (POST /v1/organizations/{org_id}/texts/format, spec 44 RT-04). The model key
// lives on arena-api only; the bot sends the organizer's text and gets ART
// back, together with the word whether the model was used.

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// formatCallTimeout is how long the bot waits for the route. The API gives the
// model 25 seconds (textformat.RequestTimeout) and the router 30.
const formatCallTimeout = 32 * time.Second

// FormattedText is the answer of the format route.
type FormattedText struct {
	// HTML is ART: formatted when AIApplied, otherwise the plain conversion
	// of the text. The route always answers with a safe text.
	HTML string `json:"html"`
	// AIApplied says the model's markup was checked and kept. Clients look at
	// this alone: the reason is only for the log.
	AIApplied     bool   `json:"ai_applied"`
	Reason        string `json:"reason,omitempty"`
	PromptVersion string `json:"prompt_version,omitempty"`
}

// WithFormatTimeout sets how long FormatText waits for the route (tests; a
// zero or negative value keeps the default). The receiver is returned.
func (c *ArenaClient) WithFormatTimeout(d time.Duration) *ArenaClient {
	if d > 0 {
		c.formatTimeout = d
	}
	return c
}

// FormatText asks the API to mark up a text the organizer typed. kind is
// "description" or "email_note" (the API also knows "session_message"). A
// failure is an error here and the caller shows the plain text instead: the
// route itself answers 200 for every model failure.
func (c *ArenaClient) FormatText(ctx context.Context, jwt string, orgID uuid.UUID, text, kind, locale string) (FormattedText, error) {
	timeout := c.formatTimeout
	if timeout <= 0 {
		timeout = formatCallTimeout
	}
	slow := *c
	hc := *c.http
	hc.Timeout = timeout
	slow.http = &hc
	var out FormattedText
	body := map[string]any{"text": text, "kind": kind, "locale": locale}
	err := slow.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/texts/format", jwt, body, &out)
	return out, err
}
