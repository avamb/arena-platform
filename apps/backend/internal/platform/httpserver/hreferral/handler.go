// Package hreferral serves the organization's side of "Refer a friend"
// (spec 52, section 7.1):
//
//	GET  /v1/organizations/{org_id}/events/{event_id}/referral-program           (promo.read)
//	PUT  /v1/organizations/{org_id}/events/{event_id}/referral-program           (promo.update + refund.create + refund.approve)
//	POST .../referral-program/pause | resume | end                               (same three)
//	GET  .../referral-program/summary                                            (promo.read)
//	GET  .../referral-rewards                                                    (promo.read)
//
// and the same six for the organization's shop under
// /v1/organizations/{org_id}/shop/referral-program (spec 52 section 3.5; one
// live programme per organization, no event).
//
// Every route loads the event inside the path organization (another
// organization's or an unknown event is the route's own 404 event.not_found)
// and every query is scoped by that organization. Rewards and the summary show
// order NUMBERS and money, never a buyer's e-mail, phone or name. Changing a
// programme's percentages touches NEW codes only: a code already issued keeps
// the discount it promised and a reward keeps the cashback it copied.
package hreferral

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
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	domain "github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

const maxBodyBytes = 4 << 10

// Programme statuses (referral_programs.status).
const (
	StatusActive = "active"
	StatusPaused = "paused"
	StatusEnded  = "ended"
)

// Handler holds the dependencies of the routes.
type Handler struct {
	pool     *pgxpool.Pool
	queries  *gen.Queries
	audit    audit.Writer
	logger   *slog.Logger
	registry *domain.Registry
	now      func() time.Time
}

// New builds a Handler. A nil pool answers 503. A nil audit writer skips the
// audit row (unit tests); production always has one.
func New(pool *pgxpool.Pool, auditWriter audit.Writer, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{pool: pool, audit: auditWriter, logger: logger, registry: paymodules.Registry(), now: time.Now}
	if pool != nil {
		h.queries = gen.New(pool)
	}
	return h
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
}

func (h *Handler) failed(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.Error("referral: "+what+" failed", slog.String("error", err.Error()))
	writeErr(w, r, http.StatusInternalServerError, "referral.failed", "could not complete the request, please try again")
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.pool == nil || h.queries == nil {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable", "database is not available")
		return false
	}
	return true
}

// scope reads {org_id} and {event_id} and checks that the event is a live
// event of that organization; false = the answer is written.
func (h *Handler) scope(w http.ResponseWriter, r *http.Request) (orgID, eventID uuid.UUID, ok bool) {
	if !h.ready(w, r) {
		return uuid.Nil, uuid.Nil, false
	}
	orgID, ok = httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	eventID, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	owner, err := h.queries.GetEventOrgForMaterials(r.Context(), eventID)
	if err != nil || owner != orgID {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			h.failed(w, r, "event lookup", err)
			return uuid.Nil, uuid.Nil, false
		}
		writeErr(w, r, http.StatusNotFound, "event.not_found", "event not found")
		return uuid.Nil, uuid.Nil, false
	}
	return orgID, eventID, true
}

// readBody decodes the JSON body strictly: one object, no unknown members.
func readBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(raw) > maxBodyBytes || strings.TrimSpace(string(raw)) == "" {
		writeErr(w, r, http.StatusBadRequest, "referral.invalid_body",
			"the request body is missing or too large, please send a JSON object as the documentation shows")
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		writeErr(w, r, http.StatusBadRequest, "referral.invalid_body",
			"the request body is not valid JSON for this route, please check the field names and types")
		return false
	}
	return true
}

func fmtTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

// programJSON is a programme as the routes show it.
type programJSON struct {
	ID                string  `json:"id"`
	Scope             string  `json:"scope"`
	EventID           *string `json:"event_id"`
	Status            string  `json:"status"`
	FriendDiscountPct int     `json:"friend_discount_pct"`
	CashbackPct       int     `json:"cashback_pct"`
	CashbackCapPct    int     `json:"cashback_cap_pct"`
	MaxFriends        *int    `json:"max_friends"`
	StartsAt          *string `json:"starts_at"`
	EndsAt            *string `json:"ends_at"`
	TermsAcceptedAt   string  `json:"terms_accepted_at"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

func toProgramJSON(p gen.ReferralProgramRow) programJSON {
	var maxFriends *int
	if p.MaxFriends != nil {
		v := int(*p.MaxFriends)
		maxFriends = &v
	}
	var eventID *string
	if p.EventID != nil {
		v := p.EventID.String()
		eventID = &v
	}
	return programJSON{
		ID: p.ID.String(), Scope: p.Scope, EventID: eventID, Status: p.Status,
		FriendDiscountPct: int(p.FriendDiscountPct), CashbackPct: int(p.CashbackPct), CashbackCapPct: int(p.CashbackCapPct),
		MaxFriends: maxFriends, StartsAt: fmtTime(p.StartsAt), EndsAt: fmtTime(p.EndsAt),
		TermsAcceptedAt: p.TermsAcceptedAt.UTC().Format(time.RFC3339),
		CreatedAt:       p.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: p.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// writeAudit records one change inside its transaction.
func (h *Handler) writeAudit(ctx context.Context, tx pgx.Tx, r *http.Request, action string, p gen.ReferralProgramRow, meta map[string]any) error {
	if h.audit == nil {
		return nil
	}
	actor, _ := auth.ActorFromContext(ctx)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["org_id"] = p.OrgID.String()
	if p.EventID != nil {
		meta["event_id"] = p.EventID.String()
	}
	return h.audit.WriteTx(ctx, tx, audit.Event{
		OccurredAt:   h.now().UTC(),
		ActorType:    "user", // rewritten to api_key by audit.WithServiceActor
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "referral_program",
		ResourceID:   p.ID.String(),
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     meta,
	})
}

// actorLabel is the acting user's id (an API key's id for a key), recorded as
// referral_programs.created_by.
func actorLabel(ctx context.Context) string {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok {
		return ""
	}
	return actor.ID
}

func createdVia(ctx context.Context) string {
	if v, ok := audit.ClientChannelFromContext(ctx); ok {
		return v
	}
	return "api"
}
