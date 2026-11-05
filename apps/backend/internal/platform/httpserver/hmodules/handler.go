// Package hmodules serves the paid modules of an organization (spec
// 08_architecture/61 section 4.4): a platform superadmin connects, ends and
// re-prices the shop module; the organizer reads its periods and price.
//
// Superadmin routes (the server mounts them behind superadmin.read AND the
// platform superadmin marker; every write needs X-Admin-Reason and writes its
// audit row on the change's own transaction, with the reason):
//
//	GET  /v1/admin/organizations/{org_id}/modules
//	POST /v1/admin/organizations/{org_id}/modules/shop
//	POST /v1/admin/org-modules/{id}/end
//	POST /v1/admin/org-modules/{id}/reprice
//
// Organizer route (billing.read and membership of {org_id}, read-only like
// GET .../billing/white-label):
//
//	GET  /v1/organizations/{org_id}/billing/modules
package hmodules

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orgmodules"
)

// Audit actions of the superadmin writes.
const (
	AuditConnect = "v1.admin.org_module.connect"
	AuditEnd     = "v1.admin.org_module.end"
	AuditReprice = "v1.admin.org_module.reprice"
)

// shopModuleMonthKind is the billing event kind of one shop module-month
// (migration 0198): the price list's 'subscription' rule for it is the list
// price of the module.
const shopModuleMonthKind = "shop_module_month"

const (
	dateLayout = "2006-01-02"
	maxBody    = 64 << 10
)

// Handler serves the module routes.
type Handler struct {
	pool    *pgxpool.Pool
	queries *gen.Queries
	audit   audit.Writer
	logger  *slog.Logger
	now     func() time.Time
}

// New builds the handler. A nil pool makes every route answer 503.
func New(pool *pgxpool.Pool, auditWriter audit.Writer, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{pool: pool, audit: auditWriter, logger: logger, now: func() time.Time { return time.Now().UTC() }}
	if pool != nil {
		h.queries = gen.New(pool)
	}
	return h
}

// ─── answers ────────────────────────────────────────────────────────────────

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.ErrorContext(r.Context(), "org modules: "+what, "error", err, "request_id", logging.RequestID(r.Context()))
	writeErr(w, r, http.StatusInternalServerError, "org_module.failed", "nothing was changed: the operation failed, please try again")
}

// moduleErr answers an orgmodules refusal with its code; anything else is a
// logged 500.
func (h *Handler) moduleErr(w http.ResponseWriter, r *http.Request, what string, err error) {
	switch {
	case errors.Is(err, orgmodules.ErrOverlap):
		writeErr(w, r, http.StatusConflict, "org_module.overlap",
			"this organization already has the module on some of these days; end that period first or pick other dates")
	case errors.Is(err, orgmodules.ErrInvalid):
		writeErr(w, r, http.StatusUnprocessableEntity, "org_module.invalid", strings.TrimPrefix(err.Error(), orgmodules.ErrInvalid.Error()+": "))
	case errors.Is(err, orgmodules.ErrNotFound):
		writeErr(w, r, http.StatusNotFound, "org_module.not_found", "this module period does not exist")
	case errors.Is(err, orgmodules.ErrAlreadyEnded):
		writeErr(w, r, http.StatusConflict, "org_module.already_ended",
			"this period already ends on or before that day; ending a period never extends it")
	default:
		h.fail(w, r, what, err)
	}
}

// ready answers 503 without a database, or - for a write - without an audit
// writer that puts its row on the change's transaction.
func (h *Handler) ready(w http.ResponseWriter, r *http.Request, write bool) bool {
	if h.pool == nil || h.queries == nil {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable",
			"modules are not available right now, please try again in a moment")
		return false
	}
	if write && !audit.IsDurableInTx(h.audit) {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.audit_unavailable",
			"nothing was changed: the audit log is not available, please try again in a moment")
		return false
	}
	return true
}

// decodeBody reads one JSON object into dst, refusing unknown keys and
// trailing data.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeErr(w, r, http.StatusBadRequest, "org_module.invalid_body",
			"the request body is not valid JSON of the expected shape: "+err.Error())
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeErr(w, r, http.StatusBadRequest, "org_module.invalid_body", "the request body has data after the JSON object")
		return false
	}
	return true
}

func actorUUID(r *http.Request) *uuid.UUID {
	a, _ := auth.ActorFromContext(r.Context())
	id, err := uuid.Parse(a.ID)
	if err != nil {
		return nil
	}
	return &id
}

// auditTx writes the audit row of a change on the change's transaction; the
// operator's X-Admin-Reason is metadata.reason.
func (h *Handler) auditTx(r *http.Request, tx pgx.Tx, action string, periodID uuid.UUID, reason string, meta map[string]any) error {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["reason"] = reason
	actorID := ""
	if id := actorUUID(r); id != nil {
		actorID = id.String()
	}
	return h.audit.WriteTx(r.Context(), tx, audit.Event{
		OccurredAt:   h.now(),
		ActorType:    "user",
		ActorID:      actorID,
		Action:       action,
		ResourceType: "org_module_period",
		ResourceID:   periodID.String(),
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     meta,
	})
}

