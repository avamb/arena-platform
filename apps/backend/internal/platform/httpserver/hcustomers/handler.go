// Package hcustomers implements the org-scoped customer read surface
// (feature #482, W1-A4d, spec §12.3):
//
//	GET /v1/organizations/{org_id}/customers?q=       — search / list
//	GET /v1/organizations/{org_id}/customers/{id}     — card
//
// Both routes require the `customer.read` permission and are scoped via
// customer_org_links.org_id = org — a customer never linked to the caller's
// org is invisible, even by direct id lookup. The card additionally masks
// strong identities (email/phone/telegram) unless they have been verified,
// matching the spec §12.3 "identities — strong ones masked unless verified"
// rule.
package hcustomers

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// Handler holds the shared dependencies for the customers read handlers.
type Handler struct {
	queries *gen.Queries
	logger  *slog.Logger
}

// New constructs a Handler. A nil queries is allowed; handlers self-gate
// with a 503 dependency.database_unavailable envelope.
func New(queries *gen.Queries, logger *slog.Logger) *Handler {
	return &Handler{queries: queries, logger: logger}
}

func parsePagination(w http.ResponseWriter, r *http.Request) (limit, offset int32, ok bool) {
	limit = defaultLimit
	offset = 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > maxLimit || n > math.MaxInt32 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"customers.invalid_limit", "limit must be a positive integer up to 200", r))
			return 0, 0, false
		}
		limit = int32(n) // #nosec G109 -- bounded above by maxLimit (200) and math.MaxInt32
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > math.MaxInt32 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"customers.invalid_offset", "offset must be a non-negative integer", r))
			return 0, 0, false
		}
		offset = int32(n) // #nosec G109 -- bounded above by math.MaxInt32
	}
	return limit, offset, true
}

// maskStrongIdentity redacts an unverified strong identity value so a
// caller with customer.read cannot exfiltrate full contact details for a
// customer who never actually consented to sharing them with this org.
// Verified identities are returned in full. Weak identities (device /
// wc_customer / bil24_user) are never masked — they carry no PII on their
// own.
func maskStrongIdentity(kind, value string, verified bool) string {
	if verified {
		return value
	}
	switch kind {
	case "email":
		at := strings.IndexByte(value, '@')
		if at <= 1 {
			return "***"
		}
		return value[:1] + "***" + value[at:]
	case "phone":
		if len(value) <= 4 {
			return "***"
		}
		return "***" + value[len(value)-4:]
	default:
		if len(value) <= 2 {
			return "***"
		}
		return value[:1] + "***" + value[len(value)-1:]
	}
}

func isStrongKind(kind string) bool {
	return kind == "email" || kind == "phone" || kind == "telegram"
}

