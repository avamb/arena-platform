package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// handleClearTicketReviewHold lifts a ticket's review hold (a partial refund
// that named no ticket put it there, so no refund of the order could go
// through the API or the bot). Platform superadmin only, with X-Admin-Reason
// and a note; the change is audited with the reason the hold carried.
func (s *Server) handleClearTicketReviewHold(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !s.enforceMembershipInOrg(w, r, orgID) {
		return
	}
	if !auth.HasSuperadminOrgAccess(r.Context()) {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope("ticket.review_hold_superadmin_only",
			"only a platform operator can lift a review hold", r))
		return
	}
	ticketID, ok := httputil.UUIDPathParam(w, r, "ticket_id")
	if !ok {
		return
	}
	if s.pgxPool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil || json.Unmarshal(raw, &body) != nil || strings.TrimSpace(body.Note) == "" || len([]rune(body.Note)) > 1000 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(refunds.CodeInvalidRequest,
			"a note of up to 1000 characters saying what was decided is required", r))
		return
	}
	ctx := r.Context()
	notFound := func() {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("ticket.not_found", "ticket not found", r))
	}
	q := gen.New(s.pgxPool)
	owner, err := q.GetTicketOrgID(ctx, ticketID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != orgID) {
		notFound()
		return
	}
	if err != nil {
		s.logger.Error("review hold: ticket lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("ticket.lookup_failed", "failed to look up the ticket", r))
		return
	}
	actor, via := refundActor(r)
	var previous string
	err = pgx.BeginFunc(ctx, s.pgxPool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(review_hold_reason, '') FROM tickets WHERE id = $1 AND review_hold FOR UPDATE`,
			ticketID).Scan(&previous); err != nil {
			return err
		}
		if _, err := q.WithTx(tx).ClearTicketReviewHold(ctx, ticketID); err != nil {
			return err
		}
		if s.audit == nil {
			return nil
		}
		return s.audit.WriteTx(ctx, tx, audit.Event{ActorType: actor.Type, ActorID: actor.ID,
			Action: "v1.ticket.review_hold_clear", ResourceType: "ticket", ResourceID: ticketID.String(),
			Metadata: map[string]any{"org_id": orgID.String(), "note": strings.TrimSpace(body.Note),
				"previous_reason": previous, "via": via}})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope("ticket.not_on_review_hold", "the ticket is not on review hold", r))
		return
	}
	if err != nil {
		s.logger.Error("review hold: clearing failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("ticket.review_hold_clear_failed", "failed to lift the review hold", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ticket_id": ticketID.String(), "review_hold": false})
}
