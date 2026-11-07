// order_reissue.go — spec 63, stage R-2: replace the tickets of an order whose
// letter went to a wrong address.
//
//	GET  /v1/organizations/{org_id}/orders/{order_id}/reissue-options      (ticket.update)
//	POST /v1/organizations/{org_id}/orders/{order_id}/reissue-tickets      (ticket.update, Idempotency-Key)
//	POST /v1/organizations/{org_id}/orders/{order_id}/reissues/{reissue_id}/resend (ticket.update)
//	GET  /v1/organizations/{org_id}/orders/{order_id}/reissues             (order.read)
//
// The work is ticketreissue's; this file only reads the request, checks it,
// builds the letter's delivery payloads and maps the answers. The caller (the
// *Server shim) has checked the membership; the mount checks the permission.
package htickets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ticketreissue"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

type reissueRequest struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

// refuseAPIKey answers 403 for an organization API key: a reissue changes the
// buyer's data and reads it (spec 63 section 4.1).
func refuseAPIKey(w http.ResponseWriter, r *http.Request) bool {
	if actor, ok := auth.ActorFromContext(r.Context()); ok && actor.IsService() {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"reissue.api_key_forbidden",
			"an API key cannot replace tickets; a member of the organization does it signed in", r))
		return true
	}
	return false
}

func (h *Handler) reissueReady(w http.ResponseWriter, r *http.Request) bool {
	if h.pool == nil || h.ticketQueries == nil || h.deliveryJobQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "ticket store is not available", r))
		return false
	}
	return true
}

