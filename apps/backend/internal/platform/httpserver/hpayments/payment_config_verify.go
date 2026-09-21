// payment_config_verify.go — ask the provider whether the stored credential
// actually works, and remember the answer.
//
// `status` on a config means "the required fields are non-empty". It has
// never meant the key is usable, and on 2026-09-21 that gap cost a live
// organization a day of sales: the admin showed the Stripe config as
// configured throughout while the api_key held a string that was not a key,
// and the only feedback loop that existed ran through a buyer hitting a 503.
//
// So the answer is fetched from the provider and stored on the row, where an
// operator can see it. Two rules shape everything here:
//
//   - A verification NEVER blocks the save. The configuration is the
//     operator's to record; our opinion of it is an annotation. A save that
//     failed because Stripe was briefly unreachable would be a worse product
//     than one that saves and shows "unverified".
//   - "Could not reach the provider" is not "the key is bad". Only a refusal
//     is recorded as a failure; an unreachable provider leaves the row
//     unverified, because a red badge we produced from our own connectivity
//     teaches operators to ignore the badge.
package hpayments

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripe"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// The three values payment_provider_configs.verification_status may hold.
// Mirrors the CHECK constraint in migration 0107.
const (
	VerificationUnverified = "unverified"
	VerificationOK         = "ok"
	VerificationFailed     = "failed"
)

// verifyTimeout bounds a single provider round trip. Generous enough for a
// slow TLS handshake, short enough that a save never feels stuck behind it —
// and the save has already committed by the time this runs.
const verifyTimeout = 10 * time.Second

// verificationErrorLimit caps what we store of a provider's refusal. Their
// messages are short; a runaway body must not become an unbounded column.
const verificationErrorLimit = 500

// normalizeSecretPatch trims pasted whitespace from every value in a secrets
// patch, leaving keys and empty values (the delete marker) alone.
func normalizeSecretPatch(patch map[string]string) map[string]string {
	if patch == nil {
		return nil
	}
	out := make(map[string]string, len(patch))
	for k, v := range patch {
		out[k] = NormalizeSecretValue(v)
	}
	return out
}

// CredentialVerifierFor builds a provider client for this row's stored
// credential, or reports that the provider cannot be checked.
//
// A provider we have no verifier for is NOT a failure: it simply cannot be
// asked, and its config stays "unverified" forever, which is the honest
// state. Only Stripe is wired today.
func (h *Handler) CredentialVerifierFor(row gen.PaymentProviderConfigRow) (payments.CredentialVerifier, bool) {
	if row.Provider != "stripe" {
		return nil, false
	}
	apiKey := storedSecretValue(row.Secrets, "api_key")
	if apiKey == "" {
		return nil, false
	}
	return stripe.New(stripe.Config{SecretKey: apiKey, BaseURL: h.stripeBaseURL}), true
}

// verifyAndRecord asks the provider about the row's credential and writes the
// verdict back, returning the row as it now stands.
//
// Never returns an error to the caller's HTTP path: a verification is an
// annotation on a write that has already succeeded, and the worst outcome of
// it failing must be a config that reads "unverified".
func (h *Handler) verifyAndRecord(ctx context.Context, row gen.PaymentProviderConfigRow) gen.PaymentProviderConfigRow {
	verifier, canVerify := h.CredentialVerifierFor(row)
	if !canVerify {
		return row
	}

	// Detached from the request context on purpose: a buyer's browser
	// hanging up must not leave the row claiming a verdict we never got.
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), verifyTimeout)
	defer cancel()

	status, detail := classifyVerification(verifier.VerifyCredentials(callCtx))
	if status == VerificationUnverified && row.VerificationStatus == VerificationUnverified {
		// Nothing learned and nothing to unlearn — skip the write.
		return row
	}

	updated, err := h.paymentConfigQueries.SetPaymentProviderConfigVerification(
		ctx, row.ID, row.OrgID, status, detail,
	)
	if err != nil {
		h.logger.Warn("payment_config: could not record verification verdict",
			slog.String("payment_config_id", row.ID.String()),
			slog.String("verification_status", status),
			slog.String("error", err.Error()),
		)
		return row
	}
	return updated
}

// classifyVerification turns a verifier's answer into the stored verdict.
//
// A refusal is a fact about the key and is recorded. Anything else — a
// timeout, DNS, a 5xx, a request of ours the provider disliked — is a fact
// about the trip and leaves the row unverified with the reason attached, so
// an operator can tell "we could not check" from "it was rejected".
func classifyVerification(err error) (status, detail string) {
	switch {
	case err == nil:
		return VerificationOK, ""
	case errors.Is(err, payments.ErrCredentialRefused):
		return VerificationFailed, truncate(err.Error(), verificationErrorLimit)
	default:
		return VerificationUnverified, truncate(err.Error(), verificationErrorLimit)
	}
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// HandleVerifyPaymentConfig backs
// POST /v1/organizations/{org_id}/payment-configs/{id}/verify — the operator
// pressing "check again" without re-entering the credential.
//
// Answers 200 with the config in its new state whatever the verdict was: the
// verdict is the payload, not the HTTP status. A caller wanting to know
// whether the key works reads verification_status; an HTTP error here would
// mean the CHECK could not be performed, which is a different thing and is
// what the unverified state carries.
func (h *Handler) HandleVerifyPaymentConfig(w http.ResponseWriter, r *http.Request) {
	if h.paymentConfigQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, orgID) {
		return
	}

	ctx := r.Context()
	row, getErr := h.paymentConfigQueries.GetPaymentProviderConfigByID(ctx, id, orgID)
	if err := getErr; err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"payment_config.not_found", "payment config not found", r,
			))
			return
		}
		h.logger.Error("payment_config: verify lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"payment_config.verify_failed", "failed to load payment config", r,
		))
		return
	}

	verified := h.verifyAndRecord(ctx, row)

	h.writePaymentConfigAudit(ctx, r, "v1.payment_config.verify", verified.ID.String(), map[string]any{
		"org_id":              orgID.String(),
		"provider":            verified.Provider,
		"mode":                verified.Mode,
		"verification_status": verified.VerificationStatus,
	})

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"payment_config": PaymentConfigFromRow(verified),
	})
}
