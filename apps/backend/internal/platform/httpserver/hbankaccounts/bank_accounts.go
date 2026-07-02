// bank_accounts.go implements the read + delete HTTP handlers for the
// organization bank-accounts CRUD surface (feature #255). The write surface
// (POST, PATCH) lives in bank_accounts_write.go; response shaping and
// validation helpers live in bank_accounts_types.go.
//
// Endpoints (all gated on `org.update` through Server.applyAuth in
// mount_iam.go — these rows are sensitive financial data and are not
// surfaced to actors with only `org.read`):
//
//	GET    /v1/organizations/{org_id}/bank-accounts       — list
//	POST   /v1/organizations/{org_id}/bank-accounts       — create
//	PATCH  /v1/organizations/{org_id}/bank-accounts/{id}  — update
//	DELETE /v1/organizations/{org_id}/bank-accounts/{id}  — soft-delete
//
// Invariants enforced here (documented in openapi.yaml):
//   - An org with any active bank account has exactly one primary account.
//     The first account created is promoted automatically; later promotions
//     atomically demote the previous primary in the same transaction.
//   - Demoting or deleting the primary while other active accounts exist is
//     rejected with 409 bank_account.primary_required.
//   - Every account carries at least one identifier: `iban` or the
//     `account_number` + `routing_number` pair (400
//     bank_account.identifier_required otherwise).
//   - Duplicate identifiers on the same org are rejected with 409
//     bank_account.duplicate_identifier.
//
// All endpoints are owner-gated through the WHERE org_id=$N clause in the
// underlying queries — a caller authenticated as org A cannot mutate or read
// bank accounts belonging to org B.
package hbankaccounts

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
// GET /v1/organizations/{org_id}/bank-accounts
// ─────────────────────────────────────────────────────────────────────────────

// HandleListBankAccounts serves GET /v1/organizations/{org_id}/bank-accounts.
func (h *Handler) HandleListBankAccounts(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
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

	// The spec documents 404 for an unknown or archived organization.
	if _, err := h.queries.GetOrganizationByID(ctx, orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("org.not_found", "organization not found", r))
			return
		}
		h.logger.Error("bank_account: org lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.list_failed", "failed to list bank accounts", r,
		))
		return
	}

	rows, err := h.queries.ListOrganizationBankAccountsByOrg(ctx, orgID)
	if err != nil {
		h.logger.Error("bank_account: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.list_failed", "failed to list bank accounts", r,
		))
		return
	}

	result := make([]BankAccountResponse, 0, len(rows))
	for _, b := range rows {
		result = append(result, BankAccountFromRow(b))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"bank_accounts": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/bank-accounts/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleDeleteBankAccount serves DELETE /v1/organizations/{org_id}/bank-accounts/{id}.
func (h *Handler) HandleDeleteBankAccount(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
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

	qtx := h.queries.WithTx(tx)

	existing, err := qtx.GetOrganizationBankAccountByID(ctx, id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("bank_account.not_found", "bank account not found", r))
			return
		}
		h.logger.Error("bank_account: pre-delete get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.delete_failed", "failed to delete bank account", r,
		))
		return
	}

	// Deleting the primary account while other active accounts remain would
	// leave the org without a primary — the caller must promote a
	// replacement via PATCH first.
	if existing.IsPrimary {
		others, err := qtx.CountOtherActiveOrganizationBankAccounts(ctx, orgID, id)
		if err != nil {
			h.logger.Error("bank_account: sibling count failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"bank_account.delete_failed", "failed to delete bank account", r,
			))
			return
		}
		if others > 0 {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"bank_account.primary_required",
				"cannot delete the primary bank account while other active accounts exist; promote a replacement primary first",
				r,
			))
			return
		}
	}

	deleted, err := qtx.SoftDeleteOrganizationBankAccount(ctx, id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("bank_account.not_found", "bank account not found", r))
			return
		}
		h.logger.Error("bank_account: delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.delete_failed", "failed to delete bank account", r,
		))
		return
	}

	if err := h.writeBankAccountAuditTx(ctx, tx, r, "v1.bank_account.delete", deleted.ID.String(), map[string]any{
		"org_id":     orgID.String(),
		"currency":   deleted.Currency,
		"country":    deleted.Country,
		"is_primary": deleted.IsPrimary,
	}); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.audit_failed", "failed to write audit event", r,
		))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"bank_account": BankAccountFromRow(deleted),
		"deleted":      true,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Audit helper (transactional — shared by POST / PATCH / DELETE)
// ─────────────────────────────────────────────────────────────────────────────

// writeBankAccountAuditTx emits an audit event inside the mutation's
// transaction so the audit trail and the row change commit atomically.
// Returns the writer error (already logged) so callers can fail the request.
func (h *Handler) writeBankAccountAuditTx(ctx context.Context, tx pgx.Tx, r *http.Request, action, resourceID string, metadata map[string]any) error {
	if h.audit == nil {
		return nil
	}
	actor, _ := auth.ActorFromContext(ctx)
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "organization_bank_account",
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
		h.logger.Error("bank_account: audit write failed", slog.String("error", err.Error()))
		return err
	}
	return nil
}