func dateJSON(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(dateLayout)
}

func timeJSON(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func uuidJSON(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// status is "scheduled", "in_force" or "ended" on day.
func status(p orgmodules.Period, day time.Time) string {
	switch {
	case p.StartsOn.After(day):
		return "scheduled"
	case p.EndsOn != nil && p.EndsOn.Before(day):
		return "ended"
	}
	return "in_force"
}

// periodJSON is a period as the routes answer it; operator=false leaves out
// the operator-only fields (reason, created_by, ended_by).
func periodJSON(p orgmodules.Period, day time.Time, operator bool) map[string]any {
	out := map[string]any{
		"id":                  p.ID.String(),
		"org_id":              p.OrgID.String(),
		"module":              p.Module,
		"starts_on":           p.StartsOn.Format(dateLayout),
		"ends_on":             dateJSON(p.EndsOn),
		"monthly_price_minor": p.MonthlyPriceMinor,
		"currency":            p.Currency,
		"status":              status(p, day),
		"created_at":          p.CreatedAt.UTC().Format(time.RFC3339Nano),
		"ended_at":            timeJSON(p.EndedAt),
	}
	if operator {
		out["reason"] = p.Reason
		out["created_by"] = uuidJSON(p.CreatedBy)
		out["ended_by"] = uuidJSON(p.EndedBy)
	}
	return out
}

func parseDay(s string) (time.Time, bool) {
	t, err := time.Parse(dateLayout, strings.TrimSpace(s))
	return t, err == nil
}

// ─── price ──────────────────────────────────────────────────────────────────

// listPrice is the price list's monthly price of the shop module for the
// organization's billing account today, in the account's currency. It never
// creates an account: an organization without one, a price list without the
// rule, or terms that cannot be read answer nil (the currency is still given
// when it is known).
func (h *Handler) listPrice(ctx context.Context, orgID uuid.UUID, now time.Time) (*int64, string) {
	country, err := h.queries.GetOrgBillingCountry(ctx, orgID)
	if err != nil {
		return nil, ""
	}
	currency, _, err := billing.RouteCurrency(ctx, h.queries, country)
	if err != nil {
		return nil, ""
	}
	acc, err := h.queries.GetBillingAccount(ctx, orgID, currency, billing.AccountKindOrganizer)
	if err != nil {
		return nil, currency
	}
	terms, err := billing.TermsAt(ctx, h.queries, acc.ID, now)
	if err != nil {
		return nil, currency
	}
	for _, rule := range terms.Rules {
		if rule.Type != billing.RuleSubscription || rule.EventKind != shopModuleMonthKind ||
			rule.Line != "standard" || rule.SaleCurrency != "" {
			continue
		}
		if rule.Subscription != nil {
			v := rule.Subscription.MonthlyMinor
			return &v, currency
		}
		var p struct {
			MonthlyMinor *int64 `json:"monthly_minor"`
		}
		if json.Unmarshal(rule.RawParams, &p) == nil && p.MonthlyMinor != nil {
			return p.MonthlyMinor, currency
		}
	}
	return nil, currency
}

// effectivePrice is the monthly price that applies today: the individual
// price of the period covering today, else the price list's. source is
// "individual", "price_list" or "" (unknown).
type effectivePrice struct {
	MonthlyMinor *int64
	Currency     *string
	Source       string
}

func (h *Handler) effective(ctx context.Context, orgID uuid.UUID, periods []orgmodules.Period, now time.Time) effectivePrice {
	for _, p := range periods {
		if p.Module == orgmodules.ModuleShop && p.Covers(now) && p.MonthlyPriceMinor != nil {
			v, c := *p.MonthlyPriceMinor, p.Currency
			return effectivePrice{MonthlyMinor: &v, Currency: c, Source: "individual"}
		}
	}
	v, cur := h.listPrice(ctx, orgID, now)
	out := effectivePrice{MonthlyMinor: v}
	if cur != "" {
		out.Currency = &cur
	}
	if v != nil {
		out.Source = "price_list"
	}
	return out
}

func (e effectivePrice) source() any {
	if e.Source == "" {
		return nil
	}
	return e.Source
}

// billingCurrency resolves the organization's billing currency, used when an
// individual price is given without one.
func (h *Handler) billingCurrency(ctx context.Context, q *gen.Queries, orgID uuid.UUID) (string, error) {
	country, err := q.GetOrgBillingCountry(ctx, orgID)
	if err != nil {
		return "", err
	}
	cur, _, err := billing.RouteCurrency(ctx, q, country)
	return cur, err
}

// priceInput pairs an individual price with its currency: both absent is the
// price list; a price without a currency is in the organization's billing
// currency.
func (h *Handler) priceInput(ctx context.Context, q *gen.Queries, orgID uuid.UUID, price *int64, currency *string) (*int64, *string, error) {
	if currency != nil && strings.TrimSpace(*currency) == "" {
		currency = nil
	}
	if price == nil || currency != nil {
		return price, currency, nil
	}
	cur, err := h.billingCurrency(ctx, q, orgID)
	if err != nil {
		return nil, nil, err
	}
	return price, &cur, nil
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