// HandleReissueOptions serves GET .../reissue-options: the check screen.
func (h *Handler) HandleReissueOptions(w http.ResponseWriter, r *http.Request) {
	if refuseAPIKey(w, r) || !h.reissueReady(w, r) {
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
	opts, err := ticketreissue.LoadOptions(r.Context(), h.ticketQueries.DB(), orgID, orderID)
	if err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	tickets := make([]map[string]any, 0, len(opts.Tickets))
	for _, t := range opts.Tickets {
		item := map[string]any{
			"ticket_id":  t.TicketID.String(),
			"number":     t.Number,
			"seat":       t.Seat,
			"category":   t.Category,
			"status":     t.Status,
			"reissuable": t.Reissuable,
		}
		if t.Reason != "" {
			item["reason"] = t.Reason
		}
		if t.ScannedAt != nil {
			item["scanned_at"] = t.ScannedAt.UTC().Format(time.RFC3339)
		}
		tickets = append(tickets, item)
	}
	resp := map[string]any{
		"route":          opts.Route,
		"order_id":       opts.OrderID.String(),
		"order_number":   opts.OrderNumber,
		"order_status":   opts.OrderStatus,
		"order_email":    opts.OrderEmail,
		"buyer_name":     opts.BuyerName,
		"buyer_phone":    opts.BuyerPhone,
		"venue_timezone": opts.VenueTimezone,
		"tickets":        tickets,
		"blocked":        nilIfEmpty(opts.Blocked),
		"provider":       providerJSON(opts.Provider),
	}
	if opts.Open != nil {
		resp["pending_reissue"] = map[string]any{
			"id":         opts.Open.ID.String(),
			"state":      opts.Open.State,
			"new_email":  opts.Open.NewEmail,
			"created_at": opts.Open.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// HandleReissueTickets serves POST .../reissue-tickets.
func (h *Handler) HandleReissueTickets(w http.ResponseWriter, r *http.Request) {
	if refuseAPIKey(w, r) || !h.reissueReady(w, r) {
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
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"reissue.idempotency_key_required", "send an Idempotency-Key header (at most 200 characters)", r))
		return
	}
	var body reissueRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"reissue.invalid_body", "body must be a JSON object with email, reason and an optional note", r))
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if !ValidResendEmail(body.Email) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"reissue.invalid_email", "email is not a valid address", r))
		return
	}
	if !ticketreissue.ValidReason(body.Reason) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"reissue.invalid_reason", "reason must be sent_to_wrong_address, forwarded_to_stranger or other", r))
		return
	}
	if len([]rune(strings.TrimSpace(body.Note))) > ticketreissue.MaxNote {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"reissue.note_too_long", "the note is longer than 500 characters", r))
		return
	}
	ctx := r.Context()

	// Everything the letter's payloads need is read BEFORE the transaction
	// opens (a pool query while holding row locks waits for a full pool).
	templates, err := h.reissuePayloadTemplates(ctx, orgID, orderID)
	if err != nil {
		h.writeReissueError(w, r, err)
		return
	}

	actor, _ := auth.ActorFromContext(ctx)
	actorType := "user"
	if auth.HasSuperadminOrgAccess(ctx) {
		actorType = "superadmin"
	}
	via, _ := audit.ClientChannelFromContext(ctx)
	in := ticketreissue.Input{
		OrgID: orgID, OrderID: orderID,
		Email: body.Email, Reason: body.Reason, Note: body.Note,
		IdempotencyKey: key,
		ActorType:      actorType, ActorID: actor.ID, Via: via,
		ActorLabel: "user:" + actor.ID,
		Outbox:     h.outboxWriter,
		Enqueue: func(ctx context.Context, tx pgx.Tx, pairs []ticketreissue.Pair, recipient string) error {
			q := h.deliveryJobQueries.WithTx(tx)
			for _, p := range pairs {
				payload := templates[p.OldTicketID]
				payload.TicketID = p.NewTicketID.String()
				if _, err := q.InsertDeliveryJob(ctx, p.NewTicketID, &recipient); err != nil {
					return err
				}
				if _, err := worker.EnqueueInTx(ctx, tx, delivery.JobType, payload, 5); err != nil {
					return err
				}
			}
			return nil
		},
		Audit: func(ctx context.Context, tx pgx.Tx, meta map[string]any) error {
			if h.audit == nil {
				return nil
			}
			return h.audit.WriteTx(ctx, tx, audit.Event{
				OccurredAt:   time.Now().UTC(),
				ActorType:    "user",
				ActorID:      actor.ID,
				Action:       ticketreissue.AuditAction,
				ResourceType: "order",
				ResourceID:   orderID.String(),
				RequestID:    logging.RequestID(ctx),
				TraceID:      logging.TraceID(ctx),
				IP:           httputil.ExtractClientIP(r),
				Metadata:     meta,
			})
		},
	}
	res, err := ticketreissue.Reissue(ctx, h.pool, in)
	if err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	if !res.Replayed {
		h.logger.Info("ticket reissue: queued",
			slog.String("order_id", orderID.String()),
			slog.String("reissue_id", res.ReissueID.String()),
			slog.Int("tickets", len(res.Pairs)))
	}
	httputil.WriteJSON(w, http.StatusOK, reissueResultJSON(res))
}

