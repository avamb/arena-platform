// order_resend.go — EC-13 (spec 35 §6.4): an operator sends the tickets of a
// whole ORDER again.
//
//	POST /v1/organizations/{org_id}/orders/{order_id}/resend-tickets   (ticket.update)
//	body: {"email": "<optional one-time address>"}
//
// The existing admin route (admin_ticket_delivery.go) resends ONE ticket for the
// support console. This one speaks about an order: it requeues the delivery job
// of every ACTIVE ticket (a refunded or cancelled ticket is never mailed) with
// RequeueDeliveryJob — delivery_jobs holds one row per ticket and the plain
// insert is a no-op on conflict, which is why the first resend silently did
// nothing (see the AGENTS.md note) — and the worker's order-letter claim then
// folds the requeued jobs, which all carry the same recipient, into ONE e-mail.
//
// A different address is a ONE-TIME address of this resend. It lives on the
// delivery job and in the worker payload (recipient_expires_at, 24 hours) and
// never on the order, whose buyer e-mail does not change. Everything — the
// requeues, the worker jobs, the order-history row and the audit event — is one
// transaction, so a failure leaves no half-queued letter.
package htickets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

const (
	// OrderResendEventType is the order_events.type of one resend.
	OrderResendEventType = "tickets_resent"
	// OrderResendAuditAction is the audit_events.action of one resend.
	OrderResendAuditAction = "v1.order.tickets_resend"
	// OneTimeAddressTTL is how long a one-time address stays valid.
	OneTimeAddressTTL = 24 * time.Hour

	orderSourceGateway = "bil24_gateway"
	maxEmailLen        = 254
)

type orderResendRequest struct {
	Email *string `json:"email"`
}

// HandleResendOrderTickets serves POST /v1/organizations/{org_id}/orders/{order_id}/resend-tickets.
// The caller (the *Server shim) has already checked the organization membership
// and the ticket.update permission.
func (h *Handler) HandleResendOrderTickets(w http.ResponseWriter, r *http.Request) {
	if h.deliveryJobQueries == nil || h.ticketQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "delivery store is not available", r))
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
	ctx := r.Context()

	override, ok := h.decodeResendEmail(w, r)
	if !ok {
		return
	}

	// A foreign organization's order is indistinguishable from a missing one.
	order, err := h.ticketQueries.GetOrderByID(ctx, orderID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"orders.not_found", "order not found", r))
			return
		}
		h.logger.Error("order_resend: load order failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"order_resend.internal", "failed to load order", r))
		return
	}

	// A selling site writes its own letters: the same decision a session
	// change makes (sessionchange.ChannelHasSite), plus the gateway source.
	site := order.Source == orderSourceGateway
	if !site {
		if site, err = sessionchange.ChannelHasSite(ctx, h.ticketQueries.DB(), order.ChannelID); err != nil {
			h.logger.Error("order_resend: site route lookup failed", slog.Any("error", err))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"order_resend.internal", "failed to check the sales channel", r))
			return
		}
	}
	if site {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"order.seller_site_order", "this order was sold through a seller's own site, which sends its letters itself", r))
		return
	}
	if order.Status != "paid" && order.Status != "partially_refunded" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"order.not_paid", "tickets can be sent only for a paid order", r))
		return
	}

	rows, err := h.ticketQueries.ListOrderTicketDetails(ctx, order.ID)
	if err != nil {
		h.logger.Error("order_resend: list tickets failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"order_resend.internal", "failed to load the order's tickets", r))
		return
	}
	var active []gen.OrderTicketDetailRow
	for _, t := range rows {
		if t.Status == "active" {
			active = append(active, t)
		}
	}
	if len(active) == 0 {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"order.no_active_tickets", "the order has no active tickets to send", r))
		return
	}

	// The order's own address: the buyer's, else the first holder's.
	orderEmail := ""
	if order.BuyerEmail != nil {
		orderEmail = strings.TrimSpace(*order.BuyerEmail)
	}
	for _, t := range active {
		if orderEmail != "" {
			break
		}
		if t.HolderEmail != nil {
			orderEmail = strings.TrimSpace(*t.HolderEmail)
		}
	}
	recipient, different := orderEmail, false
	if override != "" {
		recipient = override
		different = !strings.EqualFold(override, orderEmail)
	}
	if recipient == "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"order.no_recipient", "the order has no e-mail address: pass \"email\"", r))
		return
	}

	var expires *time.Time
	if different {
		e := time.Now().UTC().Add(OneTimeAddressTTL)
		expires = &e
	}

	queued, err := h.queueOrderResend(ctx, r, order, active, recipient, expires, different)
	if err != nil {
		h.logger.Error("order_resend: enqueue failed",
			slog.String("order_id", order.ID.String()), slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"order_resend.enqueue_failed", "failed to queue the tickets", r))
		return
	}

	resp := map[string]any{
		"order_id":          order.ID.String(),
		"queued_tickets":    queued,
		"different_address": different,
		"recipient_masked":  MaskEmail(recipient),
		"expires_at":        nil,
	}
	if expires != nil {
		resp["expires_at"] = expires.Format(time.RFC3339)
	}
	httputil.WriteJSON(w, http.StatusAccepted, resp)
}

