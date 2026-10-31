package eventbot

// arena_client_invite.go — the calls behind "Invitations" (EC-12): issue free
// tickets, page through the issued ones, annul one. Issuing and the list go
// through the staff-invitation routes of spec 46 (INV-05); the old
// complimentary_issuances list and revoke stay for what was issued before. A request carries guests'
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

// ─── staff invitations (spec 46, INV-05) ─────────────────────────────────────

// StaffInvitationInput is one guest's invitation on general-admission places
// of a date (POST .../sessions/{id}/invitations with "ga"). RequestID makes a
// retry safe: the API answers a repeated one with the first order (200,
// replayed) and sends no second letter.
type StaffInvitationInput struct {
	SessionID uuid.UUID
	TierID    uuid.UUID
	Qty       int
	RequestID uuid.UUID
	// Name is printed on the ticket; the API requires it.
	Name string
	// Email is the guest's address; empty sends the PDF to nobody (pdf_only).
	Email string
	// VIP marks every ticket of the order with the organizer's private VIP flag.
	VIP bool
}

// StaffInvitationIssued is the part of the issue answer the bot uses.
type StaffInvitationIssued struct {
	OrderID  uuid.UUID `json:"order_id"`
	SystemID int64     `json:"system_id"`
	Replayed bool      `json:"replayed"`
}

// IssueStaffInvitation issues the invitation (one order) of one guest.
// 409 invitation.not_enough_places / invitation.capacity_exceeded: no free
// place; 422 invitation.session_cancelled; 400 names the field.
func (c *ArenaClient) IssueStaffInvitation(ctx context.Context, jwt string, orgID uuid.UUID, in StaffInvitationInput) (StaffInvitationIssued, error) {
	guest := map[string]any{"name": in.Name}
	delivery := "pdf_only"
	if in.Email != "" {
		guest["email"] = in.Email
		delivery = "guest_email"
	}
	body := map[string]any{
		"request_id": in.RequestID.String(),
		"ga":         []map[string]any{{"tier_id": in.TierID.String(), "qty": in.Qty}},
		"guest":      guest,
		"delivery":   delivery,
		"note":       "issued from the Telegram bot",
	}
	if in.VIP {
		body["vip"] = true
	}
	var out StaffInvitationIssued
	err := c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/sessions/"+in.SessionID.String()+"/invitations", jwt, body, &out)
	return out, err
}

// ListOrgInvitations returns one page of the organization's staff invitations
// of every date, newest first, annulled ones included (state=all). cursor is
// the previous page's next_cursor, empty for the first page.
func (c *ArenaClient) ListOrgInvitations(ctx context.Context, jwt string, orgID uuid.UUID, cursor string, limit int) (openapi.StaffInvitationOrgListResponse, error) {
	q := url.Values{}
	q.Set("state", "all")
	q.Set("limit", strconv.Itoa(limit))
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out openapi.StaffInvitationOrgListResponse
	err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/invitations?"+q.Encode(), jwt, nil, &out)
	return out, err
}

// StaffInvitationCancelled is the answer to a cancellation.
type StaffInvitationCancelled struct {
	CancelledTicketIDs []string `json:"cancelled_ticket_ids"`
	SkippedTicketIDs   []string `json:"skipped_ticket_ids"`
}

// CancelStaffInvitation cancels every active ticket of the order
// (POST .../invitations/{order_id}/cancel). The guest is told by e-mail when
// there is an address. A ticket already scanned at the door is skipped.
func (c *ArenaClient) CancelStaffInvitation(ctx context.Context, jwt string, orgID, orderID uuid.UUID) (StaffInvitationCancelled, error) {
	var out StaffInvitationCancelled
	err := c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/invitations/"+orderID.String()+"/cancel", jwt, map[string]any{"notify_guest": true}, &out)
	return out, err
}

// MiniappInvitationsLink draws a one-time code for the staff invitations
// Mini App page of one date: <EVENT_CENTER_PUBLIC_URL>/tg/invitations
// ?session=<id>&event=<id>#code=<code> (the code is after "#").
func (c *ArenaClient) MiniappInvitationsLink(ctx context.Context, telegramUserID int64, eventID, sessionID uuid.UUID) (WebLoginLink, error) {
	var out WebLoginLink
	body := map[string]any{
		"telegram_user_id": telegramUserID, "purpose": "miniapp", "kind": "invitations",
		"event_id": eventID.String(), "session_id": sessionID.String(),
	}
	err := c.do(ctx, http.MethodPost, "/v1/bot/web-login-codes", c.serviceToken, body, &out)
	return out, err
}
