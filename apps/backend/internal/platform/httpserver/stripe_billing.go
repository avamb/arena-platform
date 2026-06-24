// stripe_billing.go — Stripe Billing adapter HTTP handlers (feature #162).
//
// Pushes platform SaaS invoices (from the billing ledger) to the platform's
// Estonia Stripe account and syncs payment status back via webhooks.
//
// Endpoints:
//
//	POST /v1/billing/stripe/push-invoice/{id}  — push local invoice to Stripe (billing.admin)
//	POST /v1/billing/stripe/webhook            — Stripe Billing webhook receiver (public)
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripebilling"
	"github.com/abhteam/arena_new/apps/backend/internal/payments"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// stripeBillingHelper — interface for the Stripe Billing adapter
// ─────────────────────────────────────────────────────────────────────────────

// stripeBillingHelper defines the Stripe Billing operations used by the HTTP
// handlers. The interface decouples the handlers from the concrete adapter so
// tests can inject a mock without making real Stripe API calls.
type stripeBillingHelper interface {
	// CreateOrUpdateCustomer creates a Stripe Customer on the platform account.
	// email and name may be empty strings. idempotencyKey should be
	// "cust-<orgID>" to prevent duplicate creation on retries.
	CreateOrUpdateCustomer(ctx context.Context, email, name, idempotencyKey string) (string, error)

	// CreateInvoiceItem adds a pending invoice item to the given Stripe customer.
	// idempotencyKey should be "item-<lineID>" to prevent duplicate line items.
	CreateInvoiceItem(ctx context.Context, stripeCustomerID, description string, amountMinor int64, currency, idempotencyKey string) (string, error)

	// CreateAndFinalizeInvoice creates and sends a Stripe Invoice collecting
	// all pending items for the customer. idempotencyKey should be
	// "inv-<localInvoiceID>" to prevent duplicate Stripe invoices on retries.
	CreateAndFinalizeInvoice(ctx context.Context, stripeCustomerID, description string, metadata map[string]string, idempotencyKey string) (string, error)

	// HandleBillingWebhook verifies the Stripe-Signature and parses the event.
	HandleBillingWebhook(body []byte, sigHeader, secret string) (*stripebilling.BillingWebhookEvent, error)
}

