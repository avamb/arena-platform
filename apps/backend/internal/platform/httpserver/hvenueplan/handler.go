// Package hvenueplan serves the hall plan intake (spec 08_architecture/64,
// wave 1): an organization sends the files of a hall plan, a platform
// administrator builds the plan when no generator can, the organizer checks
// the draft and approves it, and the approved draft becomes a seating plan.
//
// Organization routes (JWT only; an organization API key is refused with 403
// venue_plan_intake.api_key_forbidden, because a source may be an internal
// document of the client):
//
//	POST /v1/organizations/{org_id}/venues/{venue_id}/plan-intakes         seating_plan.create
//	GET  /v1/organizations/{org_id}/plan-intakes                           seating_plan.read
//	GET  /v1/organizations/{org_id}/plan-intakes/{intake_id}               seating_plan.read
//	GET  .../plan-intakes/{intake_id}/sources/{source_id}                  seating_plan.read
//	GET  .../plan-intakes/{intake_id}/drafts/{n}/svg                       seating_plan.read
//	POST .../plan-intakes/{intake_id}/answers|corrections-text|escalate|cancel|approve
//	                                                                       seating_plan.create
//
// Administrator routes (venue_plan_intake.curate and a platform superadmin;
// every write and the source download need X-Admin-Reason):
//
//	GET  /v1/admin/plan-intakes, GET /v1/admin/plan-intakes/{intake_id}
//	GET  .../{intake_id}/sources/{source_id}, GET .../{intake_id}/drafts/{n}/svg
//	POST .../{intake_id}/claim|drafts|return|approve|reject
//
// Every child is read by (id, org_id) or (child id, intake id), so another
// organization's intake is this route's own 404. Every state change goes
// through venueplan.CanTransition under LockVenuePlanIntake, and writes its
// audit row in the same transaction.
package hvenueplan

import (
	"context"
	"encoding/json"
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/intake"
)

// maxJSONBody caps the JSON bodies of the action routes.
const maxJSONBody = 64 << 10

// Handler holds the dependencies of the routes.
type Handler struct {
	pool    *pgxpool.Pool
	queries *gen.Queries
	media   *mediastore.Repo
	audit   audit.Writer
	logger  *slog.Logger
	now     func() time.Time

	// notifier is read per request (the server may set it after mounting).
	notifier func() venueplan.Notifier
	// renderer and interpreter are the seams of the later waves; nil in wave 1.
	renderer    venueplan.Renderer
	interpreter venueplan.TextInterpreter
}

// New builds a Handler. A nil pool or media store answers 503.
func New(pool *pgxpool.Pool, media *mediastore.Repo, auditWriter audit.Writer, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{pool: pool, media: media, audit: auditWriter, logger: logger, now: time.Now}
	if pool != nil {
		h.queries = gen.New(pool)
	}
	return h
}

// WithNotifier sets the notification seam; get is called per request and may
// answer nil (no notifications).
func (h *Handler) WithNotifier(get func() venueplan.Notifier) *Handler {
	h.notifier = get
	return h
}

// WithRenderer sets the renderer seam (wave 2).
func (h *Handler) WithRenderer(r venueplan.Renderer) *Handler {
	h.renderer = r
	return h
}

// WithTextInterpreter sets the text-correction seam (spec 4.10).
func (h *Handler) WithTextInterpreter(t venueplan.TextInterpreter) *Handler {
	h.interpreter = t
	return h
}

func (h *Handler) notify() venueplan.Notifier {
	if h.notifier != nil {
		if n := h.notifier(); n != nil {
			return n
		}
	}
	return venueplan.NopNotifier{}
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
}

func writeErrDetails(w http.ResponseWriter, r *http.Request, status int, code, msg string, details map[string]any) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelopeWithDetails(code, msg, r, details))
}

