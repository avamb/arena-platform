// billing_ledger.go implements the service billing ledger HTTP API (feature #161).
//
// The billing ledger tracks platform service fees charged to organizers/agents.
// It is separate from customer ticket payments (those are in the payment layer).
//
// Entities:
//   - tariffs        — versioned tariff plans (per-ticket, per-event, monthly fees)
//   - usage_records  — monthly usage counters per org (incremented by usage hooks)
//   - invoices       — billing invoice headers (draft → issued → paid / void)
//   - invoice_lines  — line items per invoice (links to tariff version used)
//
// Invoice state machine:
//
//	draft → issued → paid   (terminal)
//	draft → void            (terminal)
//	issued → void           (terminal)
//
// Endpoints (all require JWT auth):
//
//	POST /v1/billing/tariffs                           — create tariff version (billing.admin)
//	GET  /v1/billing/tariffs/active                   — get active tariff     (billing.read)
//	GET  /v1/organizations/{org_id}/billing/usage     — current usage         (billing.read)
//	POST /v1/billing/invoices/generate                — month-end batch       (billing.admin)
//	GET  /v1/organizations/{org_id}/billing/invoices  — list invoices         (billing.read)
//	GET  /v1/billing/invoices/{id}                    — get invoice + lines   (billing.read)
//	POST /v1/billing/invoices/{id}/issue              — draft → issued        (billing.admin)
//	POST /v1/billing/invoices/{id}/pay                — issued → paid         (billing.admin)
//	POST /v1/billing/invoices/{id}/void               — draft/issued → void   (billing.admin)
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	billingdomain "github.com/abhteam/arena_new/apps/backend/internal/domain/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// ─────────────────────────────────────────────────────────────────────────────
// Invoice state transition table
//
// The invoice state machine itself lives in internal/domain/billing
// (feature #187 — "DDD split: billing / reporting"). The package-level vars
// and helpers below are thin forwarders so that all in-package call sites
// and tests continue to compile unchanged, while the canonical source of
// truth is the pure-domain package.
// ─────────────────────────────────────────────────────────────────────────────

// validInvoiceTransitions forwards to billingdomain.ValidInvoiceTransitions.
var validInvoiceTransitions = billingdomain.ValidInvoiceTransitions

// allInvoiceStates forwards to billingdomain.AllInvoiceStates.
var allInvoiceStates = billingdomain.AllInvoiceStates

const (
	billingPeriodLayout = billingdomain.BillingPeriodLayout
	billingDateLayout   = billingdomain.BillingDateLayout
)

// isTerminalInvoiceState forwards to billingdomain.IsTerminalInvoiceState.
func isTerminalInvoiceState(state string) bool {
	return billingdomain.IsTerminalInvoiceState(state)
}

// ─────────────────────────────────────────────────────────────────────────────
// billingPeriodForTime forwards to billingdomain.PeriodForTime.
// ─────────────────────────────────────────────────────────────────────────────

