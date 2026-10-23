// Package hpromoattempts serves the read side of the promo-code attempt
// journal (migration 0161, internal/platform/promoattempts):
//
//	GET /v1/organizations/{org_id}/promo-codes/{id}/attempts          (promo.read)
//	GET /v1/organizations/{org_id}/promo-codes/{id}/attempts/summary  (promo.read)
//	GET /v1/organizations/{org_id}/promo-code-attempts                (promo.read)
//
// The last route is the organization-wide view: a code nobody has created
// ("RED30" typed by a buyer) has no promo code row, so only this view shows
// it (filter ?code=). A code of another organization is the route's own 404,
// like a missing one; every query is scoped by the path organization.
package hpromoattempts

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/promoattempts"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// Handler holds the dependencies of the routes.
type Handler struct {
	pool    *pgxpool.Pool
	queries *gen.Queries
	logger  *slog.Logger
}

// New builds a Handler. A nil pool answers 503.
func New(pool *pgxpool.Pool, queries *gen.Queries, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{pool: pool, queries: queries, logger: logger}
}

type attemptJSON struct {
	ID               string   `json:"id"`
	CreatedAt        string   `json:"created_at"`
	ChannelID        *string  `json:"channel_id"`
	Surface          string   `json:"surface"`
	CodeEntered      string   `json:"code_entered"`
	PromoCodeID      *string  `json:"promo_code_id"`
	SessionIDs       []string `json:"session_ids"`
	Result           string   `json:"result"`
	Reason           *string  `json:"reason"`
	CustomerID       *string  `json:"customer_id"`
	GatewaySessionID *string  `json:"gateway_session_id"`
	ClientRef        *string  `json:"client_ref"`
}

func strPtr(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

func toJSON(it promoattempts.Item) attemptJSON {
	sess := make([]string, 0, len(it.SessionIDs))
	for _, s := range it.SessionIDs {
		sess = append(sess, s.String())
	}
	return attemptJSON{
		ID: it.ID.String(), CreatedAt: it.CreatedAt.UTC().Format(time.RFC3339),
		ChannelID: strPtr(it.ChannelID), Surface: it.Surface, CodeEntered: it.CodeEntered,
		PromoCodeID: strPtr(it.PromoCodeID), SessionIDs: sess, Result: it.Result,
		Reason: it.Reason, CustomerID: strPtr(it.CustomerID),
		GatewaySessionID: strPtr(it.GatewaySessionID), ClientRef: it.ClientRef,
	}
}

var validResults = map[string]bool{promoattempts.ResultApplied: true, promoattempts.ResultRejected: true}

var validReasons = map[string]bool{
	promoattempts.ReasonUnknownCode: true, promoattempts.ReasonWrongSession: true,
	promoattempts.ReasonWrongTier: true, promoattempts.ReasonOutsideWindow: true,
	promoattempts.ReasonExpired: true, promoattempts.ReasonMaxUses: true,
	promoattempts.ReasonMaxUsesPerCustomer: true, promoattempts.ReasonMinOrder: true,
	promoattempts.ReasonCurrency: true, promoattempts.ReasonPaused: true,
	promoattempts.ReasonOther: true,
}

func bad(w http.ResponseWriter, r *http.Request, code, msg string) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(code, msg, r))
}

// filterFromRequest reads the optional filters. false = the answer is written.
func filterFromRequest(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) (promoattempts.Filter, bool) {
	q := r.URL.Query()
	f := promoattempts.Filter{OrgID: orgID, Code: strings.TrimSpace(q.Get("code"))}
	if f.Result = q.Get("result"); f.Result != "" && !validResults[f.Result] {
		bad(w, r, "promo_attempts.invalid_result", "result must be 'applied' or 'rejected'")
		return f, false
	}
	if f.Reason = q.Get("reason"); f.Reason != "" && !validReasons[f.Reason] {
		bad(w, r, "promo_attempts.invalid_reason", "unknown reason")
		return f, false
	}
	for _, p := range []struct {
		name string
		dst  **time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(p.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				bad(w, r, "promo_attempts.invalid_date", p.name+" must be an RFC 3339 timestamp")
				return f, false
			}
			*p.dst = &t
		}
	}
	return f, true
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.pool == nil || h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return false
	}
	return true
}

