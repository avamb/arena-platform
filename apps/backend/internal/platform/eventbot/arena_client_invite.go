package eventbot

// arena_client_invite.go — the calls behind "Invitations" (EC-12): issue free
// tickets, page through the issued ones, annul one. A request carries guests'
// e-mails and names: nothing here logs them, and an error keeps the route
// only (routeOf), never the body.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// InvitationInput is one operation's request: the tickets of ONE guest (the
// bot issues one issuance per guest, so each can be annulled on its own).
type InvitationInput struct {
	SessionID uuid.UUID
	TierID    uuid.UUID
	Email     string
	Name      string
	// BatchID makes a retry safe: the API answers a repeated (organization,
	// batch) with the first result and sends no second letter.
	BatchID string
}

// InvitationTicket is a ticket the API issued.
type InvitationTicket struct {
	ID             uuid.UUID `json:"id"`
	SystemTicketID int64     `json:"system_ticket_id"`
	HolderEmail    *string   `json:"holder_email"`
	HolderName     *string   `json:"holder_name"`
}

// InvitationIssued is the answer to an issue call.
type InvitationIssued struct {
	Issuance struct {
		ID uuid.UUID `json:"id"`
	} `json:"issuance"`
	Tickets          []InvitationTicket `json:"tickets"`
	IdempotentReplay bool               `json:"idempotent_replay"`
}

// IssueInvitation issues one free ticket to one guest
// (POST .../complimentary). A 409 is one of tier.sold_out or
// complimentary.capacity_overflow (no free place), a 400 names the field.
func (c *ArenaClient) IssueInvitation(ctx context.Context, jwt string, orgID uuid.UUID, in InvitationInput) (InvitationIssued, error) {
	body := map[string]any{
		"session_id": in.SessionID.String(),
		"tier_id":    in.TierID.String(),
		"qty":        1,
		"recipients": []string{in.Email},
		"batch_id":   in.BatchID,
		"issued_by":  "telegram_bot",
	}
	if in.Name != "" {
		body["recipient_names"] = []string{in.Name}
	}
	var out InvitationIssued
	err := c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/complimentary", jwt, body, &out)
	return out, err
}

// ListInvitations returns one page of the issued invitations, newest first.
func (c *ArenaClient) ListInvitations(ctx context.Context, jwt string, orgID uuid.UUID, limit, offset int) (openapi.ComplimentaryListResponse, error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	var out openapi.ComplimentaryListResponse
	err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/complimentary?"+q.Encode(), jwt, nil, &out)
	return out, err
}

// RevokeInvitation annuls every ticket of an issuance
// (POST /v1/complimentary/{id}/revoke - a flat route, guarded by the owning
// organization of the row). A 409 is complimentary.already_revoked or
// complimentary.scanned_ticket_requires_manual_review; a 403 is a row of
// another organization.
func (c *ArenaClient) RevokeInvitation(ctx context.Context, jwt string, issuanceID uuid.UUID) error {
	return c.do(ctx, http.MethodPost, "/v1/complimentary/"+issuanceID.String()+"/revoke", jwt, map[string]string{"reason": "revoked from the Telegram bot"}, nil)
}
