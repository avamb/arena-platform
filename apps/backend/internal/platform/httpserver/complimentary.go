// complimentary.go implements the complimentary ticket issuance and revocation
// HTTP API (features #148 and #150).
//
// Complimentary issuances let org admins issue tickets to named recipients
// without a checkout session or payment. The batch_id provides idempotency:
// re-submitting the same (org_id, batch_id) pair is a safe no-op.
//
// Inventory is decremented atomically: ReserveCapacity + ConfirmCapacity are
// called in a single transaction so complimentary tickets consume capacity the
// same way paid tickets do, preventing over-issuance.
//
// Ticket creation (step 4) inserts one ticket row per recipient (or qty
// anonymous tickets when recipients is empty). Tickets use the
// complimentary_issuance_id FK instead of checkout_session_id.
//
// Revocation (feature #150):
//   - Checks whether any ticket has been scanned (barcode status='scanned').
//   - If scanned → transitions to 'manual_review' (409).
//   - If not scanned → atomically revokes tickets, barcodes, credentials, and
//     restores inventory capacity (RestoreSoldCapacity), then marks the
//     issuance 'revoked' (200).
//
// # Endpoints (all require JWT auth)
//
//	POST /v1/organizations/{org_id}/complimentary        — issue batch (complimentary.issue)
//	GET  /v1/organizations/{org_id}/complimentary        — list issuances (complimentary.read)
//	GET  /v1/organizations/{org_id}/complimentary/{id}   — get issuance detail (complimentary.read)
//	POST /v1/complimentary/{id}/revoke                   — revoke issuance (complimentary.issue)
package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/complimentary
// ─────────────────────────────────────────────────────────────────────────────

type createComplimentaryIssuanceRequest struct {
	SessionID  string   `json:"session_id"`
	TierID     *string  `json:"tier_id"`
	Qty        int32    `json:"qty"`
	Recipients []string `json:"recipients"`
	BatchID    string   `json:"batch_id"`
	IssuedBy   *string  `json:"issued_by"`
	Notes      *string  `json:"notes"`
}