// compile-time interface guard: *stripebilling.Adapter must implement stripeBillingHelper.
var _ stripeBillingHelper = (*stripebilling.Adapter)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/billing/stripe/push-invoice/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handlePushInvoiceToStripe pushes a locally issued platform invoice to Stripe
// Billing so Stripe can collect the payment from the organizer.
//
// Flow:
//  1. Fetch the local invoice; must be in "issued" state.
//  2. Fetch all invoice lines.
//  3. Ensure a Stripe Customer exists for the org (upsert stripe_customers).
//  4. Create Stripe InvoiceItems for each line.
//  5. Create + finalize the Stripe Invoice.
//  6. Store stripe_invoice_id on the local invoice row.
//
// Permission required: billing.admin
func (s *Server) handlePushInvoiceToStripe(w http.ResponseWriter, r *http.Request) {
	if s.stripeBilling == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("stripe_billing.unavailable", "Stripe Billing adapter not configured", r))
		return
	}
	if s.billingQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("billing.unavailable", "billing ledger not configured", r))
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.bad_request", "id must be a valid UUID", r))
		return
	}

	ctx := r.Context()

	// ── Step 1: Fetch invoice; must be issued ─────────────────────────────────
	invoice, err := s.billingQueries.GetInvoiceByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("billing.not_found", "invoice not found", r))
			return
		}
		s.logger.Error("stripe_billing: get invoice failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "failed to fetch invoice", r))
		return
	}

	if invoice.State != "issued" {
		writeJSON(w, http.StatusConflict, errorEnvelope("stripe_billing.wrong_state",
			fmt.Sprintf("invoice must be in 'issued' state to push to Stripe; current state: %s", invoice.State), r))
		return
	}

	// ── Step 2: Fetch invoice lines ───────────────────────────────────────────
	lines, err := s.billingQueries.ListInvoiceLines(ctx, id)
	if err != nil {
		s.logger.Error("stripe_billing: list invoice lines failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "failed to fetch invoice lines", r))
		return
	}
	if len(lines) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope("stripe_billing.no_lines", "invoice has no lines; cannot push empty invoice to Stripe", r))
		return
	}

	// ── Step 3: Ensure Stripe Customer exists for org ─────────────────────────
	orgID := invoice.OrgID
	var stripeCustomerID string

	existingCustomer, lookupErr := s.billingQueries.GetStripeCustomerByOrgID(ctx, orgID)
	if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
		s.logger.Error("stripe_billing: get stripe customer failed", slog.Any("error", lookupErr))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "failed to look up Stripe customer", r))
		return
	}

	if errors.Is(lookupErr, pgx.ErrNoRows) {
		// Create a new Stripe Customer for this org.
		idempotencyKey := "cust-" + orgID.String()
		newCustomerID, createErr := s.stripeBilling.CreateOrUpdateCustomer(ctx, "", "", idempotencyKey)
		if createErr != nil {
			s.logger.Error("stripe_billing: create Stripe customer failed",
				slog.String("org_id", orgID.String()),
				slog.Any("error", createErr),
			)
			writeJSON(w, http.StatusBadGateway, errorEnvelope("stripe_billing.customer_error", "failed to create Stripe customer", r))
			return
		}
		stripeCustomerID = newCustomerID
		// Persist the mapping.
		if _, persistErr := s.billingQueries.UpsertStripeCustomer(ctx, orgID, stripeCustomerID, nil, nil); persistErr != nil {
			s.logger.Error("stripe_billing: upsert stripe customer mapping failed",
				slog.String("org_id", orgID.String()),
				slog.Any("error", persistErr),
			)
			// Non-fatal: continue with the Stripe customer ID in memory.
		}
	} else {
		stripeCustomerID = existingCustomer.StripeCustomerID
	}

	// ── Step 4: Create Stripe InvoiceItems for each line ─────────────────────
	for _, line := range lines {
		itemIdempotencyKey := "item-" + line.ID.String()
		if _, itemErr := s.stripeBilling.CreateInvoiceItem(
			ctx,
			stripeCustomerID,
			line.Description,
			line.TotalAmountMinor,
			line.Currency,
			itemIdempotencyKey,
		); itemErr != nil {
			s.logger.Error("stripe_billing: create invoice item failed",
				slog.String("invoice_id", id.String()),
				slog.String("line_id", line.ID.String()),
				slog.Any("error", itemErr),
			)
			writeJSON(w, http.StatusBadGateway, errorEnvelope("stripe_billing.item_error", "failed to create Stripe invoice item", r))
			return
		}
	}

	// ── Step 5: Create + finalize Stripe Invoice ──────────────────────────────
	description := fmt.Sprintf("Platform service fee for billing period %s", invoice.BillingPeriod)
	metadata := map[string]string{
		"arena_invoice_id": id.String(),
		"org_id":           orgID.String(),
		"billing_period":   invoice.BillingPeriod,
	}
	invIdempotencyKey := "inv-" + id.String()

	stripeInvoiceID, invErr := s.stripeBilling.CreateAndFinalizeInvoice(
		ctx,
		stripeCustomerID,
		description,
		metadata,
		invIdempotencyKey,
	)
	if invErr != nil {
		s.logger.Error("stripe_billing: create and finalize Stripe invoice failed",
			slog.String("invoice_id", id.String()),
			slog.Any("error", invErr),
		)
		writeJSON(w, http.StatusBadGateway, errorEnvelope("stripe_billing.invoice_error", "failed to create Stripe invoice", r))
		return
	}

	// ── Step 6: Persist stripe_invoice_id ────────────────────────────────────
	updated, persistErr := s.billingQueries.UpdateInvoiceStripeID(ctx, id, stripeInvoiceID)
	if persistErr != nil {
		s.logger.Error("stripe_billing: store stripe_invoice_id failed",
			slog.String("invoice_id", id.String()),
			slog.String("stripe_invoice_id", stripeInvoiceID),
			slog.Any("error", persistErr),
		)
		// Return success anyway — the Stripe invoice was created; idempotency
		// keys protect from duplicate charges on retry.
		writeJSON(w, http.StatusOK, map[string]any{
			"invoice_id":        id.String(),
			"stripe_invoice_id": stripeInvoiceID,
			"warning":           "stripe_invoice_id could not be persisted locally; retry to reconcile",
		})
		return
	}

	s.logger.Info("stripe_billing: invoice pushed to Stripe",
		slog.String("invoice_id", id.String()),
		slog.String("stripe_invoice_id", stripeInvoiceID),
		slog.String("org_id", orgID.String()),
		slog.String("billing_period", invoice.BillingPeriod),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"invoice_id":        updated.ID.String(),
		"stripe_invoice_id": stripeInvoiceID,
		"state":             updated.State,
		"billing_period":    updated.BillingPeriod,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/billing/stripe/webhook
// ─────────────────────────────────────────────────────────────────────────────

// handleStripeBillingWebhook receives Stripe Billing webhook events and syncs
// payment status to the local invoice ledger.
//
// Handled events:
//   - invoice.paid             → transition local invoice from "issued" to "paid"
//   - invoice.payment_failed   → log warning; local invoice stays "issued"
//
// This endpoint is public (no JWT auth) because Stripe cannot send Bearer tokens.
// Security is provided by the Stripe-Signature HMAC verification inside
// HandleBillingWebhook.
func (s *Server) handleStripeBillingWebhook(w http.ResponseWriter, r *http.Request) {
	if s.stripeBilling == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("stripe_billing.unavailable", "Stripe Billing adapter not configured", r))
		return
	}
	if s.billingQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("billing.unavailable", "billing ledger not configured", r))
		return
	}

	// Read the raw body for signature verification (must not be parsed first).
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("stripe_billing.read_error", "cannot read request body", r))
		return
	}

	sigHeader := r.Header.Get("Stripe-Signature")
	if sigHeader == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("stripe_billing.missing_signature", "Stripe-Signature header is required", r))
		return
	}

	event, err := s.stripeBilling.HandleBillingWebhook(body, sigHeader, "")
	if err != nil {
		if errors.Is(err, payments.ErrInvalidWebhookSignature) {
			writeJSON(w, http.StatusUnauthorized, errorEnvelope("stripe_billing.invalid_signature", "webhook signature verification failed", r))
			return
		}
		s.logger.Error("stripe_billing: webhook parse failed", slog.Any("error", err))
		writeJSON(w, http.StatusBadRequest, errorEnvelope("stripe_billing.parse_error", "failed to parse webhook event", r))
		return
	}

	ctx := r.Context()

	switch event.EventType {
	case stripebilling.EventInvoicePaid:
		if syncErr := s.syncStripeBillingInvoicePaid(ctx, event); syncErr != nil {
			s.logger.Error("stripe_billing: handle invoice.paid failed",
				slog.String("stripe_event_id", event.StripeEventID),
				slog.String("stripe_invoice_id", event.StripeInvoiceID),
				slog.Any("error", syncErr),
			)
			// Return 200 anyway to prevent Stripe retries; reconcile manually.
		}

	case stripebilling.EventInvoicePaymentFailed:
		s.logger.Warn("stripe_billing: invoice payment failed",
			slog.String("stripe_event_id", event.StripeEventID),
			slog.String("stripe_invoice_id", event.StripeInvoiceID),
			slog.String("status", event.Status),
		)

	default:
		// Unknown event type — acknowledge and ignore.
		s.logger.Info("stripe_billing: ignoring unknown webhook event",
			slog.String("event_type", event.EventType),
			slog.String("stripe_event_id", event.StripeEventID),
		)
	}

	// Always return 200 to Stripe to prevent retries.
	writeJSON(w, http.StatusOK, map[string]any{"received": true})
}

