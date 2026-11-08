package eventbot

// arena_client_reissue.go — the calls behind "Replace tickets" (spec 63,
// stages R-4 and R-5). An order and an invitation issued before spec 46 (a
// complimentary issuance without an order) have the same four routes under
// their own path, with the same bodies and answers. The request body and two
// of the answers carry buyer addresses: nothing here logs them (do logs the
// route only).

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func reissueOrderPath(orgID, orderID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/orders/" + orderID.String()
}

func reissueInvitationPath(orgID, invitationID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/complimentary/" + invitationID.String()
}

// ReissueOptions reads the check screen of an order's reissue
// (GET .../orders/{id}/reissue-options): the route, the order's address and
// buyer for comparison, every ticket and what would block the operation now.
func (c *ArenaClient) ReissueOptions(ctx context.Context, jwt string, orgID, orderID uuid.UUID) (openapi.ReissueOptions, error) {
	var out openapi.ReissueOptions
	err := c.do(ctx, http.MethodGet, reissueOrderPath(orgID, orderID)+"/reissue-options", jwt, nil, &out)
	return out, err
}

// InvitationReissueOptions reads the check screen of an invitation's reissue
// (GET .../complimentary/{id}/reissue-options).
func (c *ArenaClient) InvitationReissueOptions(ctx context.Context, jwt string, orgID, invitationID uuid.UUID) (openapi.InvitationReissueOptions, error) {
	var out openapi.InvitationReissueOptions
	err := c.do(ctx, http.MethodGet, reissueInvitationPath(orgID, invitationID)+"/reissue-options", jwt, nil, &out)
	return out, err
}

// ReissueTickets replaces every active ticket of the order
// (POST .../orders/{id}/reissue-tickets). The Idempotency-Key makes a repeat
// of the same confirmed operation answer the first result instead of
// replacing the tickets twice.
func (c *ArenaClient) ReissueTickets(ctx context.Context, jwt string, orgID, orderID uuid.UUID, key, email, reason, note string) (openapi.ReissueTicketsResponse, error) {
	return c.reissueTicketsAt(ctx, jwt, reissueOrderPath(orgID, orderID), key, email, reason, note)
}

// InvitationReissueTickets replaces the active tickets of an invitation
// (POST .../complimentary/{id}/reissue-tickets), with the same key rule.
func (c *ArenaClient) InvitationReissueTickets(ctx context.Context, jwt string, orgID, invitationID uuid.UUID, key, email, reason, note string) (openapi.ReissueTicketsResponse, error) {
	return c.reissueTicketsAt(ctx, jwt, reissueInvitationPath(orgID, invitationID), key, email, reason, note)
}

func (c *ArenaClient) reissueTicketsAt(ctx context.Context, jwt, base, key, email, reason, note string) (openapi.ReissueTicketsResponse, error) {
	req := openapi.ReissueTicketsRequest{Email: email, Reason: reason}
	if n := strings.TrimSpace(note); n != "" {
		req.Note = &n
	}
	var out openapi.ReissueTicketsResponse
	err := c.do(withIdempotencyKey(ctx, key), http.MethodPost, base+"/reissue-tickets", jwt, req, &out)
	return out, err
}

// ResendReissue queues the letter of an order's reissue that has not gone out
// again (POST .../orders/{id}/reissues/{reissue_id}/resend). A 409
// reissue.already_applied means the letter has already left.
func (c *ArenaClient) ResendReissue(ctx context.Context, jwt string, orgID, orderID, reissueID uuid.UUID) (openapi.ReissueResendResponse, error) {
	return c.resendReissueAt(ctx, jwt, reissueOrderPath(orgID, orderID), reissueID)
}

// InvitationResendReissue is ResendReissue for an invitation.
func (c *ArenaClient) InvitationResendReissue(ctx context.Context, jwt string, orgID, invitationID, reissueID uuid.UUID) (openapi.ReissueResendResponse, error) {
	return c.resendReissueAt(ctx, jwt, reissueInvitationPath(orgID, invitationID), reissueID)
}

func (c *ArenaClient) resendReissueAt(ctx context.Context, jwt, base string, reissueID uuid.UUID) (openapi.ReissueResendResponse, error) {
	var out openapi.ReissueResendResponse
	err := c.do(ctx, http.MethodPost, base+"/reissues/"+reissueID.String()+"/resend", jwt, map[string]string{}, &out)
	return out, err
}

// ReissueHistory lists the reissues of an order, newest first
// (GET .../orders/{id}/reissues). Each entry carries both addresses.
func (c *ArenaClient) ReissueHistory(ctx context.Context, jwt string, orgID, orderID uuid.UUID) (openapi.ReissueHistory, error) {
	var out openapi.ReissueHistory
	err := c.do(ctx, http.MethodGet, reissueOrderPath(orgID, orderID)+"/reissues", jwt, nil, &out)
	return out, err
}

// InvitationReissueHistory lists the reissues of an invitation, newest first.
func (c *ArenaClient) InvitationReissueHistory(ctx context.Context, jwt string, orgID, invitationID uuid.UUID) (openapi.ReissueHistory, error) {
	var out openapi.ReissueHistory
	err := c.do(ctx, http.MethodGet, reissueInvitationPath(orgID, invitationID)+"/reissues", jwt, nil, &out)
	return out, err
}