// reissuePayloadTemplates builds one delivery payload per active ticket of
// the order, keyed by the OLD ticket id: the replacement keeps the seat, the
// language and the organizer's branding.
func (h *Handler) reissuePayloadTemplates(ctx context.Context, orgID, orderID uuid.UUID) (map[uuid.UUID]delivery.Payload, error) {
	order, err := h.ticketQueries.GetOrderByID(ctx, orderID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ticketreissue.ErrOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := h.ticketQueries.ListOrderTicketDetails(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	locale := h.BuyerLocaleFor(ctx, nil, order.CheckoutSessionID)
	// An invitation order is replaced by an invitation letter (spec 63 R-5),
	// the same rule the resend applies.
	template := ""
	if order.Source == "complimentary" {
		template = delivery.TemplateInvitation
	}
	out := make(map[uuid.UUID]delivery.Payload, len(rows))
	for _, t := range rows {
		if t.Status != "active" {
			continue
		}
		p := delivery.Payload{Locale: locale, Template: template}
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
		out[t.TicketID] = p
	}
	return out, nil
}

// HandleReissueResend serves POST .../reissues/{reissue_id}/resend: the letter
// of an operation that has not been applied is queued again, to the same
// address, with the same tickets. No barcode is minted.
func (h *Handler) HandleReissueResend(w http.ResponseWriter, r *http.Request) {
	if refuseAPIKey(w, r) || !h.reissueReady(w, r) {
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
	reissueID, ok := httputil.UUIDPathParam(w, r, "reissue_id")
	if !ok {
		return
	}
	ctx := r.Context()
	entry, err := ticketreissue.Get(ctx, h.ticketQueries.DB(), orgID, orderID, reissueID)
	if err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	if entry.State == ticketreissue.StateApplied {
		h.writeReissueError(w, r, ticketreissue.ErrNotResendable)
		return
	}
	order, err := h.ticketQueries.GetOrderByID(ctx, orderID, orgID)
	if err != nil {
		h.writeReissueError(w, r, ticketreissue.ErrOrderNotFound)
		return
	}
	locale := h.BuyerLocaleFor(ctx, nil, order.CheckoutSessionID)
	template := ""
	if order.Source == "complimentary" {
		template = delivery.TemplateInvitation
	}
	payloads := make([]delivery.Payload, 0, len(entry.Pairs))
	for _, p := range entry.Pairs {
		pl := delivery.Payload{TicketID: p.NewTicketID.String(), Locale: locale, Template: template}
		h.applyOrgBranding(ctx, &pl, p.NewTicketID)
		payloads = append(payloads, pl)
	}
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.deliveryJobQueries.WithTx(tx)
	recipient := entry.NewEmail
	for i, p := range entry.Pairs {
		if _, err := q.RequeueDeliveryJob(ctx, p.NewTicketID, &recipient); err != nil {
			h.writeReissueError(w, r, err)
			return
		}
		if _, err := worker.EnqueueInTx(ctx, tx, delivery.JobType, payloads[i], 5); err != nil {
			h.writeReissueError(w, r, err)
			return
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE ticket_reissues SET state = 'pending', updated_at = now()
		WHERE id = $1 AND state = 'send_failed'`, reissueID); err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	actor, _ := auth.ActorFromContext(ctx)
	hist, _ := json.Marshal(map[string]any{"reissue_id": reissueID.String(), "tickets": len(entry.Pairs)})
	if _, err := q.InsertOrderEvent(ctx, orderID, "reissue_resent", "user:"+actor.ID, hist); err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{
		"reissue_id": reissueID.String(),
		"state":      ticketreissue.StatePending,
		"letter":     "queued",
	})
}

// HandleReissueHistory serves GET .../reissues: every operation of the order,
// newest first, with both addresses (members only: an API key is refused).
func (h *Handler) HandleReissueHistory(w http.ResponseWriter, r *http.Request) {
	if refuseAPIKey(w, r) || !h.reissueReady(w, r) {
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
	if _, err := h.ticketQueries.GetOrderByID(ctx, orderID, orgID); err != nil {
		h.writeReissueError(w, r, ticketreissue.ErrOrderNotFound)
		return
	}
	entries, err := ticketreissue.History(ctx, h.ticketQueries.DB(), orgID, orderID)
	if err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	provider := ticketreissue.Provider{}
	if opts, oErr := ticketreissue.LoadOptions(ctx, h.ticketQueries.DB(), orgID, orderID); oErr == nil {
		provider = opts.Provider
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"reissues": entriesJSON(entries),
		"provider": providerJSON(provider),
	})
}

func reissueResultJSON(res ticketreissue.Result) map[string]any {
	return map[string]any{
		"reissue_id":       res.ReissueID.String(),
		"state":            res.State,
		"replayed":         res.Replayed,
		"recipient_masked": ticketreissue.MaskEmail(res.NewEmail),
		"tickets":          pairsJSON(res.Pairs),
		"letter":           "queued",
	}
}

func pairsJSON(pairs []ticketreissue.Pair) []map[string]any {
	out := make([]map[string]any, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, map[string]any{
			"old_ticket_id":       p.OldTicketID.String(),
			"old_number":          p.OldNumber,
			"new_ticket_id":       p.NewTicketID.String(),
			"new_number":          p.NewNumber,
			"seat":                p.Seat,
			"category":            p.Category,
			"old_code_scanned_at": timeOrNil(p.OldScannedAt),
		})
	}
	return out
}

func providerJSON(p ticketreissue.Provider) any {
	if p.Name == "" {
		return nil
	}
	return map[string]any{"name": p.Name, "title": p.Title, "email_updatable": p.EmailUpdatable}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func timeOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// writeReissueError maps ticketreissue's errors to answers (spec 63 7.1).
func (h *Handler) writeReissueError(w http.ResponseWriter, r *http.Request, err error) {
	var refusal *ticketreissue.TicketRefusal
	switch {
	case errors.Is(err, ticketreissue.ErrOrderNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("orders.not_found", "order not found", r))
	case errors.Is(err, ticketreissue.ErrInvitationNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("complimentary.not_found", "invitation not found", r))
	case errors.Is(err, ticketreissue.ErrInvitationNotIssued):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("reissue.invitation_not_issued",
			"only an issued invitation can be replaced; a revoked one is issued again", r))
	case errors.Is(err, ticketreissue.ErrReissueNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("reissue.not_found", "reissue not found", r))
	case errors.Is(err, ticketreissue.ErrNotPaid):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("order.not_paid", "tickets can be replaced only for a paid order", r))
	case errors.Is(err, ticketreissue.ErrSellerSite):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("order.seller_site_order",
			"this order was sold through a seller's own site, which sends its letters itself", r))
	case errors.Is(err, ticketreissue.ErrNothingToReissue):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("reissue.nothing_to_reissue", "the order has no active tickets", r))
	case errors.Is(err, ticketreissue.ErrSessionClosed):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("reissue.session_closed", "the event date is cancelled or over", r))
	case errors.Is(err, ticketreissue.ErrInProgress):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("reissue.in_progress",
			"an earlier replacement of this order has not been sent yet", r))
	case errors.Is(err, ticketreissue.ErrKeyReused):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("reissue.idempotency_key_reused",
			"this Idempotency-Key was used for another request", r))
	case errors.Is(err, ticketreissue.ErrNotResendable):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("reissue.already_applied",
			"the letter of this replacement has already gone out", r))
	case errors.As(err, &refusal):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails("reissue.ticket_not_reissuable",
			"a ticket of the order cannot be replaced now", r,
			map[string]any{"ticket_number": refusal.Number, "reason": refusal.Reason}))
	default:
		h.logger.Error("ticket reissue failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reissue.internal", "failed to replace the tickets", r))
	}
}

// entriesJSON is the history answer of an order or an invitation.
func entriesJSON(entries []ticketreissue.Entry) []map[string]any {
	items := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		items = append(items, map[string]any{
			"id":                   e.ID.String(),
			"state":                e.State,
			"old_email":            e.OldEmail,
			"new_email":            e.NewEmail,
			"reason":               e.Reason,
			"note":                 e.Note,
			"old_code_scanned_at":  timeOrNil(e.OldCodeScannedAt),
			"applied_at":           timeOrNil(e.AppliedAt),
			"letter_failed_at":     timeOrNil(e.LetterFailedAt),
			"provider_email_state": nilIfEmpty(e.ProviderEmailState),
			"actor_type":           e.ActorType,
			"actor_id":             nilIfEmpty(e.ActorID),
			"via":                  nilIfEmpty(e.Via),
			"created_at":           e.CreatedAt.UTC().Format(time.RFC3339),
			"tickets":              pairsJSON(e.Pairs),
		})
	}
	return items
}
