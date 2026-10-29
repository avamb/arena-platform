package hinvitations

// delivery.go — the delivery side of staff invitations (spec 46, section 3.4,
// task INV-03): the order-wide PDF download, the resend, and the letter that
// tells a guest their invitation was cancelled.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// OrderResender queues the tickets of an invitation order for delivery again.
// htickets.Handler is the production implementation.
type OrderResender interface {
	ResendInvitationOrder(ctx context.Context, r *http.Request, order gen.OrderRow, recipient string, oneTime bool) (int, error)
}

// PDFRenderer renders the given tickets as the pages of one PDF in the given
// label language. The production implementation is delivery.RenderOrderPDF
// wired with the API's media store.
type PDFRenderer func(ctx context.Context, ticketIDs []uuid.UUID, locale string) ([]byte, error)

// deliveryDeps are the optional collaborators of the delivery routes.
type deliveryDeps struct {
	resender OrderResender
	renderer PDFRenderer
}

// WithResender sets the resend collaborator; without it the resend route
// answers 503.
func (h *Handler) WithResender(r OrderResender) *Handler {
	h.delivery.resender = r
	return h
}

// WithPDFRenderer sets the PDF collaborator; without it the download route
// answers 503.
func (h *Handler) WithPDFRenderer(r PDFRenderer) *Handler {
	h.delivery.renderer = r
	return h
}

// issuedOrder is an invitation order whose tickets exist, with the facts the
// delivery routes need.
type issuedOrder struct {
	order  gen.InvitationOrderRow
	active []uuid.UUID
}

// loadIssuedOrder reads the invitation (404 for a foreign organization or an
// order that is not a staff invitation) and its tickets. It answers the
// request itself and returns false when the tickets are not all issued yet
// (409 invitation.tickets_not_issued) or none is active (409
// invitation.no_active_tickets).
func (h *Handler) loadIssuedOrder(w http.ResponseWriter, r *http.Request, orgID, orderID uuid.UUID) (issuedOrder, bool) {
	ctx := r.Context()
	o, err := h.queries.GetInvitationOrder(ctx, orderID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, r, http.StatusNotFound, "invitation.not_found", "invitation not found", nil)
		return issuedOrder{}, false
	}
	if err != nil {
		h.internal(w, r, "delivery: order lookup", err)
		return issuedOrder{}, false
	}
	lines, err := h.queries.ListInvitationOrderLines(ctx, []uuid.UUID{orderID})
	if err != nil {
		h.internal(w, r, "delivery: lines", err)
		return issuedOrder{}, false
	}
	issued := len(lines) > 0
	for _, l := range lines {
		if l.TicketID == nil {
			issued = false
		}
	}
	if !issued {
		writeErr(w, r, http.StatusConflict, "invitation.tickets_not_issued",
			"the tickets of this invitation are still being issued, try again in a moment", nil)
		return issuedOrder{}, false
	}
	tickets, err := h.queries.ListTicketsByCheckoutSession(ctx, o.CheckoutSessionID)
	if err != nil {
		h.internal(w, r, "delivery: tickets", err)
		return issuedOrder{}, false
	}
	var active []uuid.UUID
	for _, t := range tickets {
		if t.Status == "active" {
			active = append(active, t.ID)
		}
	}
	if len(active) == 0 {
		writeErr(w, r, http.StatusConflict, "invitation.no_active_tickets",
			"every ticket of this invitation was cancelled", nil)
		return issuedOrder{}, false
	}
	return issuedOrder{order: o, active: active}, true
}

