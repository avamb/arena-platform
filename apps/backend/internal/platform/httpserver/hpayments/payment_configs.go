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
//   - The `secrets` map is accepted in POST/PATCH bodies but NEVER
//     returned (PaymentConfigFromRow drops it).
//   - Status derivation runs after every write: when every required
//     secret field for the chosen provider is non-empty, status flips
//     to "configured"; otherwise "missing_required_fields".
//
// All endpoints are owner-gated through the WHERE org_id=$N clause in
// the underlying queries — a caller authenticated as org A cannot
// mutate or read configs belonging to org B.
package hpayments

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/payment-configs
// ─────────────────────────────────────────────────────────────────────────────

// HandleListPaymentConfigs serves GET /v1/organizations/{org_id}/payment-configs.
func (h *Handler) HandleListPaymentConfigs(w http.ResponseWriter, r *http.Request) {
	if h.paymentConfigQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	rows, err := h.paymentConfigQueries.ListPaymentProviderConfigsByOrg(ctx, orgID)
	if err != nil {
		h.logger.Error("payment_config: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"payment_config.list_failed", "failed to list payment configs", r,
		))
		return
	}

	result := make([]PaymentConfigResponse, 0, len(rows))
	for _, p := range rows {
		result = append(result, PaymentConfigFromRow(p))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"payment_configs": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/payment-configs/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetPaymentConfig serves GET /v1/organizations/{org_id}/payment-configs/{id}.
func (h *Handler) HandleGetPaymentConfig(w http.ResponseWriter, r *http.Request) {
	if h.paymentConfigQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	row, err := h.paymentConfigQueries.GetPaymentProviderConfigByID(ctx, id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("payment_config.not_found", "payment config not found", r))
			return
		}
		h.logger.Error("payment_config: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"payment_config.get_failed", "failed to get payment config", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"payment_config": PaymentConfigFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/payment-configs/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleDeletePaymentConfig serves DELETE /v1/organizations/{org_id}/payment-configs/{id}.
func (h *Handler) HandleDeletePaymentConfig(w http.ResponseWriter, r *http.Request) {
	if h.paymentConfigQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.paymentConfigQueries.WithTx(tx)
	deleted, err := qtx.SoftDeletePaymentProviderConfig(ctx, id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("payment_config.not_found", "payment config not found", r))
			return
		}
		h.logger.Error("payment_config: delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"payment_config.delete_failed", "failed to delete payment config", r,
		))
		return
	}

	if h.audit != nil {
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
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"org_id":   orgID.String(),
				"provider": deleted.Provider,
				"mode":     deleted.Mode,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
			h.logger.Error("payment_config: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"payment_config.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"payment_config.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"payment_config": PaymentConfigFromRow(deleted),
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
func (h *Handler) writePaymentConfigAudit(ctx context.Context, r *http.Request, action, resourceID string, metadata map[string]any) {
	if h.audit == nil {
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
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.Write(ctx, ev); err != nil {
		h.logger.Warn("payment_config: best-effort audit failed", slog.String("error", err.Error()))
	}
}