// decodeResendEmail reads the optional body. It returns the validated address
// ("" when none was given); on a bad body it has already answered.
func (h *Handler) decodeResendEmail(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body orderResendRequest
	if r.Body != nil {
		dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"order.invalid_body", "body must be a JSON object with an optional \"email\"", r))
			return "", false
		}
	}
	if body.Email == nil {
		return "", true
	}
	addr := strings.TrimSpace(*body.Email)
	if addr == "" {
		return "", true
	}
	if !ValidResendEmail(addr) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"order.invalid_email", "email is not a valid address", r))
		return "", false
	}
	return addr, true
}

// ValidResendEmail accepts a bare address of the form local@domain.tld.
func ValidResendEmail(s string) bool {
	if s == "" || len(s) > maxEmailLen || strings.ContainsAny(s, " \t\r\n<>,;\"") {
		return false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s {
		return false
	}
	at := strings.LastIndex(s, "@")
	if at < 1 {
		return false
	}
	domain := s[at+1:]
	return strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}

// MaskEmail hides all but the first character of the local part and of the
// domain's first label: "anna@example.com" -> "a***@e*****.com".
func MaskEmail(s string) string {
	at := strings.LastIndex(s, "@")
	if at < 1 {
		return "***"
	}
	local, domain := s[:at], s[at+1:]
	first, rest := domain, ""
	if dot := strings.Index(domain, "."); dot > 0 {
		first, rest = domain[:dot], domain[dot:]
	}
	return maskWord(local) + "@" + maskWord(first) + rest
}

func maskWord(w string) string {
	r := []rune(w)
	if len(r) == 0 {
		return ""
	}
	return string(r[:1]) + strings.Repeat("*", len(r)-1)
}

// queueOrderResend writes the whole resend in one transaction and returns the
// number of tickets queued.
func (h *Handler) queueOrderResend(
	ctx context.Context,
	r *http.Request,
	order gen.OrderRow,
	active []gen.OrderTicketDetailRow,
	recipient string,
	expires *time.Time,
	different bool,
) (int, error) {
	// Everything read-only the payloads need is loaded BEFORE the transaction
	// opens: a pool query while holding row locks is a deadlock waiting for a
	// full pool (AGENTS.md).
	template := ""
	if order.Source == "complimentary" {
		template = delivery.TemplateInvitation
	}
	locale := h.BuyerLocaleFor(ctx, nil, order.CheckoutSessionID)
	payloads := make([]delivery.Payload, 0, len(active))
	for _, t := range active {
		p := delivery.Payload{
			TicketID:           t.TicketID.String(),
			Template:           template,
			Locale:             locale,
			RecipientExpiresAt: expires,
		}
		if t.SeatSector != nil {
			p.SeatSector = *t.SeatSector
		}
		if t.SeatRow != nil {
			p.SeatRow = *t.SeatRow
		}
		if t.SeatNumber != nil {
			p.SeatNumber = *t.SeatNumber
		}
		h.applyOrgBranding(ctx, &p, t.TicketID)
		payloads = append(payloads, p)
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.deliveryJobQueries.WithTx(tx)

	for i, t := range active {
		if _, err := q.RequeueDeliveryJob(ctx, t.TicketID, &recipient); err != nil {
			return 0, err
		}
		if _, err := worker.EnqueueInTx(ctx, tx, delivery.JobType, payloads[i], 5); err != nil {
			return 0, err
		}
	}

	actor, _ := auth.ActorFromContext(ctx)
	actorLabel := "system"
	if actor.ID != "" {
		actorLabel = "user:" + actor.ID
	}
	// The history row names the number of tickets and nothing about the
	// address: order history is read by every member.
	hist, _ := json.Marshal(map[string]any{
		"tickets":           len(active),
		"different_address": different,
	})
	if _, err := q.InsertOrderEvent(ctx, order.ID, OrderResendEventType, actorLabel, hist); err != nil {
		return 0, err
	}

	if h.audit != nil {
		ev := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       OrderResendAuditAction,
			ResourceType: "order",
			ResourceID:   order.ID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"org_id":            order.OrgID.String(),
				"tickets":           len(active),
				"different_address": different,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(active), nil
}

// applyOrgBranding copies the owning organization's branding onto the payload,
// exactly as the post-issuance enqueuer does, so the resent letter carries the
// same logo, legal block and sender as the original. Best effort.
func (h *Handler) applyOrgBranding(ctx context.Context, p *delivery.Payload, ticketID uuid.UUID) {
	b, err := h.deliveryJobQueries.GetOrgBrandingByTicketID(ctx, ticketID)
	if err != nil {
		h.logger.Warn("order_resend: org branding lookup failed — using platform defaults",
			slog.String("ticket_id", ticketID.String()), slog.Any("error", err))
		return
	}
	p.OrgName = b.Name
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&p.OrgWebsiteURL, b.WebsiteURL)
	set(&p.OrgLegalName, b.LegalName)
	set(&p.OrgLegalAddressLine1, b.LegalAddressLine1)
	set(&p.OrgLegalAddressLine2, b.LegalAddressLine2)
	set(&p.OrgLegalAddressPostal, b.LegalAddressPostalCode)
	set(&p.OrgLegalAddressCity, b.LegalAddressCity)
	set(&p.OrgLegalAddressCountry, b.LegalAddressCountry)
	set(&p.OrgContactEmail, b.ContactEmail)
	set(&p.SenderEmail, b.SenderEmail)
	if b.LogoMediaID != nil {
		p.OrgLogoMediaID = b.LogoMediaID.String()
	}
	p.SenderVerificationStatus = b.SenderVerificationStatus
}