// syncStripeBillingInvoicePaid transitions the local invoice to "paid" state
// when Stripe confirms payment. Safe to call multiple times (idempotent):
// if the invoice is already "paid", the state transition is a no-op.
//
// Panics from the DB layer (e.g. nil pool in tests) are caught and returned
// as errors so the webhook handler can log them and still return HTTP 200.
func (s *Server) syncStripeBillingInvoicePaid(ctx context.Context, event *stripebilling.BillingWebhookEvent) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("stripe_billing: syncStripeBillingInvoicePaid: recovered panic: %v", r)
		}
	}()

	// Look up the local invoice by stripe_invoice_id.
	localInvoice, err := s.billingQueries.GetInvoiceByStripeID(ctx, event.StripeInvoiceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.logger.Warn("stripe_billing: invoice.paid event for unknown stripe_invoice_id",
				slog.String("stripe_invoice_id", event.StripeInvoiceID),
				slog.String("stripe_event_id", event.StripeEventID),
			)
			return nil // idempotent — ignore unknown invoices
		}
		return fmt.Errorf("get invoice by stripe ID: %w", err)
	}

	// Skip if already in a terminal state.
	if isTerminalInvoiceState(localInvoice.State) {
		s.logger.Info("stripe_billing: invoice already in terminal state; skip paid sync",
			slog.String("invoice_id", localInvoice.ID.String()),
			slog.String("state", localInvoice.State),
		)
		return nil
	}

	// Validate transition to "paid" is legal from current state.
	if !validInvoiceTransitions[localInvoice.State]["paid"] {
		s.logger.Warn("stripe_billing: cannot transition invoice to paid from current state",
			slog.String("invoice_id", localInvoice.ID.String()),
			slog.String("current_state", localInvoice.State),
		)
		return nil // Log and skip; do not return error to Stripe.
	}

	if _, err := s.billingQueries.UpdateInvoiceState(ctx, localInvoice.ID, "paid"); err != nil {
		return fmt.Errorf("update invoice state to paid: %w", err)
	}

	s.logger.Info("stripe_billing: invoice marked paid via Stripe webhook",
		slog.String("invoice_id", localInvoice.ID.String()),
		slog.String("stripe_invoice_id", event.StripeInvoiceID),
		slog.String("stripe_event_id", event.StripeEventID),
	)
	return nil
}