// codeInOrg loads the promo code inside the path organization; false = the
// answer (404 or 500) is written.
func (h *Handler) codeInOrg(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) (uuid.UUID, bool) {
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return uuid.Nil, false
	}
	if _, err := h.queries.GetPromoCodeByID(r.Context(), id, orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("promo.not_found", "promo code not found", r))
			return uuid.Nil, false
		}
		h.logger.Error("promo_attempts: code lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"promo_attempts.lookup_failed", "failed to look up promo code", r))
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, f promoattempts.Filter) {
	limit, offset := defaultLimit, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLimit {
			bad(w, r, "promo_attempts.invalid_limit", "limit must be between 1 and 200")
			return
		}
		limit = n
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			bad(w, r, "promo_attempts.invalid_offset", "offset must be 0 or more")
			return
		}
		offset = n
	}
	items, total, err := promoattempts.List(r.Context(), h.pool, f, limit, offset)
	if err != nil {
		h.logger.Error("promo_attempts: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"promo_attempts.list_failed", "failed to list promo code attempts", r))
		return
	}
	out := make([]attemptJSON, 0, len(items))
	for _, it := range items {
		out = append(out, toJSON(it))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"attempts": out, "total": total, "limit": limit, "offset": offset,
		"has_more": int64(offset+len(out)) < total,
	})
}

// HandleListForCode serves GET .../promo-codes/{id}/attempts.
func (h *Handler) HandleListForCode(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	id, ok := h.codeInOrg(w, r, orgID)
	if !ok {
		return
	}
	f, ok := filterFromRequest(w, r, orgID)
	if !ok {
		return
	}
	f.PromoCodeID = id
	h.list(w, r, f)
}

// HandleListForOrg serves GET .../promo-code-attempts.
func (h *Handler) HandleListForOrg(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	f, ok := filterFromRequest(w, r, orgID)
	if !ok {
		return
	}
	if v := r.URL.Query().Get("promo_code_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			bad(w, r, "promo_attempts.invalid_promo_code_id", "promo_code_id must be a UUID")
			return
		}
		f.PromoCodeID = id
	}
	h.list(w, r, f)
}

// HandleSummary serves GET .../promo-codes/{id}/attempts/summary.
func (h *Handler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	id, ok := h.codeInOrg(w, r, orgID)
	if !ok {
		return
	}
	f, ok := filterFromRequest(w, r, orgID)
	if !ok {
		return
	}
	f.PromoCodeID = id
	// The summary counts everything in the window; result/reason filters
	// would make "applied vs rejected" meaningless.
	f.Result, f.Reason = "", ""
	s, err := promoattempts.Summarize(r.Context(), h.pool, f)
	if err != nil {
		h.logger.Error("promo_attempts: summary failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"promo_attempts.summary_failed", "failed to summarize promo code attempts", r))
		return
	}
	reasons := make([]map[string]any, 0, len(s.TopReasons))
	for _, rc := range s.TopReasons {
		reasons = append(reasons, map[string]any{"reason": rc.Reason, "count": rc.Count})
	}
	fmtTime := func(t *time.Time) *string {
		if t == nil {
			return nil
		}
		v := t.UTC().Format(time.RFC3339)
		return &v
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"promo_code_id": id.String(), "total": s.Total, "applied": s.Applied, "rejected": s.Rejected,
		"top_reasons": reasons, "first_attempt_at": fmtTime(s.FirstAttemptAt), "last_attempt_at": fmtTime(s.LastAttemptAt),
	})
}