func billingPeriodForTime(t time.Time) string {
	return billingdomain.PeriodForTime(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/billing/tariffs — create a new tariff version
// ─────────────────────────────────────────────────────────────────────────────

type createTariffRequest struct {
	OrgID             *string `json:"org_id"`
	PlanName          string  `json:"plan_name"`
	EffectiveFrom     string  `json:"effective_from"` // 'YYYY-MM-DD'
	PerTicketFeeMinor int64   `json:"per_ticket_fee_minor"`
	PerEventFeeMinor  int64   `json:"per_event_fee_minor"`
	MonthlyFeeMinor   int64   `json:"monthly_fee_minor"`
	Currency          string  `json:"currency"`
	Notes             *string `json:"notes"`
}

func (s *Server) handleCreateTariff(w http.ResponseWriter, r *http.Request) {
	if s.billingQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("billing.unavailable", "billing ledger not configured", r))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.invalid_body", "cannot read request body", r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.empty_body", "request body is required", r))
		return
	}

	var req createTariffRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.invalid_json", "request body is not valid JSON", r))
		return
	}

	if req.PlanName == "" {
		req.PlanName = "standard"
	}
	if req.Currency == "" {
		req.Currency = "EUR"
	}
	if req.EffectiveFrom == "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope("billing.validation", "effective_from is required (YYYY-MM-DD)", r))
		return
	}
	effectiveFrom, err := time.Parse("2006-01-02", req.EffectiveFrom)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope("billing.validation", "effective_from must be YYYY-MM-DD", r))
		return
	}
	if req.PerTicketFeeMinor < 0 || req.PerEventFeeMinor < 0 || req.MonthlyFeeMinor < 0 {
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope("billing.validation", "fee amounts must be non-negative", r))
		return
	}

	var orgID *uuid.UUID
	if req.OrgID != nil && *req.OrgID != "" {
		id, err := uuid.Parse(*req.OrgID)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope("billing.validation", "org_id must be a valid UUID", r))
			return
		}
		orgID = &id
	}

	actorID := ""
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		actorID = a.ID
	}
	var createdBy *string
	if actorID != "" {
		createdBy = &actorID
	}

	row, err := s.billingQueries.InsertTariff(
		r.Context(),
		orgID,
		req.PlanName,
		effectiveFrom,
		req.PerTicketFeeMinor,
		req.PerEventFeeMinor,
		req.MonthlyFeeMinor,
		req.Currency,
		req.Notes,
		createdBy,
	)
	if err != nil {
		s.logger.Error("billing: insert tariff failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "failed to create tariff", r))
		return
	}

	writeJSON(w, http.StatusCreated, tariffToResponse(row))
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/billing/tariffs/active — get the currently active tariff
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleGetActiveTariff(w http.ResponseWriter, r *http.Request) {
	if s.billingQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("billing.unavailable", "billing ledger not configured", r))
		return
	}

	// Optional ?org_id= query param; defaults to global tariff lookup.
	var orgID uuid.UUID
	if orgIDStr := r.URL.Query().Get("org_id"); orgIDStr != "" {
		id, err := uuid.Parse(orgIDStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.bad_request", "org_id must be a valid UUID", r))
			return
		}
		orgID = id
	}

	row, err := s.billingQueries.GetActiveTariff(r.Context(), orgID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("billing.not_found", "no active tariff configured", r))
			return
		}
		s.logger.Error("billing: get active tariff failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "failed to fetch active tariff", r))
		return
	}

	writeJSON(w, http.StatusOK, tariffToResponse(row))
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/billing/usage — current-period usage for org
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	if s.billingQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("billing.unavailable", "billing ledger not configured", r))
		return
	}

	orgIDStr := chi.URLParam(r, "org_id")
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.bad_request", "org_id must be a valid UUID", r))
		return
	}

	// Default to current period; allow ?period=YYYY-MM override.
	period := billingPeriodForTime(time.Now().UTC())
	if p := r.URL.Query().Get("period"); p != "" {
		if _, err := time.Parse("2006-01", p); err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.bad_request", "period must be YYYY-MM format", r))
			return
		}
		period = p
	}

	row, err := s.billingQueries.GetUsageRecord(r.Context(), orgID, period)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No usage yet — return zeroed record rather than 404.
			writeJSON(w, http.StatusOK, map[string]any{
				"org_id":               orgID.String(),
				"billing_period":       period,
				"tickets_sold":         0,
				"complimentary_issued": 0,
				"events_published":     0,
			})
			return
		}
		s.logger.Error("billing: get usage record failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "failed to fetch usage record", r))
		return
	}

	writeJSON(w, http.StatusOK, usageToResponse(row))
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/billing/invoices/generate — month-end batch: generate draft invoices
// ─────────────────────────────────────────────────────────────────────────────

type generateInvoicesRequest struct {
	BillingPeriod string `json:"billing_period"` // 'YYYY-MM'
}

func (s *Server) handleGenerateInvoices(w http.ResponseWriter, r *http.Request) {
	if s.billingQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("billing.unavailable", "billing ledger not configured", r))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.invalid_body", "cannot read request body", r))
		return
	}

	var req generateInvoicesRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.invalid_json", "request body is not valid JSON", r))
			return
		}
	}

	if req.BillingPeriod == "" {
		// Default: previous month.
		prev := time.Now().UTC().AddDate(0, -1, 0)
		req.BillingPeriod = billingPeriodForTime(prev)
	}
	if _, err := time.Parse("2006-01", req.BillingPeriod); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope("billing.validation", "billing_period must be YYYY-MM", r))
		return
	}

	results, err := s.generateInvoicesForPeriod(r.Context(), req.BillingPeriod)
	if err != nil {
		s.logger.Error("billing: month-end generate failed",
			slog.String("period", req.BillingPeriod),
			slog.Any("error", err),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "invoice generation failed", r))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"billing_period":   req.BillingPeriod,
		"invoices_created": len(results),
		"invoices":         results,
	})
}

