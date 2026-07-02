// bank_accounts_write.go houses the POST and PATCH handlers for the
// organization bank-accounts CRUD surface (feature #255). Splitting the
// write surface out of bank_accounts.go mirrors the hpayments layout and
// keeps each file within the httpserver size conventions.
package hbankaccounts

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// readBankAccountBody slurps and strictly decodes the request body shared by
// POST and PATCH. On failure it writes the 400 error envelope and returns
// ok=false.
func readBankAccountBody(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("bank_account.invalid_body", "cannot read request body: "+err.Error(), r))
		return nil, false
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("bank_account.empty_body", "request body is required", r))
		return nil, false
	}
	fields, code, message := decodeBankAccountBody(body)
	if code != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(code, message, r))
		return nil, false
	}
	return fields, true
}

// invalidField writes the 400 envelope for a field whose JSON type does not
// match the schema.
func invalidField(w http.ResponseWriter, r *http.Request, field, want string) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
		"bank_account.invalid_field", "field "+quoteKey(field)+" must be "+want, r,
		map[string]any{"field": field},
	))
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/bank-accounts
// ─────────────────────────────────────────────────────────────────────────────

// HandleCreateBankAccount serves POST /v1/organizations/{org_id}/bank-accounts.
func (h *Handler) HandleCreateBankAccount(w http.ResponseWriter, r *http.Request) {
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
	fields, ok := readBankAccountBody(w, r)
	if !ok {
		return
	}

	// Required scalar fields.
	holderName, _, ok := stringField(fields, "holder_name")
	if !ok {
		invalidField(w, r, "holder_name", "a string")
		return
	}
	if holderName == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bank_account.invalid_holder_name", "holder_name is required and must be non-empty", r,
			map[string]any{"field": "holder_name"},
		))
		return
	}
	currency, _, ok := stringField(fields, "currency")
	if !ok || currency == nil || !isValidCurrency(*currency) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bank_account.invalid_currency", "currency must be an ISO 4217 alpha-3 code matching ^[A-Z]{3}$", r,
			map[string]any{"field": "currency"},
		))
		return
	}
	country, _, ok := stringField(fields, "country")
	if !ok || country == nil || !isValidCountry(*country) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bank_account.invalid_country", "country must be an ISO 3166-1 alpha-2 code matching ^[A-Z]{2}$", r,
			map[string]any{"field": "country"},
		))
		return
	}

	// Optional identifier / display fields.
	optional := map[string]*string{}
	for _, key := range []string{"bank_name", "iban", "bic", "account_number", "routing_number"} {
		v, _, ok := stringField(fields, key)
		if !ok {
			invalidField(w, r, key, "a string")
			return
		}
		optional[key] = v
	}
	isPrimary, _, ok := boolField(fields, "is_primary")
	if !ok {
		invalidField(w, r, "is_primary", "a boolean")
		return
	}

	if !hasBankIdentifier(optional["iban"], optional["account_number"], optional["routing_number"]) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"bank_account.identifier_required",
			"at least one of iban or account_number+routing_number must be supplied",
			r,
		))
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

	// The spec documents 404 for an unknown or archived organization.
	if _, err := qtx.GetOrganizationByID(ctx, orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("org.not_found", "organization not found", r))
			return
		}
		h.logger.Error("bank_account: org lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.create_failed", "failed to create bank account", r,
		))
		return
	}

	siblings, err := qtx.ListOrganizationBankAccountsByOrg(ctx, orgID)
	if err != nil {
		h.logger.Error("bank_account: sibling list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.create_failed", "failed to create bank account", r,
		))
		return
	}
	for _, sib := range siblings {
		if duplicateIdentifier(sib, optional["iban"], optional["account_number"], optional["routing_number"]) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"bank_account.duplicate_identifier",
				"a bank account with the same identifier already exists in this organization",
				r,
			))
			return
		}
	}

	// Primary invariant: an org with any active account has exactly one
	// primary, so the first account is always promoted; an explicit
	// is_primary=true demotes the previous primary atomically.
	if len(siblings) == 0 {
		isPrimary = true
	}
	if isPrimary {
		if err := qtx.DemoteOrganizationBankAccountDefault(ctx, orgID, uuid.Nil); err != nil {
			h.logger.Error("bank_account: demote failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"bank_account.create_failed", "failed to create bank account", r,
			))
			return
		}
	}

	row, err := qtx.InsertOrganizationBankAccount(
		ctx, orgID, optional["bank_name"], *holderName,
		optional["iban"], optional["bic"], optional["account_number"], optional["routing_number"],
		*currency, *country, isPrimary,
	)
	if err != nil {
		h.logger.Error("bank_account: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.create_failed", "failed to create bank account", r,
		))
		return
	}

	if err := h.writeBankAccountAuditTx(ctx, tx, r, "v1.bank_account.create", row.ID.String(), map[string]any{
		"org_id":     orgID.String(),
		"currency":   row.Currency,
		"country":    row.Country,
		"is_primary": row.IsPrimary,
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

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"bank_account": BankAccountFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/bank-accounts/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleUpdateBankAccount serves PATCH /v1/organizations/{org_id}/bank-accounts/{id}.
func (h *Handler) HandleUpdateBankAccount(w http.ResponseWriter, r *http.Request) {
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
	fields, ok := readBankAccountBody(w, r)
	if !ok {
		return
	}

	// Pre-validate present fields before opening a transaction.
	holderName, holderPresent, ok := stringField(fields, "holder_name")
	if !ok {
		invalidField(w, r, "holder_name", "a string")
		return
	}
	if holderPresent && holderName == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bank_account.invalid_holder_name", "holder_name must be non-empty", r,
			map[string]any{"field": "holder_name"},
		))
		return
	}
	currency, currencyPresent, ok := stringField(fields, "currency")
	if !ok || (currencyPresent && (currency == nil || !isValidCurrency(*currency))) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bank_account.invalid_currency", "currency must be an ISO 4217 alpha-3 code matching ^[A-Z]{3}$", r,
			map[string]any{"field": "currency"},
		))
		return
	}
	country, countryPresent, ok := stringField(fields, "country")
	if !ok || (countryPresent && (country == nil || !isValidCountry(*country))) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"bank_account.invalid_country", "country must be an ISO 3166-1 alpha-2 code matching ^[A-Z]{2}$", r,
			map[string]any{"field": "country"},
		))
		return
	}
	type patchValue struct {
		value   *string
		present bool
	}
	clearable := map[string]patchValue{}
	for _, key := range []string{"bank_name", "iban", "bic", "account_number", "routing_number"} {
		v, present, ok := stringField(fields, key)
		if !ok {
			invalidField(w, r, key, "a string or null")
			return
		}
		clearable[key] = patchValue{value: v, present: present}
	}
	isPrimary, isPrimaryPresent, ok := boolField(fields, "is_primary")
	if !ok {
		invalidField(w, r, "is_primary", "a boolean")
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
		h.logger.Error("bank_account: pre-update get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.update_failed", "failed to update bank account", r,
		))
		return
	}

	// Merge: omitted fields keep the existing value; present-null / empty
	// values clear the nullable fields.
	final := struct {
		holderName, currency, country               string
		bankName, iban, bic, accountNum, routingNum *string
		isPrimary                                   bool
	}{
		holderName: existing.HolderName,
		currency:   existing.Currency,
		country:    existing.Country,
		bankName:   existing.BankName,
		iban:       existing.Iban,
		bic:        existing.Bic,
		accountNum: existing.AccountNumber,
		routingNum: existing.RoutingNumber,
		isPrimary:  existing.IsPrimary,
	}
	if holderPresent {
		final.holderName = *holderName
	}
	if currencyPresent {
		final.currency = *currency
	}
	if countryPresent {
		final.country = *country
	}
	if pv := clearable["bank_name"]; pv.present {
		final.bankName = pv.value
	}
	if pv := clearable["iban"]; pv.present {
		final.iban = pv.value
	}
	if pv := clearable["bic"]; pv.present {
		final.bic = pv.value
	}
	if pv := clearable["account_number"]; pv.present {
		final.accountNum = pv.value
	}
	if pv := clearable["routing_number"]; pv.present {
		final.routingNum = pv.value
	}

	// is_primary transitions. Demoting the primary directly is rejected —
	// the org would be left with active accounts but no primary.
	if isPrimaryPresent {
		if !isPrimary && existing.IsPrimary {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"bank_account.primary_required",
				"cannot demote the primary bank account; promote another account to primary instead",
				r,
			))
			return
		}
		final.isPrimary = isPrimary
	}

	// The merged row must still carry an identifier.
	if !hasBankIdentifier(final.iban, final.accountNum, final.routingNum) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"bank_account.identifier_required",
			"the update would leave the account without iban or account_number+routing_number",
			r,
		))
		return
	}

	// Duplicate-identifier check against the org's other active rows.
	siblings, err := qtx.ListOrganizationBankAccountsByOrg(ctx, orgID)
	if err != nil {
		h.logger.Error("bank_account: sibling list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.update_failed", "failed to update bank account", r,
		))
		return
	}
	for _, sib := range siblings {
		if sib.ID == id {
			continue
		}
		if duplicateIdentifier(sib, final.iban, final.accountNum, final.routingNum) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"bank_account.duplicate_identifier",
				"a bank account with the same identifier already exists in this organization",
				r,
			))
			return
		}
	}

	// Promotion atomically demotes the previous primary in the same tx.
	if final.isPrimary && !existing.IsPrimary {
		if err := qtx.DemoteOrganizationBankAccountDefault(ctx, orgID, id); err != nil {
			h.logger.Error("bank_account: demote failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"bank_account.update_failed", "failed to update bank account", r,
			))
			return
		}
	}

	row, err := qtx.UpdateOrganizationBankAccount(
		ctx, id, orgID, final.bankName, final.holderName,
		final.iban, final.bic, final.accountNum, final.routingNum,
		final.currency, final.country, final.isPrimary,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("bank_account.not_found", "bank account not found", r))
			return
		}
		h.logger.Error("bank_account: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bank_account.update_failed", "failed to update bank account", r,
		))
		return
	}

	if err := h.writeBankAccountAuditTx(ctx, tx, r, "v1.bank_account.update", row.ID.String(), map[string]any{
		"org_id":         orgID.String(),
		"currency":       row.Currency,
		"country":        row.Country,
		"is_primary":     row.IsPrimary,
		"is_primary_set": isPrimaryPresent,
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
		"bank_account": BankAccountFromRow(row),
	})
}
