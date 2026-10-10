package eventbot

// arena_client_team.go - the two calls the Team screen makes about a bot
// invitation (spec 35 EC-16): annul it, or send its letter again.

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// RevokeResult is what DELETE .../bot-invitations/{id} answers.
type RevokeResult struct {
	Email             string `json:"email"`
	MembershipRemoved bool   `json:"membership_removed"`
	KeptReason        string `json:"kept_reason"`
}

// RevokeInvitation annuls an invitation. The route is owner-only.
func (c *ArenaClient) RevokeInvitation(ctx context.Context, jwt string, orgID, invitationID uuid.UUID) (RevokeResult, error) {
	var out RevokeResult
	err := c.do(ctx, http.MethodDelete, "/v1/organizations/"+orgID.String()+"/bot-invitations/"+invitationID.String(), jwt, nil, &out)
	return out, err
}

// ResendInvitation sends the invitation's e-mail again with a new link. The
// 429 of the ten-minute limit comes back as an *APIError.
func (c *ArenaClient) ResendInvitation(ctx context.Context, jwt string, orgID, invitationID uuid.UUID, locale string) error {
	body := map[string]any{"locale": NormalizeLocale(locale)}
	return c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/bot-invitations/"+invitationID.String()+"/resend", jwt, body, nil)
}
