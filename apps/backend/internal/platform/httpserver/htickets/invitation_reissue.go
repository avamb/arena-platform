// invitation_reissue.go — replacing the tickets of an invitation without an
// order (spec 63, stage R-5): the same check screen, replacement, resend and
// history as an order's, keyed by the complimentary issuance.
package htickets

import (
	"context"
	"encoding/json"
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

// HandleInvitationReissueOptions serves GET
// .../complimentary/{id}/reissue-options.
func (h *Handler) HandleInvitationReissueOptions(w http.ResponseWriter, r *http.Request) {
	orgID, issuanceID, ok := h.invitationPath(w, r)
	if !ok {
		return
	}
	opts, err := ticketreissue.LoadInvitationOptions(r.Context(), h.ticketQueries.DB(), orgID, issuanceID)
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
		"invitation_id":     opts.IssuanceID.String(),
		"invitation_status": opts.Status,
		"guest_email":       opts.Email,
		"venue_timezone":    opts.VenueTimezone,
		"tickets":           tickets,
		"blocked":           nilIfEmpty(opts.Blocked),
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

// HandleInvitationReissue serves POST .../complimentary/{id}/reissue-tickets.
func (h *Handler) HandleInvitationReissue(w http.ResponseWriter, r *http.Request) {
	orgID, issuanceID, ok := h.invitationPath(w, r)
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
	// The letter's payloads are read before the transaction opens.
	opts, err := ticketreissue.LoadInvitationOptions(ctx, h.ticketQueries.DB(), orgID, issuanceID)
	if err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	payloads := make(map[uuid.UUID]delivery.Payload, len(opts.Tickets))
	for _, t := range opts.Tickets {
		if !t.Reissuable {
			continue
		}
		p := delivery.Payload{Template: delivery.TemplateInvitation, Locale: h.BuyerLocaleForTicket(ctx, t.TicketID)}
		h.applyOrgBranding(ctx, &p, t.TicketID)
		payloads[t.TicketID] = p
	}

	actor, _ := auth.ActorFromContext(ctx)
	actorType := "user"
	if auth.HasSuperadminOrgAccess(ctx) {
		actorType = "superadmin"
	}
	via, _ := audit.ClientChannelFromContext(ctx)
	in := ticketreissue.Input{
		OrgID: orgID, IssuanceID: issuanceID,
		Email: body.Email, Reason: body.Reason, Note: body.Note,
		IdempotencyKey: key,
		ActorType:      actorType, ActorID: actor.ID, Via: via,
		ActorLabel: "user:" + actor.ID,
		Outbox:     h.outboxWriter,
		Enqueue: func(ctx context.Context, tx pgx.Tx, pairs []ticketreissue.Pair, recipient string) error {
			q := h.deliveryJobQueries.WithTx(tx)
			for _, p := range pairs {
				payload := payloads[p.OldTicketID]
				payload.Template = delivery.TemplateInvitation
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
				ResourceType: "complimentary_issuance",
				ResourceID:   issuanceID.String(),
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
		h.logger.Info("ticket reissue: invitation queued",
			slog.String("invitation_id", issuanceID.String()),
			slog.String("reissue_id", res.ReissueID.String()),
			slog.Int("tickets", len(res.Pairs)))
	}
	httputil.WriteJSON(w, http.StatusOK, reissueResultJSON(res))
}

// HandleInvitationReissueResend serves POST
// .../complimentary/{id}/reissues/{reissue_id}/resend.
func (h *Handler) HandleInvitationReissueResend(w http.ResponseWriter, r *http.Request) {
	orgID, issuanceID, ok := h.invitationPath(w, r)
	if !ok {
		return
	}
	reissueID, ok := httputil.UUIDPathParam(w, r, "reissue_id")
	if !ok {
		return
	}
	ctx := r.Context()
	entry, err := ticketreissue.GetInvitation(ctx, h.ticketQueries.DB(), orgID, issuanceID, reissueID)
	if err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	if entry.State == ticketreissue.StateApplied {
		h.writeReissueError(w, r, ticketreissue.ErrNotResendable)
		return
	}
	payloads := make([]delivery.Payload, 0, len(entry.Pairs))
	for _, p := range entry.Pairs {
		pl := delivery.Payload{TicketID: p.NewTicketID.String(), Template: delivery.TemplateInvitation,
			Locale: h.BuyerLocaleForTicket(ctx, p.NewTicketID)}
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

// HandleInvitationReissueHistory serves GET .../complimentary/{id}/reissues.
func (h *Handler) HandleInvitationReissueHistory(w http.ResponseWriter, r *http.Request) {
	orgID, issuanceID, ok := h.invitationPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, err := ticketreissue.LoadInvitationOptions(ctx, h.ticketQueries.DB(), orgID, issuanceID); err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	entries, err := ticketreissue.InvitationHistory(ctx, h.ticketQueries.DB(), orgID, issuanceID)
	if err != nil {
		h.writeReissueError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"reissues": entriesJSON(entries),
		"provider": nil,
	})
}

// invitationPath reads org_id and the issuance id of an invitation route, and
// refuses an API key or a server without a database.
func (h *Handler) invitationPath(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	if refuseAPIKey(w, r) || !h.reissueReady(w, r) {
		return uuid.Nil, uuid.Nil, false
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	issuanceID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return orgID, issuanceID, true
}
