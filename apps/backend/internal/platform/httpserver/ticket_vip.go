package httpserver

// ticket_vip.go — PATCH /v1/organizations/{org_id}/tickets/{ticket_id}/vip
// (task V1, migration 0164): the organizer's private VIP mark of one ticket,
// bought or an invitation. Right: ticket.update (owner and manager). The
// ticket's organization comes from GetTicketOrgID (not from its session), so
// a ticket of a cancelled session is still reachable, and a foreign ticket
// reads as the route's own 404. The mark is never printed on the PDF e-ticket
// and never written in a letter to the buyer.

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// handleSetTicketVIP sets or clears the VIP mark. It is idempotent: a call
// that repeats the stored value answers 200 with changed=false and writes
// neither a row nor an audit event.
func (s *Server) handleSetTicketVIP(w http.ResponseWriter, r *http.Request) {
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !s.enforceMembershipInOrg(w, r, orgID) {
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
		VIP *bool `json:"vip"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<12))
	if err != nil || json.Unmarshal(raw, &body) != nil || body.VIP == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("ticket.invalid_vip",
			"the body must be a JSON object with a boolean field vip", r))
		return
	}
	ctx := r.Context()
	q := gen.New(s.pgxPool)
	owner, err := q.GetTicketOrgID(ctx, ticketID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != orgID) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("ticket.not_found", "ticket not found", r))
		return
	}
	if err != nil {
		s.logger.Error("ticket vip: ticket lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("ticket.lookup_failed", "failed to look up the ticket", r))
		return
	}
	actor, via := refundActor(r)
	var by *uuid.UUID
	if id, perr := uuid.Parse(actor.ID); perr == nil {
		by = &id
	}
	changed := false
	err = pgx.BeginFunc(ctx, s.pgxPool, func(tx pgx.Tx) error {
		var err error
		if changed, err = q.WithTx(tx).SetTicketVIP(ctx, ticketID, *body.VIP, by); err != nil {
			return err
		}
		// A repeat writes no audit event. A server built without an audit
		// writer (tests, tools) skips it, exactly like the sibling mutations
		// (review-hold clear, bank accounts, promoters): the writer is optional
		// wiring, not a precondition of the change.
		if !changed || s.audit == nil {
			return nil
		}
		return s.audit.WriteTx(ctx, tx, audit.Event{ActorType: actor.Type, ActorID: actor.ID,
			Action: "v1.ticket.vip_set", ResourceType: "ticket", ResourceID: ticketID.String(),
			Metadata: map[string]any{"org_id": orgID.String(), "ticket_id": ticketID.String(),
				"vip": *body.VIP, "via": via}})
	})
	if err != nil {
		s.logger.Error("ticket vip: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("ticket.vip_failed", "failed to change the VIP mark", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ticket_id": ticketID.String(), "vip": *body.VIP, "changed": changed})
}
