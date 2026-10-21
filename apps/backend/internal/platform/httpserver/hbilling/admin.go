// admin.go - the platform superadmin screen "Price lists and rates" (billing
// wave B-A, task B1-5): versions of a price list, individual rates of an
// organization, and the dated terms of a billing account.
//
// Every route lives under /v1/admin/billing, needs the billing.admin
// permission AND a platform superadmin (the server marks one with
// auth.HasSuperadminOrgAccess), and every write needs X-Admin-Reason. Every
// write also writes its audit_events row IN THE SAME TRANSACTION as the
// change; a handler without an audit writer answers 503 instead of changing
// anything (fail closed).
//
// What the screen never does:
//   - no number comes from here: every rate, threshold and cap is typed in by
//     the operator, so there are no defaults to leak a price into a version;
//   - a price-list version is only INSERTED. The one UPDATE is revoked_at,
//     and only before the version's effective_from (RevokePriceListVersion
//     repeats the rule in its WHERE, so a race with the clock loses);
//   - a new version, individual rate or terms row must start in the future,
//     so it can never reach an event that already happened;
//   - an individual rate is closed (effective_to) or, before it has started,
//     removed; a rate in force is never edited.
package hbilling

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// maxAdminBody caps a request body of the admin billing routes (a price list
// with its rules and packs is a few kilobytes).
const maxAdminBody = 1 << 20

// Audit actions of the screen. A test walks the router and fails when a
// write route of /v1/admin/billing has no action here.
const (
	AuditPriceListCreate    = "v1.admin.billing.price_list.create"
	AuditPriceListRevoke    = "v1.admin.billing.price_list.revoke"
	AuditRateOverrideCreate = "v1.admin.billing.rate_override.create"
	AuditRateOverrideClose  = "v1.admin.billing.rate_override.close"
	AuditRateOverrideDelete = "v1.admin.billing.rate_override.delete"
	AuditAccountTermsCreate = "v1.admin.billing.account_terms.create"
)

// AdminHandler serves the /v1/admin/billing routes.
type AdminHandler struct {
	pool    *pgxpool.Pool
	queries *gen.Queries
	audit   audit.Writer
	logger  *slog.Logger
	now     func() time.Time
	// stripe is the platform Stripe account (admin_invoices.go); nil when
	// BILLING_STRIPE_SECRET_KEY is not set.
	stripe        PlatformStripe
	returnBaseURL string
}

// NewAdmin builds the handler. A nil pool makes every route answer 503.
func NewAdmin(pool *pgxpool.Pool, auditWriter audit.Writer, logger *slog.Logger) *AdminHandler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &AdminHandler{pool: pool, audit: auditWriter, logger: logger, now: func() time.Time { return time.Now().UTC() }}
	if pool != nil {
		h.queries = gen.New(pool)
	}
	return h
}

// WithClock replaces the clock; tests move it to check the "only before
// effective_from" rules.
func (h *AdminHandler) WithClock(now func() time.Time) *AdminHandler {
	h.now = now
	return h
}

// ready answers 503 when the handler has no database or no audit writer.
func (h *AdminHandler) ready(w http.ResponseWriter, r *http.Request, write bool) bool {
	if h.pool == nil || h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.billing_unavailable", "billing is not available right now, please try again in a moment", r))
		return false
	}
	if write && h.audit == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.audit_unavailable", "nothing was changed: the audit log is not available, please try again in a moment", r))
		return false
	}
	return true
}

// writeStart is the common start of every write: the handler is ready and
// the operator gave a reason. It returns the reason.
func (h *AdminHandler) writeStart(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !h.ready(w, r, true) {
		return "", false
	}
	return httputil.RequireAdminReason(w, r)
}

// decodeBody reads one JSON object into dst, refusing unknown keys and
// trailing data.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"billing.invalid_body", "the request body is not valid JSON of the expected shape: "+err.Error(), r))
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"billing.invalid_body", "the request body has data after the JSON object", r))
		return false
	}
	return true
}

// fail answers a failed operation: 500 with a fixed message, the cause only
// in the log.
func (h *AdminHandler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.ErrorContext(r.Context(), "admin billing: "+what, "error", err, "request_id", logging.RequestID(r.Context()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"billing.admin_failed", "nothing was changed: the operation failed, please try again", r))
}

func unprocessable(w http.ResponseWriter, r *http.Request, code, msg string) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(code, msg, r))
}

func conflict(w http.ResponseWriter, r *http.Request, code, msg string) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(code, msg, r))
}

func notFound(w http.ResponseWriter, r *http.Request, code, msg string) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(code, msg, r))
}

// actorID is the caller's id for created_by columns and audit rows: the UUID
// of the principal, "" when there is none.
func actorID(r *http.Request) string {
	a, _ := auth.ActorFromContext(r.Context())
	return a.ID
}

// auditTx writes the audit row of a change on the change's transaction.
func (h *AdminHandler) auditTx(r *http.Request, tx pgx.Tx, action, resourceType string, resourceID uuid.UUID, reason string, meta map[string]any) error {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["reason"] = reason
	return h.audit.WriteTx(r.Context(), tx, audit.Event{
		OccurredAt:   h.now(),
		ActorType:    "user",
		ActorID:      actorID(r),
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID.String(),
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     meta,
	})
}

// timeJSON formats a time as RFC 3339 UTC, nil for nil.
func timeJSON(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func ptrString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