// generateInvoicesForPeriod is the core month-end logic. For each org that has
// usage in the period, it:
//  1. Looks up the active tariff as of the period start date.
//  2. Creates a draft invoice (idempotent: skips if already exists).
//  3. Inserts invoice lines for each tariff component with non-zero usage.
//  4. Finalizes the invoice total.
func (s *Server) generateInvoicesForPeriod(ctx context.Context, period string) ([]map[string]any, error) {
	// Parse period to get the period start date for tariff lookup.
	periodStart, err := time.Parse("2006-01", period)
	if err != nil {
		return nil, fmt.Errorf("invalid period %q: %w", period, err)
	}

	// Find all orgs with usage in this period.
	usageRecords, err := s.billingQueries.ListUsageRecordsByPeriod(ctx, period)
	if err != nil {
		return nil, fmt.Errorf("list usage records: %w", err)
	}

	var results []map[string]any

	for _, usage := range usageRecords {
		// Skip if invoice already exists (idempotent).
		existing, err := s.billingQueries.GetInvoiceByOrgAndPeriod(ctx, usage.OrgID, period)
		if err == nil {
			results = append(results, map[string]any{
				"invoice_id":     existing.ID.String(),
				"org_id":         usage.OrgID.String(),
				"already_exists": true,
				"state":          existing.State,
			})
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("check existing invoice for org %s: %w", usage.OrgID, err)
		}

		// Look up active tariff for this org.
		tariff, err := s.billingQueries.GetActiveTariff(ctx, usage.OrgID, periodStart)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				s.logger.Warn("billing: no tariff for org, skipping invoice generation",
					slog.String("org_id", usage.OrgID.String()),
					slog.String("period", period),
				)
				continue
			}
			return nil, fmt.Errorf("get tariff for org %s: %w", usage.OrgID, err)
		}

		// Build invoice lines.
		type lineItem struct {
			description string
			quantity    int64
			unitAmount  int64
			totalAmount int64
		}
		var lines []lineItem

		if tariff.MonthlyFeeMinor > 0 {
			lines = append(lines, lineItem{
				description: fmt.Sprintf("Monthly platform fee (%s)", period),
				quantity:    1,
				unitAmount:  tariff.MonthlyFeeMinor,
				totalAmount: tariff.MonthlyFeeMinor,
			})
		}
		if tariff.PerTicketFeeMinor > 0 && usage.TicketsSold > 0 {
			lines = append(lines, lineItem{
				description: fmt.Sprintf("Per-ticket service fee (%d tickets × %d %s)",
					usage.TicketsSold, tariff.PerTicketFeeMinor, tariff.Currency),
				quantity:    usage.TicketsSold,
				unitAmount:  tariff.PerTicketFeeMinor,
				totalAmount: usage.TicketsSold * tariff.PerTicketFeeMinor,
			})
		}
		if tariff.PerEventFeeMinor > 0 && usage.EventsPublished > 0 {
			lines = append(lines, lineItem{
				description: fmt.Sprintf("Per-event service fee (%d events × %d %s)",
					usage.EventsPublished, tariff.PerEventFeeMinor, tariff.Currency),
				quantity:    usage.EventsPublished,
				unitAmount:  tariff.PerEventFeeMinor,
				totalAmount: usage.EventsPublished * tariff.PerEventFeeMinor,
			})
		}

		var totalMinor int64
		for _, l := range lines {
			totalMinor += l.totalAmount
		}

		// Create draft invoice.
		invoice, err := s.billingQueries.InsertInvoice(ctx, usage.OrgID, period, 0, tariff.Currency)
		if err != nil {
			return nil, fmt.Errorf("insert invoice for org %s: %w", usage.OrgID, err)
		}

		// Insert lines.
		tariffIDPtr := &tariff.ID
		for _, l := range lines {
			if _, err := s.billingQueries.InsertInvoiceLine(
				ctx,
				invoice.ID,
				tariffIDPtr,
				l.description,
				l.quantity,
				l.unitAmount,
				l.totalAmount,
				tariff.Currency,
			); err != nil {
				return nil, fmt.Errorf("insert invoice line for org %s: %w", usage.OrgID, err)
			}
		}

		// Update total.
		invoice, err = s.billingQueries.UpdateInvoiceTotal(ctx, invoice.ID, totalMinor)
		if err != nil {
			return nil, fmt.Errorf("update invoice total for org %s: %w", usage.OrgID, err)
		}

		s.logger.Info("billing: generated draft invoice",
			slog.String("invoice_id", invoice.ID.String()),
			slog.String("org_id", usage.OrgID.String()),
			slog.String("period", period),
			slog.Int64("total_minor", totalMinor),
			slog.Int("lines", len(lines)),
		)

		results = append(results, map[string]any{
			"invoice_id":  invoice.ID.String(),
			"org_id":      usage.OrgID.String(),
			"state":       invoice.State,
			"total_minor": totalMinor,
			"currency":    tariff.Currency,
			"lines_count": len(lines),
		})
	}

	return results, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/billing/invoices/{id} — get invoice + lines
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleGetInvoice(w http.ResponseWriter, r *http.Request) {
	if s.billingQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("billing.unavailable", "billing ledger not configured", r))
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.bad_request", "id must be a valid UUID", r))
		return
	}

	invoice, err := s.billingQueries.GetInvoiceByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("billing.not_found", "invoice not found", r))
			return
		}
		s.logger.Error("billing: get invoice failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "failed to fetch invoice", r))
		return
	}

	lines, err := s.billingQueries.ListInvoiceLines(r.Context(), id)
	if err != nil {
		s.logger.Error("billing: list invoice lines failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "failed to fetch invoice lines", r))
		return
	}

	resp := invoiceToResponse(invoice)
	lineResps := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		lineResps = append(lineResps, invoiceLineToResponse(l))
	}
	resp["lines"] = lineResps

	writeJSON(w, http.StatusOK, resp)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/billing/invoices — list invoices for org
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleListOrgInvoices(w http.ResponseWriter, r *http.Request) {
	if s.billingQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("billing.unavailable", "billing ledger not configured", r))
		return
	}

	orgIDStr := chi.URLParam(r, "org_id")
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.bad_request", "org_id must be a valid UUID", r))
		return
	}

	invoices, err := s.billingQueries.ListInvoicesByOrg(r.Context(), orgID)
	if err != nil {
		s.logger.Error("billing: list invoices failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "failed to list invoices", r))
		return
	}

	resps := make([]map[string]any, 0, len(invoices))
	for _, inv := range invoices {
		resps = append(resps, invoiceToResponse(inv))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"org_id":   orgID.String(),
		"invoices": resps,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/billing/invoices/{id}/issue — draft → issued
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleIssueInvoice(w http.ResponseWriter, r *http.Request) {
	s.handleInvoiceTransition(w, r, "issued")
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/billing/invoices/{id}/pay — issued → paid
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handlePayInvoice(w http.ResponseWriter, r *http.Request) {
	s.handleInvoiceTransition(w, r, "paid")
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/billing/invoices/{id}/void — draft/issued → void
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleVoidInvoice(w http.ResponseWriter, r *http.Request) {
	s.handleInvoiceTransition(w, r, "void")
}

// handleInvoiceTransition is the shared state transition handler.
func (s *Server) handleInvoiceTransition(w http.ResponseWriter, r *http.Request, targetState string) {
	if s.billingQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("billing.unavailable", "billing ledger not configured", r))
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("billing.bad_request", "id must be a valid UUID", r))
		return
	}

	invoice, err := s.billingQueries.GetInvoiceByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("billing.not_found", "invoice not found", r))
			return
		}
		s.logger.Error("billing: get invoice for transition failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "failed to fetch invoice", r))
		return
	}

	// Check terminal state.
	if isTerminalInvoiceState(invoice.State) {
		writeJSON(w, http.StatusConflict, errorEnvelope("billing.terminal_state",
			fmt.Sprintf("invoice is in terminal state '%s'", invoice.State), r))
		return
	}

	// Validate transition.
	if !validInvoiceTransitions[invoice.State][targetState] {
		writeJSON(w, http.StatusConflict, errorEnvelope("billing.invalid_transition",
			fmt.Sprintf("cannot transition from '%s' to '%s'", invoice.State, targetState), r))
		return
	}

	updated, err := s.billingQueries.UpdateInvoiceState(r.Context(), id, targetState)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("billing.not_found", "invoice not found", r))
			return
		}
		s.logger.Error("billing: update invoice state failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("billing.internal", "failed to update invoice state", r))
		return
	}

	s.logger.Info("billing: invoice state transition",
		slog.String("invoice_id", id.String()),
		slog.String("from", invoice.State),
		slog.String("to", updated.State),
	)

	writeJSON(w, http.StatusOK, invoiceToResponse(updated))
}