// customerSummary is the shape returned by both the list and card endpoints
// for the base customer fields.
type customerSummary struct {
	ID          string  `json:"id"`
	SystemID    int64   `json:"system_id"`
	DisplayName *string `json:"display_name"`
	Locale      *string `json:"locale"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func toSummary(c gen.CustomerRow) customerSummary {
	return customerSummary{
		ID:          c.ID.String(),
		SystemID:    c.SystemID,
		DisplayName: c.DisplayName,
		Locale:      c.Locale,
		CreatedAt:   c.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   c.UpdatedAt.Format(time.RFC3339),
	}
}

// HandleList serves GET /v1/organizations/{org_id}/customers?q=.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "customers store not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	limit, offset, ok := parsePagination(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		httputil.WriteJSON(w, http.StatusBadRequest,
			httputil.ErrorEnvelope("customers.invalid_query", "q must be at most 200 characters", r))
		return
	}

	rows, err := h.queries.SearchCustomersByOrg(r.Context(), orgID, q, limit, offset)
	if err != nil {
		h.logger.Error("hcustomers: search failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("customers.internal", "failed to search customers", r))
		return
	}

	out := make([]customerSummary, 0, len(rows))
	for _, c := range rows {
		out = append(out, toSummary(c))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"customers": out,
		"limit":     limit,
		"offset":    offset,
	})
}

// HandleGet serves GET /v1/organizations/{org_id}/customers/{id} — the
// customer card: masked identities, this org's orders, org+platform
// attributes and this org's consents.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "customers store not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	idStr := chi.URLParam(r, "id")
	customerID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	// Scope: the customer must be linked to this org, or it is invisible —
	// even to a direct id lookup (spec §12.3 org-scoping rule).
	if _, err := h.queries.GetCustomerOrgLink(r.Context(), customerID, orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound,
				httputil.ErrorEnvelope("customers.not_found", "customer not found in this organization", r))
			return
		}
		h.logger.Error("hcustomers: org link lookup failed", slog.Any("error", err), slog.String("id", idStr))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("customers.internal", "failed to load customer", r))
		return
	}

	cust, err := h.queries.GetCustomerByID(r.Context(), customerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound,
				httputil.ErrorEnvelope("customers.not_found", "customer not found", r))
			return
		}
		h.logger.Error("hcustomers: get failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("customers.internal", "failed to load customer", r))
		return
	}

	identityRows, err := h.queries.ListCustomerIdentities(r.Context(), customerID)
	if err != nil {
		h.logger.Error("hcustomers: list identities failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("customers.internal", "failed to load identities", r))
		return
	}
	identities := make([]map[string]any, 0, len(identityRows))
	for _, ident := range identityRows {
		verified := ident.VerifiedAt != nil
		value := ident.ValueNormalized
		if isStrongKind(ident.Kind) {
			value = maskStrongIdentity(ident.Kind, ident.ValueNormalized, verified)
		}
		identities = append(identities, map[string]any{
			"kind":     ident.Kind,
			"value":    value,
			"verified": verified,
		})
	}

	orderRows, err := h.queries.ListOrdersByCustomerAndOrg(r.Context(), orgID, customerID, 100, 0)
	if err != nil {
		h.logger.Error("hcustomers: list orders failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("customers.internal", "failed to load orders", r))
		return
	}
	orders := make([]map[string]any, 0, len(orderRows))
	for _, o := range orderRows {
		orders = append(orders, map[string]any{
			"id":         o.ID.String(),
			"system_id":  o.SystemID,
			"status":     o.Status,
			"currency":   o.Currency,
			"total":      o.Total,
			"created_at": o.CreatedAt.Format(time.RFC3339),
		})
	}

	attrRows, err := h.queries.ListCustomerAttributesForOrg(r.Context(), customerID, orgID)
	if err != nil {
		h.logger.Error("hcustomers: list attributes failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("customers.internal", "failed to load attributes", r))
		return
	}
	attributes := make([]map[string]any, 0, len(attrRows))
	for _, a := range attrRows {
		attributes = append(attributes, map[string]any{
			"key":        a.Key,
			"value":      string(a.Value),
			"org_scoped": a.OrgID != nil,
			"source":     a.Source,
		})
	}

	consentRows, err := h.queries.ListCustomerConsentsForOrg(r.Context(), customerID, orgID)
	if err != nil {
		h.logger.Error("hcustomers: list consents failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("customers.internal", "failed to load consents", r))
		return
	}
	consents := make([]map[string]any, 0, len(consentRows))
	for _, c := range consentRows {
		consent := map[string]any{
			"kind":     c.Kind,
			"given_at": c.GivenAt.Format(time.RFC3339),
		}
		if c.WithdrawnAt != nil {
			consent["withdrawn_at"] = c.WithdrawnAt.Format(time.RFC3339)
		} else {
			consent["withdrawn_at"] = nil
		}
		consents = append(consents, consent)
	}

	summary := toSummary(cust)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":           summary.ID,
		"system_id":    summary.SystemID,
		"display_name": summary.DisplayName,
		"locale":       summary.Locale,
		"created_at":   summary.CreatedAt,
		"updated_at":   summary.UpdatedAt,
		"identities":   identities,
		"orders":       orders,
		"attributes":   attributes,
		"consents":     consents,
	})
}