// handleCreateComplimentaryIssuance serves POST /v1/organizations/{org_id}/complimentary.
//
// Workflow:
//  1. Parse and validate the request body.
//  2. Check idempotency: if (org_id, batch_id) already exists, return the
//     existing issuance immediately without touching inventory or tickets.
//  3. Begin transaction.
//  4. ReserveCapacity(session_id, tier_id, qty) — check and hold inventory.
//  5. ConfirmCapacity(session_id, tier_id, qty) — move held → sold.
//  6. InsertComplimentaryIssuance with status='pending'.
//  7. InsertComplimentaryTicket × qty (one per recipient, or anonymous when empty).
//  8. UpdateComplimentaryIssuanceStatus → 'issued'.
//  9. Commit. Return 201 with the issuance + issued tickets.
func (s *Server) handleCreateComplimentaryIssuance(w http.ResponseWriter, r *http.Request) {
	if s.complimentaryQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	orgIDStr := chi.URLParam(r, "org_id")
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"complimentary.invalid_org_id", "org_id must be a valid UUID", r,
		))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"complimentary.read_body_failed", "failed to read request body", r,
		))
		return
	}

	var req createComplimentaryIssuanceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"complimentary.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	// Validate session_id.
	sessionID, err := uuid.Parse(req.SessionID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"complimentary.invalid_session_id", "session_id must be a valid UUID", r,
		))
		return
	}

	// Validate qty.
	if req.Qty <= 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"complimentary.invalid_qty", "qty must be a positive integer", r,
		))
		return
	}

	// Validate batch_id.
	if req.BatchID == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"complimentary.missing_batch_id", "batch_id is required for idempotency", r,
		))
		return
	}

	// Parse optional tier_id.
	var tierID *uuid.UUID
	if req.TierID != nil && *req.TierID != "" {
		tid, err := uuid.Parse(*req.TierID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"complimentary.invalid_tier_id", "tier_id must be a valid UUID", r,
			))
			return
		}
		tierID = &tid
	}

	ctx := r.Context()

	// ── Idempotency check ────────────────────────────────────────────────────
	// If a matching (org_id, batch_id) row already exists, return it immediately
	// without re-issuing. This is the safe retry / replay path.
	existing, err := s.complimentaryQueries.GetComplimentaryIssuanceByBatchID(ctx, orgID, req.BatchID)
	if err == nil {
		// Row found — return the existing issuance.
		existingTickets, _ := s.complimentaryQueries.ListTicketsByComplimentaryIssuance(ctx, existing.ID)
		writeJSON(w, http.StatusOK, map[string]any{
			"issuance":          complimentaryIssuanceFromRow(existing),
			"tickets":           complimentaryTicketsFromRows(existingTickets),
			"idempotent_replay": true,
		})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		// Unexpected DB error during idempotency check.
		s.logger.Error("complimentary: idempotency check failed",
			slog.String("org_id", orgID.String()),
			slog.String("batch_id", req.BatchID),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.idempotency_check_failed", "failed to check idempotency", r,
		))
		return
	}

	// ── Transactional issuance ───────────────────────────────────────────────
	if s.inventoryQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "inventory service is not available", r,
		))
		return
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	invQ := s.inventoryQueries.WithTx(tx)
	complQ := s.complimentaryQueries.WithTx(tx)

	// Step 4: ReserveCapacity — check and hold qty units.
	if _, err := invQ.ReserveCapacity(ctx, sessionID, tierID, req.Qty); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"complimentary.capacity_overflow",
				"insufficient inventory capacity for this complimentary issuance",
				r,
			))
			return
		}
		s.logger.Error("complimentary: reserve capacity failed",
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.capacity_failed", "failed to reserve inventory capacity", r,
		))
		return
	}

	// Step 5: ConfirmCapacity — move held → sold (separate from sales counter path).
	if _, err := invQ.ConfirmCapacity(ctx, sessionID, tierID, req.Qty); err != nil {
		s.logger.Error("complimentary: confirm capacity failed",
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.capacity_failed", "failed to confirm inventory capacity", r,
		))
		return
	}

	// Normalise recipients: nil → empty slice.
	recipients := req.Recipients
	if recipients == nil {
		recipients = []string{}
	}

	// Step 6: Insert the issuance record in 'pending' status.
	issuance, err := complQ.InsertComplimentaryIssuance(
		ctx, orgID, sessionID, tierID, req.Qty, recipients, req.BatchID, req.IssuedBy, req.Notes,
	)
	if err != nil {
		s.logger.Error("complimentary: insert issuance failed",
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.insert_failed", "failed to create complimentary issuance", r,
		))
		return
	}

	// Step 7: Insert one ticket per recipient (or qty anonymous tickets).
	tickets := make([]gen.ComplimentaryTicketRow, 0, req.Qty)
	effectiveQty := req.Qty
	for i := int32(0); i < effectiveQty; i++ {
		var holderEmail *string
		if i < int32(len(recipients)) && recipients[i] != "" { //nolint:gosec // recipients length bounded by request Qty (int32) above
			e := recipients[i]
			holderEmail = &e
		}
		t, err := complQ.InsertComplimentaryTicket(
			ctx, issuance.ID, sessionID, tierID, holderEmail,
		)
		if err != nil {
			s.logger.Error("complimentary: insert ticket failed",
				slog.String("issuance_id", issuance.ID.String()),
				slog.Int("index", int(i)),
				slog.String("error", err.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"complimentary.ticket_insert_failed", "failed to create complimentary ticket", r,
			))
			return
		}
		tickets = append(tickets, t)
	}

	// Step 8: Transition issuance status to 'issued'.
	issuance, err = complQ.UpdateComplimentaryIssuanceStatus(ctx, issuance.ID, "issued")
	if err != nil {
		s.logger.Error("complimentary: status update failed",
			slog.String("issuance_id", issuance.ID.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.status_update_failed", "failed to mark issuance as issued", r,
		))
		return
	}

	// Step 9: Commit.
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.commit_failed", "failed to commit issuance transaction", r,
		))
		return
	}

	s.logger.Info("complimentary: issued",
		slog.String("issuance_id", issuance.ID.String()),
		slog.String("org_id", orgID.String()),
		slog.String("batch_id", req.BatchID),
		slog.Int("qty", int(req.Qty)),
		slog.Int("tickets_issued", len(tickets)),
	)

	// Enqueue invitation delivery emails for each issued ticket (feature #149).
	// Best-effort: runs after commit so delivery failures cannot roll back the issuance.
	s.enqueueComplimentaryDeliveryJobs(ctx, tickets)

	writeJSON(w, http.StatusCreated, map[string]any{
		"issuance":          complimentaryIssuanceFromRow(issuance),
		"tickets":           complimentaryTicketsFromRows(tickets),
		"idempotent_replay": false,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/complimentary
// ─────────────────────────────────────────────────────────────────────────────

// handleListComplimentaryIssuances serves GET /v1/organizations/{org_id}/complimentary.
// Returns all complimentary issuances for the given org, newest first.
func (s *Server) handleListComplimentaryIssuances(w http.ResponseWriter, r *http.Request) {
	if s.complimentaryQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	orgIDStr := chi.URLParam(r, "org_id")
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"complimentary.invalid_org_id", "org_id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()
	rows, err := s.complimentaryQueries.ListComplimentaryIssuancesByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("complimentary: list failed",
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.list_failed", "failed to list complimentary issuances", r,
		))
		return
	}

	issuances := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		issuances = append(issuances, complimentaryIssuanceFromRow(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuances": issuances,
		"total":     len(issuances),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/complimentary/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleGetComplimentaryIssuance serves GET /v1/organizations/{org_id}/complimentary/{id}.
// Returns the issuance record plus the list of issued tickets.
func (s *Server) handleGetComplimentaryIssuance(w http.ResponseWriter, r *http.Request) {
	if s.complimentaryQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"complimentary.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()
	issuance, err := s.complimentaryQueries.GetComplimentaryIssuanceByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"complimentary.not_found", "complimentary issuance not found", r,
			))
			return
		}
		s.logger.Error("complimentary: get failed",
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.get_failed", "failed to retrieve complimentary issuance", r,
		))
		return
	}

	tickets, err := s.complimentaryQueries.ListTicketsByComplimentaryIssuance(ctx, id)
	if err != nil {
		// Best-effort: return issuance even if tickets can't be listed.
		s.logger.Warn("complimentary: list tickets failed",
			slog.String("issuance_id", id.String()),
			slog.String("error", err.Error()),
		)
		tickets = nil
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"issuance": complimentaryIssuanceFromRow(issuance),
		"tickets":  complimentaryTicketsFromRows(tickets),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Response helpers
// ─────────────────────────────────────────────────────────────────────────────

// complimentaryIssuanceFromRow converts a ComplimentaryIssuanceRow to a
// JSON-serialisable map.
func complimentaryIssuanceFromRow(r gen.ComplimentaryIssuanceRow) map[string]any {
	return map[string]any{
		"id":         r.ID,
		"org_id":     r.OrgID,
		"session_id": r.SessionID,
		"tier_id":    r.TierID,
		"qty":        r.Qty,
		"recipients": r.Recipients,
		"batch_id":   r.BatchID,
		"status":     r.Status,
		"issued_by":  r.IssuedBy,
		"notes":      r.Notes,
		"created_at": r.CreatedAt,
		"updated_at": r.UpdatedAt,
	}
}

// complimentaryTicketsFromRows converts a slice of ComplimentaryTicketRow to a
// JSON-serialisable slice of maps.
func complimentaryTicketsFromRows(rows []gen.ComplimentaryTicketRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		out = append(out, map[string]any{
			"id":                        t.ID,
			"complimentary_issuance_id": t.ComplimentaryIssuanceID,
			"session_id":                t.SessionID,
			"tier_id":                   t.TierID,
			"holder_email":              t.HolderEmail,
			"status":                    t.Status,
			"issued_at":                 t.IssuedAt,
			"created_at":                t.CreatedAt,
			"updated_at":                t.UpdatedAt,
		})
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/complimentary/{id}/revoke
// ─────────────────────────────────────────────────────────────────────────────

// handleRevokeComplimentaryIssuance serves POST /v1/complimentary/{id}/revoke.
//
// Workflow:
//  1. Parse and validate the issuance UUID from the URL.
//  2. Fetch the issuance — 404 when not found.
//  3. Guard: if already 'revoked' → 409.
//  4. Scan-status check: HasScannedTicketsForIssuance.
//     If any ticket has been scanned → transition to 'manual_review' → 409.
//  5. Begin transaction.
//  6. RevokeComplimentaryTickets — bulk UPDATE tickets to 'revoked'.
//  7. For each revoked ticket: revoke all associated barcodes (if barcodeQueries available).
//  8. For each revoked ticket: revoke 'qr' and 'pdf' credentials (if credentialQueries available).
//  9. RestoreSoldCapacity(session_id, tier_id, qty) — restore inventory.
//
// 10. UpdateComplimentaryIssuanceStatus → 'revoked'.
// 11. Commit. Emit structured audit log. Return 200 with the updated issuance.
func (s *Server) handleRevokeComplimentaryIssuance(w http.ResponseWriter, r *http.Request) {
	if s.complimentaryQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"complimentary.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()

	// ── Step 2: Fetch the issuance ───────────────────────────────────────────
	issuance, err := s.complimentaryQueries.GetComplimentaryIssuanceByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"complimentary.not_found", "complimentary issuance not found", r,
			))
			return
		}
		s.logger.Error("complimentary.revoke: get issuance failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.get_failed", "failed to retrieve complimentary issuance", r,
		))
		return
	}

	// ── Step 3: Guard against double-revoke ──────────────────────────────────
	if issuance.Status == "revoked" {
		writeJSON(w, http.StatusConflict, errorEnvelope(
			"complimentary.already_revoked", "complimentary issuance is already revoked", r,
		))
		return
	}

	// ── Step 4: Scan-status check ────────────────────────────────────────────
	hasScanned, err := s.complimentaryQueries.HasScannedTicketsForIssuance(ctx, id)
	if err != nil {
		s.logger.Error("complimentary.revoke: scan check failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.scan_check_failed", "failed to check scan status", r,
		))
		return
	}

	if hasScanned {
		// Some tickets have been scanned — require manual review.
		updated, updErr := s.complimentaryQueries.UpdateComplimentaryIssuanceStatus(ctx, id, "manual_review")
		if updErr != nil {
			s.logger.Error("complimentary.revoke: manual_review transition failed",
				slog.String("id", id.String()),
				slog.String("error", updErr.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"complimentary.status_update_failed", "failed to flag issuance for manual review", r,
			))
			return
		}
		s.logger.Warn("complimentary.revoke: blocked by scanned ticket — manual_review",
			slog.String("issuance_id", id.String()),
		)
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "complimentary.scanned_ticket_requires_manual_review",
			"message":  "one or more tickets have been scanned; issuance flagged for manual review",
			"status":   "manual_review",
			"issuance": complimentaryIssuanceFromRow(updated),
		})
		return
	}

	// ── Steps 5–11: Transactional revocation ────────────────────────────────
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	complQ := s.complimentaryQueries.WithTx(tx)

	// Step 6: Bulk-revoke all tickets for the issuance.
	revokedTickets, err := complQ.RevokeComplimentaryTickets(ctx, id)
	if err != nil {
		s.logger.Error("complimentary.revoke: ticket revocation failed",
			slog.String("issuance_id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.revoke_tickets_failed", "failed to revoke tickets", r,
		))
		return
	}

	// Step 7: Revoke all barcodes for each ticket (best-effort; needs barcodeQueries).
	if s.barcodeQueries != nil {
		barcodeQ := s.barcodeQueries.WithTx(tx)
		for _, t := range revokedTickets {
			barcodes, listErr := barcodeQ.ListBarcodesByTicketID(ctx, t.ID)
			if listErr != nil {
				// Non-fatal: log and continue.
				s.logger.Warn("complimentary.revoke: list barcodes failed",
					slog.String("ticket_id", t.ID.String()),
					slog.String("error", listErr.Error()),
				)
				continue
			}
			for _, b := range barcodes {
				if _, rErr := barcodeQ.RevokeBarcode(ctx, b.ID); rErr != nil {
					s.logger.Warn("complimentary.revoke: revoke barcode failed",
						slog.String("barcode_id", b.ID.String()),
						slog.String("error", rErr.Error()),
					)
				}
			}
		}
	}

	// Step 8: Revoke credentials for each ticket (best-effort; needs credentialQueries).
	if s.credentialQueries != nil {
		credQ := s.credentialQueries.WithTx(tx)
		for _, t := range revokedTickets {
			for _, credType := range []string{"qr", "pdf"} {
				if _, cErr := credQ.RevokeCredential(ctx, t.ID, credType); cErr != nil {
					// pgx.ErrNoRows is expected when no credential of that type exists.
					if !errors.Is(cErr, pgx.ErrNoRows) {
						s.logger.Warn("complimentary.revoke: revoke credential failed",
							slog.String("ticket_id", t.ID.String()),
							slog.String("type", credType),
							slog.String("error", cErr.Error()),
						)
					}
				}
			}
		}
	}

	// Step 9: Restore inventory — decrement capacity_sold by the issuance qty.
	if s.inventoryQueries != nil {
		invQ := s.inventoryQueries.WithTx(tx)
		if _, invErr := invQ.RestoreSoldCapacity(ctx, issuance.SessionID, issuance.TierID, issuance.Qty); invErr != nil {
			s.logger.Error("complimentary.revoke: restore capacity failed",
				slog.String("issuance_id", id.String()),
				slog.String("error", invErr.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"complimentary.restore_capacity_failed", "failed to restore inventory capacity", r,
			))
			return
		}
	}

	// Step 10: Mark the issuance as 'revoked'.
	issuance, err = complQ.UpdateComplimentaryIssuanceStatus(ctx, id, "revoked")
	if err != nil {
		s.logger.Error("complimentary.revoke: status update failed",
			slog.String("issuance_id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.status_update_failed", "failed to mark issuance as revoked", r,
		))
		return
	}

	// Step 11: Commit.
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"complimentary.commit_failed", "failed to commit revocation transaction", r,
		))
		return
	}

	// Audit log: structured event for observability and compliance.
	s.logger.Info("complimentary.revoked",
		slog.String("issuance_id", id.String()),
		slog.String("org_id", issuance.OrgID.String()),
		slog.String("session_id", issuance.SessionID.String()),
		slog.Int("qty", int(issuance.Qty)),
		slog.Int("tickets_revoked", len(revokedTickets)),
		slog.String("event", "complimentary.revoked"),
	)

	// Publish generic per-ticket v1.ticket.revoked events for the webhook
	// event catalog (feature S-1).  Best-effort: errors are logged inside
	// publishScannerEvent and never propagate to the HTTP caller.
	if len(revokedTickets) > 0 {
		ticketIDs := make([]string, 0, len(revokedTickets))
		for _, t := range revokedTickets {
			ticketIDs = append(ticketIDs, t.ID.String())
		}
		s.publishTicketRevokedV1Events(r.Context(), ticketIDs, id.String(), "complimentary_revocation")
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"issuance":        complimentaryIssuanceFromRow(issuance),
		"tickets_revoked": len(revokedTickets),
	})
}
