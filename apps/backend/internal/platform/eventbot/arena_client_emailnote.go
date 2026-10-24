package eventbot

// arena_client_emailnote.go — the organizer's own text of the buyer letters
// (EC-31, migration 0165): saved with the event PATCH, tried out with the
// test-letter route. Reading it is the event list, which carries email_note
// and email_note_format.

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// EmailNoteTestAnswer is the 202 answer of the test-letter route.
type EmailNoteTestAnswer struct {
	Queued bool   `json:"queued"`
	To     string `json:"to"`
}

// SetEmailNote stores the text of the letters as ART (email_note_format
// "art"). An empty text clears it. A refused text is a 422
// event.invalid_email_note with the reason in APIError.Reason.
func (c *ArenaClient) SetEmailNote(ctx context.Context, jwt string, orgID, eventID uuid.UUID, art string) error {
	body := map[string]any{"email_note": nil}
	if art != "" {
		body = map[string]any{"email_note": art, "email_note_format": "art"}
	}
	return c.do(ctx, http.MethodPatch, "/v1/organizations/"+orgID.String()+"/events/"+eventID.String(), jwt, body, nil)
}

// SendEmailNoteTest queues a sample ticket letter carrying the stored text to
// the caller's own address (POST .../email-note/test).
func (c *ArenaClient) SendEmailNoteTest(ctx context.Context, jwt string, orgID, eventID uuid.UUID, locale string) (EmailNoteTestAnswer, error) {
	var out EmailNoteTestAnswer
	body := map[string]any{"locale": locale}
	err := c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/events/"+eventID.String()+"/email-note/test", jwt, body, &out)
	return out, err
}
