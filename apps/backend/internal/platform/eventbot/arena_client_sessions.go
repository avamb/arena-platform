package eventbot

// arena_client_sessions.go — the calls behind the "Sessions" screens: the dry
// run of a move or cancellation, the move/cancel themselves (a PATCH of the
// EXISTING session, never a second session) and the organizer contact buyers
// are pointed to. The rules live in arena-api (internal/platform/
// sessionchange); the bot only shows its answers.

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// ChangeImpact asks what moving the session to startAt (or cancelling it)
// would do. startAt nil means "keep the start"; cancel proposes cancelling.
// locale picks the language of the suggested message to buyers.
func (c *ArenaClient) ChangeImpact(ctx context.Context, jwt string, orgID, sessionID uuid.UUID, startAt *time.Time, cancel bool, locale string) (openapi.SessionChangeImpact, error) {
	q := url.Values{}
	if startAt != nil {
		q.Set("start_at", startAt.UTC().Format(time.RFC3339))
	}
	if cancel {
		q.Set("status", "cancelled")
	}
	if locale != "" {
		q.Set("locale", locale)
	}
	path := "/v1/organizations/" + orgID.String() + "/sessions/" + sessionID.String() + "/change-impact"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out openapi.SessionChangeImpact
	err := c.do(ctx, http.MethodGet, path, jwt, nil, &out)
	return out, err
}

func sessionPath(orgID, eventID, sessionID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/events/" + eventID.String() + "/sessions/" + sessionID.String()
}

// MoveSession moves the existing session to startAt (the session keeps its
// length). message is the organizer's own text for the buyers' letter.
func (c *ArenaClient) MoveSession(ctx context.Context, jwt string, orgID, eventID, sessionID uuid.UUID, startAt time.Time, message string) (openapi.SessionEnvelope, error) {
	body := map[string]any{
		"start_at": startAt.UTC().Format(time.RFC3339),
		"notice":   map[string]any{"message": message},
	}
	var out openapi.SessionEnvelope
	err := c.do(ctx, http.MethodPatch, sessionPath(orgID, eventID, sessionID), jwt, body, &out)
	return out, err
}

// CancelSession cancels the session; message is the organizer's own text for
// the buyers' cancellation letter.
func (c *ArenaClient) CancelSession(ctx context.Context, jwt string, orgID, eventID, sessionID uuid.UUID, message string) (openapi.SessionEnvelope, error) {
	body := map[string]any{
		"status": "cancelled",
		"notice": map[string]any{"message": message},
	}
	var out openapi.SessionEnvelope
	err := c.do(ctx, http.MethodPatch, sessionPath(orgID, eventID, sessionID), jwt, body, &out)
	return out, err
}

// SetEventContact stores the organizer contact buyers are told to write to
// (the event's promoter, or else the organization — the server decides).
func (c *ArenaClient) SetEventContact(ctx context.Context, jwt string, orgID, eventID uuid.UUID, email, phone string, phoneHidden bool) (openapi.OrganizerContact, error) {
	body := map[string]any{"email": email, "phone": phone, "phone_hidden": phoneHidden}
	var out openapi.EventContactResponse
	err := c.do(ctx, http.MethodPut, "/v1/organizations/"+orgID.String()+"/events/"+eventID.String()+"/contact", jwt, body, &out)
	return out.Contact, err
}