// HandleTicketsPDF serves GET /v1/organizations/{org_id}/invitations/{order_id}/tickets.pdf:
// one PDF with a page per active ticket of the invitation, as an attachment
// named invitation-<system_id>.pdf.
func (h *Handler) HandleTicketsPDF(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.delivery.renderer == nil {
		unavailable(w, r)
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	orderID, ok := httputil.UUIDPathParam(w, r, "order_id")
	if !ok {
		return
	}
	iss, ok := h.loadIssuedOrder(w, r, orgID, orderID)
	if !ok {
		return
	}
	ctx := r.Context()
	data, err := h.delivery.renderer(ctx, iss.active, invitationLocale(iss.order.Metadata))
	if err != nil {
		h.logger.Error("invitation.tickets_pdf: render failed",
			slog.String("order_id", orderID.String()), slog.String("error", err.Error()))
		writeErr(w, r, http.StatusInternalServerError, "invitation.pdf_failed", "the PDF could not be rendered", nil)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="invitation-%d.pdf"`, iss.order.SystemID))
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// invitationLocale reads orders.metadata.invitation.locale (the guest's
// language, already normalised at issue time); "" prints English labels.
func invitationLocale(raw json.RawMessage) string {
	var m struct {
		Invitation struct {
			Locale string `json:"locale"`
		} `json:"invitation"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Invitation.Locale
}

type resendBody struct {
	Email *string `json:"email"`
}

type resendResponse struct {
	OrderID          string  `json:"order_id"`
	QueuedTickets    int     `json:"queued_tickets"`
	DifferentAddress bool    `json:"different_address"`
	RecipientMasked  string  `json:"recipient_masked"`
	ExpiresAt        *string `json:"expires_at"`
}

// HandleResend serves POST /v1/organizations/{org_id}/invitations/{order_id}/resend.
// The letter goes to the address in the body when one is given (a one-time
// address, valid 24 hours), else to the guest, else to the caller's own
// address, else to the staff member who issued the invitation. A pdf_only
// invitation can be mailed this way too: the caller asked for it.
func (h *Handler) HandleResend(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.delivery.resender == nil {
		unavailable(w, r)
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	orderID, ok := httputil.UUIDPathParam(w, r, "order_id")
	if !ok {
		return
	}
	var body resendBody
	if r.Body != nil {
		dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, r, http.StatusBadRequest, "invitation.invalid_body",
				`the body must be a JSON object with an optional "email"`, nil)
			return
		}
	}
	override := trimPtr(body.Email)
	if override != "" && !htickets.ValidResendEmail(override) {
		writeErr(w, r, http.StatusBadRequest, "invitation.invalid_email", "email is not a valid address", nil)
		return
	}

	iss, ok := h.loadIssuedOrder(w, r, orgID, orderID)
	if !ok {
		return
	}
	ctx := r.Context()
	order, err := h.queries.GetOrderByID(ctx, orderID, orgID)
	if err != nil {
		h.internal(w, r, "resend: order", err)
		return
	}
	guest := ""
	if order.BuyerEmail != nil {
		guest = strings.TrimSpace(*order.BuyerEmail)
	}
	recipient, different := h.resendRecipient(ctx, iss.order, guest, override)
	if recipient == "" {
		writeErr(w, r, http.StatusUnprocessableEntity, "invitation.no_recipient",
			`the invitation has no e-mail address: pass "email"`, nil)
		return
	}
	// A one-time address expires and never becomes the order's address; the
	// guest's own address is a plain resend.
	queued, err := h.delivery.resender.ResendInvitationOrder(ctx, r, order, recipient, different)
	if err != nil {
		if errors.Is(err, htickets.ErrNoActiveTickets) {
			writeErr(w, r, http.StatusConflict, "invitation.no_active_tickets", "every ticket of this invitation was cancelled", nil)
			return
		}
		h.logger.Error("invitation.resend: enqueue failed",
			slog.String("order_id", orderID.String()), slog.String("error", err.Error()))
		writeErr(w, r, http.StatusInternalServerError, "invitation.resend_failed", "the tickets could not be queued", nil)
		return
	}
	resp := resendResponse{
		OrderID: orderID.String(), QueuedTickets: queued, DifferentAddress: different,
		RecipientMasked: htickets.MaskEmail(recipient),
	}
	if different {
		exp := time.Now().UTC().Add(htickets.OneTimeAddressTTL).Format(time.RFC3339)
		resp.ExpiresAt = &exp
	}
	httputil.WriteJSON(w, http.StatusAccepted, resp)
}

// resendRecipient picks the address of a resend and reports whether it is a
// one-time address (anything but the guest's own).
func (h *Handler) resendRecipient(ctx context.Context, o gen.InvitationOrderRow, guest, override string) (string, bool) {
	if override != "" {
		return override, !strings.EqualFold(override, guest)
	}
	if guest != "" {
		return guest, false
	}
	// No guest address: the caller's own, then the issuer's. Neither is the
	// order's address, so both are one-time.
	if actor, ok := auth.ActorFromContext(ctx); ok &&
		(actor.Type == auth.ActorTypeUser || actor.Type == auth.ActorTypeStubUser) {
		if uid, err := uuid.Parse(actor.ID); err == nil {
			if u, err := h.queries.GetUserByID(ctx, uid); err == nil && strings.TrimSpace(u.Email) != "" {
				return strings.TrimSpace(u.Email), true
			}
		}
	}
	if o.IssuedBy != nil {
		if u, err := h.queries.GetUserByID(ctx, *o.IssuedBy); err == nil && strings.TrimSpace(u.Email) != "" {
			return strings.TrimSpace(u.Email), true
		}
	}
	return "", false
}

// notifyGuestCancelled queues the letter that tells the guest their
// invitation was cancelled (spec 46, section 3.4, decision R-9). It is a
// no-op unless the order carries the guest's own address (delivery my_email
// and pdf_only orders have none: the guest was never written to) and
// returns nothing: a failure to queue is logged, never allowed to undo a
// cancellation that already committed.
func (h *Handler) notifyGuestCancelled(ctx context.Context, o gen.InvitationOrderRow, cancelledTicketIDs []string) {
	if len(cancelledTicketIDs) == 0 {
		return
	}
	info, err := h.queries.GetInvitationDeliveryByCheckoutSession(ctx, o.CheckoutSessionID)
	if err != nil {
		h.logger.Warn("invitation.cancel: guest address lookup failed; no letter queued",
			slog.String("order_id", o.ID.String()), slog.String("error", err.Error()))
		return
	}
	if info.BuyerEmail == "" {
		return
	}
	if _, err := worker.EnqueueInTx(ctx, h.queries.DB(), delivery.JobTypeInvitationCancelEmail,
		delivery.InvitationCancelPayload{OrderID: o.ID.String(), TicketIDs: cancelledTicketIDs},
		delivery.InvitationCancelEmailMaxAttempts); err != nil {
		h.logger.Error("invitation.cancel: could not queue the guest letter",
			slog.String("order_id", o.ID.String()), slog.String("error", err.Error()))
	}
}