func (h *Handler) failed(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.Error("venue plan intake: "+what+" failed", slog.String("error", err.Error()))
	writeErr(w, r, http.StatusInternalServerError, "venue_plan_intake.failed",
		"could not complete the request, please try again")
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.pool == nil || h.queries == nil {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable", "database is not available")
		return false
	}
	if h.media == nil {
		writeErr(w, r, http.StatusServiceUnavailable, "media.storage_unavailable", "media storage backend is not configured")
		return false
	}
	return true
}

// refuseAPIKey answers 403 for an organization API key. It runs after the
// child lookup, so a key never learns whether another organization's id
// exists.
func refuseAPIKey(w http.ResponseWriter, r *http.Request) bool {
	if actor, ok := auth.ActorFromContext(r.Context()); ok && actor.IsService() {
		writeErr(w, r, http.StatusForbidden, "venue_plan_intake.api_key_forbidden",
			"hall plan intakes are open to signed-in people only, not to an API key")
		return true
	}
	return false
}

// actorUserID is the acting user's id, nil when it is not a UUID.
func actorUserID(ctx context.Context) *uuid.UUID {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok {
		return nil
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil {
		return nil
	}
	return &id
}

// readJSON decodes a JSON body strictly: one object, no unknown members. An
// empty body decodes as {} when allowEmpty.
func readJSON(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
	if err != nil || len(raw) > maxJSONBody {
		writeErr(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_body",
			"the request body is too large or could not be read, please send a JSON object")
		return false
	}
	if strings.TrimSpace(string(raw)) == "" {
		if allowEmpty {
			return true
		}
		writeErr(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_body",
			"the request body is missing, please send a JSON object as the documentation shows")
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		writeErr(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_body",
			"the request body is not valid JSON for this route, please check the field names and types")
		return false
	}
	return true
}

// writeAudit records one change inside its transaction. meta never carries
// file content or the organizer's text.
func (h *Handler) writeAudit(ctx context.Context, tx pgx.Tx, r *http.Request, action string, in gen.VenuePlanIntakeRow, meta map[string]any) error {
	if h.audit == nil {
		return nil
	}
	return h.audit.WriteTx(ctx, tx, h.auditEvent(ctx, r, action, in, meta))
}

// writeAuditNoTx records a read that changes nothing (the administrator's
// source download) outside any transaction.
func (h *Handler) writeAuditNoTx(ctx context.Context, r *http.Request, action string, in gen.VenuePlanIntakeRow, meta map[string]any) error {
	if h.audit == nil {
		return nil
	}
	return h.audit.Write(ctx, h.auditEvent(ctx, r, action, in, meta))
}

func (h *Handler) auditEvent(ctx context.Context, r *http.Request, action string, in gen.VenuePlanIntakeRow, meta map[string]any) audit.Event {
	actor, _ := auth.ActorFromContext(ctx)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["org_id"] = in.OrgID.String()
	meta["venue_id"] = in.VenueID.String()
	return audit.Event{
		OccurredAt:   h.now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "venue_plan_intake",
		ResourceID:   in.ID.String(),
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     meta,
	}
}

// moveState is intake.MoveState, the one guarded state move shared with the
// worker. ok=false: the caller answers 409 venue_plan_intake.wrong_state.
var moveState = intake.MoveState

func wrongState(w http.ResponseWriter, r *http.Request, state string) {
	writeErrDetails(w, r, http.StatusConflict, "venue_plan_intake.wrong_state",
		"the intake cannot do this in its current state, please reload it", map[string]any{"state": state})
}

func draftStale(w http.ResponseWriter, r *http.Request, current *int32) {
	details := map[string]any{"current_draft": nil}
	if current != nil {
		details["current_draft"] = *current
	}
	writeErrDetails(w, r, http.StatusConflict, "venue_plan_intake.draft_stale",
		"this is not the current draft of the intake, please reload it", details)
}

func strPtr(s string) *string { return &s }

func reasonPtr(r venueplan.EscalationReason) *string { return strPtr(string(r)) }
