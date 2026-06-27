// payment_configs.go implements the read + delete HTTP handlers for
// payment-provider-configs (feature #237). The write surface (POST,
// PATCH) lives in payment_configs_write.go; response shaping and
// validation helpers live in payment_configs_types.go. Splitting the
// surface keeps each file under the per-file size budget enforced by
// the httpserver_file_size_175_test gate (feature #175).
//
// A payment_provider_config row carries the credentials and public
// connection metadata for one (org, provider, mode) tuple. It is the
// resource an org admin manages from the SuperAdmin UI to wire Stripe
// or AllPay into the platform — separate from sales_channels, which
// is the commercial unit a checkout binds to.
//
// Endpoints (all gated through Server.applyAuth in mount_iam.go):
//
//	GET    /v1/organizations/{org_id}/payment-configs        — list (payment_config.read)
//	GET    /v1/organizations/{org_id}/payment-configs/{id}   — get one (payment_config.read)
//	POST   /v1/organizations/{org_id}/payment-configs        — create (payment_config.write)
//	PATCH  /v1/organizations/{org_id}/payment-configs/{id}   — update (payment_config.write)
//	DELETE /v1/organizations/{org_id}/payment-configs/{id}   — soft-delete (payment_config.write)
//
// Secret handling:
//   * The `secrets` map is accepted in POST/PATCH bodies but NEVER
//     returned (paymentConfigFromRow drops it).
//   * Status derivation runs after every write: when every required
//     secret field for the chosen provider is non-empty, status flips
//     to "configured"; otherwise "missing_required_fields".
//
// All endpoints are owner-gated through the WHERE org_id=$N clause in
// the underlying queries — a caller authenticated as org A cannot
// mutate or read configs belonging to org B.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/payment-configs
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleListPaymentConfigs(w http.ResponseWriter, r *http.Request) {
	if s.paymentConfigQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}

	rows, err := s.paymentConfigQueries.ListPaymentProviderConfigsByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("payment_config: list failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"payment_config.list_failed", "failed to list payment configs", r,
		))
		return
	}

	result := make([]paymentConfigResponse, 0, len(rows))
	for _, p := range rows {
		result = append(result, paymentConfigFromRow(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"payment_configs": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/payment-configs/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleGetPaymentConfig(w http.ResponseWriter, r *http.Request) {
	if s.paymentConfigQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	id, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	row, err := s.paymentConfigQueries.GetPaymentProviderConfigByID(ctx, id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("payment_config.not_found", "payment config not found", r))
			return
		}
		s.logger.Error("payment_config: get failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"payment_config.get_failed", "failed to get payment config", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"payment_config": paymentConfigFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/payment-configs/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleDeletePaymentConfig(w http.ResponseWriter, r *http.Request) {
	if s.paymentConfigQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	id, ok := uuidPathParam(w, r, "id")
	if !ok {
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

	qtx := s.paymentConfigQueries.WithTx(tx)
	deleted, err := qtx.SoftDeletePaymentProviderConfig(ctx, id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("payment_config.not_found", "payment config not found", r))
			return
		}
		s.logger.Error("payment_config: delete failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"payment_config.delete_failed", "failed to delete payment config", r,
		))
		return
	}

	if s.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		ev := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.payment_config.delete",
			ResourceType: "payment_provider_config",
			ResourceID:   deleted.ID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           extractClientIP(r),
			Metadata: map[string]any{
				"org_id":   orgID.String(),
				"provider": deleted.Provider,
				"mode":     deleted.Mode,
			},
		}
		if err := s.audit.WriteTx(ctx, tx, ev); err != nil {
			s.logger.Error("payment_config: audit write failed", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"payment_config.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"payment_config.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"payment_config": paymentConfigFromRow(deleted),
		"deleted":        true,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Audit helper (non-transactional best-effort path for POST/PATCH)
// ─────────────────────────────────────────────────────────────────────────────

// writePaymentConfigAudit emits an audit event outside any transaction.
// Failures are logged but do not fail the surrounding HTTP response —
// the row is already committed at that point. Used by the write
// handlers in payment_configs_write.go.
func (s *Server) writePaymentConfigAudit(ctx context.Context, r *http.Request, action, resourceID string, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	actor, _ := auth.ActorFromContext(ctx)
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "payment_provider_config",
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           extractClientIP(r),
		Metadata:     metadata,
	}
	if err := s.audit.Write(ctx, ev); err != nil {
		s.logger.Warn("payment_config: best-effort audit failed", slog.String("error", err.Error()))
	}
}