// ─────────────────────────────────────────────────────────────────────────────
// IncrementBillingUsage — usage capture hook called by other handlers
// ─────────────────────────────────────────────────────────────────────────────

// IncrementBillingUsage increments usage counters for the given org and the
// current billing period. Designed to be called from ticket issuance and
// event publication hooks. Safe to call with nil billingQueries (no-op).
func (s *Server) IncrementBillingUsage(
	ctx context.Context,
	orgID uuid.UUID,
	deltaTickets int64,
	deltaComplimentary int64,
	deltaEvents int64,
) {
	if s.billingQueries == nil {
		return
	}
	period := billingPeriodForTime(time.Now().UTC())
	_, err := s.billingQueries.IncrementUsageRecord(ctx, orgID, period, deltaTickets, deltaComplimentary, deltaEvents)
	if err != nil {
		s.logger.Error("billing: increment usage record failed",
			slog.String("org_id", orgID.String()),
			slog.String("period", period),
			slog.Any("error", err),
		)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Response helpers
// ─────────────────────────────────────────────────────────────────────────────

func tariffToResponse(r gen.TariffRow) map[string]any {
	resp := map[string]any{
		"id":                   r.ID.String(),
		"plan_name":            r.PlanName,
		"effective_from":       r.EffectiveFrom.UTC().Format(billingDateLayout),
		"per_ticket_fee_minor": r.PerTicketFeeMinor,
		"per_event_fee_minor":  r.PerEventFeeMinor,
		"monthly_fee_minor":    r.MonthlyFeeMinor,
		"currency":             r.Currency,
		"created_at":           r.CreatedAt.UTC().Format(time.RFC3339),
	}
	if r.OrgID != nil {
		resp["org_id"] = r.OrgID.String()
	} else {
		resp["org_id"] = nil
	}
	if r.Notes != nil {
		resp["notes"] = *r.Notes
	} else {
		resp["notes"] = nil
	}
	if r.CreatedBy != nil {
		resp["created_by"] = *r.CreatedBy
	} else {
		resp["created_by"] = nil
	}
	return resp
}

func usageToResponse(r gen.UsageRecordRow) map[string]any {
	return map[string]any{
		"id":                   r.ID.String(),
		"org_id":               r.OrgID.String(),
		"billing_period":       r.BillingPeriod,
		"tickets_sold":         r.TicketsSold,
		"complimentary_issued": r.ComplimentaryIssued,
		"events_published":     r.EventsPublished,
		"created_at":           r.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":           r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func invoiceToResponse(r gen.InvoiceRow) map[string]any {
	resp := map[string]any{
		"id":                 r.ID.String(),
		"org_id":             r.OrgID.String(),
		"billing_period":     r.BillingPeriod,
		"state":              r.State,
		"total_amount_minor": r.TotalAmountMinor,
		"currency":           r.Currency,
		"created_at":         r.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":         r.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if r.IssuedAt != nil {
		resp["issued_at"] = r.IssuedAt.UTC().Format(time.RFC3339)
	} else {
		resp["issued_at"] = nil
	}
	if r.PaidAt != nil {
		resp["paid_at"] = r.PaidAt.UTC().Format(time.RFC3339)
	} else {
		resp["paid_at"] = nil
	}
	if r.VoidedAt != nil {
		resp["voided_at"] = r.VoidedAt.UTC().Format(time.RFC3339)
	} else {
		resp["voided_at"] = nil
	}
	return resp
}

func invoiceLineToResponse(r gen.InvoiceLineRow) map[string]any {
	resp := map[string]any{
		"id":                 r.ID.String(),
		"invoice_id":         r.InvoiceID.String(),
		"description":        r.Description,
		"quantity":           r.Quantity,
		"unit_amount_minor":  r.UnitAmountMinor,
		"total_amount_minor": r.TotalAmountMinor,
		"currency":           r.Currency,
		"created_at":         r.CreatedAt.UTC().Format(time.RFC3339),
	}
	if r.TariffID != nil {
		resp["tariff_id"] = r.TariffID.String()
	} else {
		resp["tariff_id"] = nil
	}
	return resp
}
