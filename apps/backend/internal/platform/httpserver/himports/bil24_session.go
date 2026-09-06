// bil24_session.go implements POST /v1/organizations/{org_id}/imports/bil24-session
// (feature #517, W1-C3c; spec §13.2) — the HTTP layer: decoding, payload
// validation, the pre-transaction poster side-load, transaction management
// and error mapping. The upsert algorithm itself lives in import_exec.go.
package himports

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// maxImportBodyBytes bounds the request body. The svg block of a large hall
// plan dominates the payload; 8 MB comfortably fits the biggest Bil24 plans
// observed while keeping a hostile caller from exhausting memory.
const maxImportBodyBytes int64 = 8 << 20

// HandleBil24Session serves POST /v1/organizations/{org_id}/imports/bil24-session.
func (h *Handler) HandleBil24Session(w http.ResponseWriter, r *http.Request) {
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
	if !h.requireOrgMembership(w, r, orgID) {
		return
	}

	var req bil24compat.ImportSessionRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImportBodyBytes))
	// Unknown fields are deliberately TOLERATED: the payload is assembled
	// from raw Bil24 responses by a third-party site plugin, and Bil24 adds
	// fields without notice. Rejecting them would break every operator on an
	// upstream change that arena does not even care about.
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"import.invalid_body", "request body is not valid JSON: "+err.Error(), r,
		))
		return
	}

	// Step 1 — every Bil24 identifier must stay below the 1e9 compat ceiling.
	if err := req.ValidateExternalIDs(); err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"compat.external_id_out_of_range", err.Error(), r,
		))
		return
	}
	if len(req.CategoryList) == 0 {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"import.categories_required", "categoryList must contain at least one category", r,
		))
		return
	}
	if req.Action.Name() == "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"import.action_name_required", "action.actionName or action.fullActionName is required", r,
		))
		return
	}
	currency, err := normalizeCurrency(req.ActionEvent.Currency)
	if err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"import.invalid_currency", err.Error(), r,
		))
		return
	}

	warnings := newWarningSink()
	if req.SVG != "" || len(req.SeatList) > 0 {
		warnings.add(WarnSeatingNotImported,
			"seating plan and seat list are not imported by this endpoint yet; the session was created as general admission")
	}

	// The day/time literals are validated against a fixed zone FIRST: their
	// syntax does not depend on the venue timezone, and rejecting a malformed
	// payload before touching the database keeps a broken caller from costing
	// a round-trip per request.
	if _, err := req.ActionEvent.ParseLocalStart(time.UTC); err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"import.invalid_start_time", err.Error(), r,
		))
		return
	}
	saleEnd, err := req.ActionEvent.ParseSellEnd()
	if err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"import.invalid_sell_end_time", err.Error(), r,
		))
		return
	}

	// The venue timezone must be resolvable BEFORE anything is written: it
	// determines the session start instant, and a wrong guess would silently
	// schedule the session at the wrong moment.
	loc, tzWarn, err := h.resolveTimezone(ctx, req)
	if err != nil {
		h.writeTimezoneError(w, r, err)
		return
	}
	if tzWarn != "" {
		warnings.add(WarnVenueTimezoneKept, tzWarn)
	}
	startAt, err := req.ActionEvent.ParseLocalStart(loc)
	if err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"import.invalid_start_time", err.Error(), r,
		))
		return
	}

	// Poster side-load happens OUTSIDE the transaction: it is a network call
	// to a third-party host and must never hold row locks open. A failure is
	// downgraded to a warning — a missing poster does not invalidate an
	// otherwise correct catalog import.
	posterMediaID := h.sideLoadPoster(ctx, orgID, req, warnings)

	plan := importPlan{
		OrgID:         orgID,
		Request:       req,
		Currency:      currency,
		StartAt:       startAt.UTC(),
		SaleWindowEnd: saleEnd,
		PosterMediaID: posterMediaID,
		Timezone:      loc.String(),
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		h.logger.Error("import: begin tx failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"import.transaction_failed", "failed to start import transaction", r,
		))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	result, err := h.executeImport(ctx, gen.New(tx), tx, plan, warnings)
	if err != nil {
		h.writeImportError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("import: commit failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"import.transaction_failed", "failed to commit import transaction", r,
		))
		return
	}
	committed = true

	h.writeImportAudit(ctx, r, orgID, req, result)

	httputil.WriteJSON(w, http.StatusOK, ImportSessionResponse{
		EventID:              result.EventID,
		SessionID:            result.SessionID,
		TierIDs:              result.TierIDs,
		SeatingPlanVersionID: nil,
		SeatsMaterialized:    0,
		Warnings:             warnings.list(),
		Created:              result.Created,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Error mapping
// ─────────────────────────────────────────────────────────────────────────────

// importError carries an HTTP status + machine code out of the transactional
// import so the HTTP layer can map it without inspecting database internals.
type importError struct {
	status  int
	code    string
	message string
}

func (e *importError) Error() string { return e.code + ": " + e.message }

func failImport(status int, code, message string) error {
	return &importError{status: status, code: code, message: message}
}

// writeImportError maps an executeImport failure onto the error envelope.
// Anything that is not an explicit *importError is an infrastructure fault
// and is logged before answering a generic 500.
func (h *Handler) writeImportError(w http.ResponseWriter, r *http.Request, err error) {
	var ie *importError
	if errors.As(err, &ie) {
		httputil.WriteJSON(w, ie.status, httputil.ErrorEnvelope(ie.code, ie.message, r))
		return
	}
	h.logger.Error("import: bil24 session import failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"import.failed", "failed to import bil24 session", r,
	))
}

// writeTimezoneError answers a resolveTimezone failure. Only an explicit
// *importError is a caller-fixable 422 — a database fault while looking the
// venue up is infrastructure and must NOT be dressed up as
// venue.timezone_required, which would send the operator chasing a payload
// problem that does not exist.
func (h *Handler) writeTimezoneError(w http.ResponseWriter, r *http.Request, err error) {
	var ie *importError
	if errors.As(err, &ie) {
		httputil.WriteJSON(w, ie.status, httputil.ErrorEnvelope(ie.code, ie.message, r))
		return
	}
	h.logger.Error("import: venue timezone lookup failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"import.failed", "failed to resolve the venue timezone", r,
	))
}

// ─────────────────────────────────────────────────────────────────────────────
// Timezone resolution
// ─────────────────────────────────────────────────────────────────────────────

// resolveTimezone determines the location used to interpret the payload's
// local day/time (spec §13.2 step 2 / step 4).
//
// An already-known venue's STORED timezone wins over the payload: changing it
// would silently move every session already scheduled at that venue. When
// they disagree the caller gets a WarnVenueTimezoneKept warning. A venue that
// arena does not know yet MUST carry a timezone in the payload — otherwise
// 422 venue.timezone_required.
func (h *Handler) resolveTimezone(ctx context.Context, req bil24compat.ImportSessionRequest) (*time.Location, string, error) {
	payloadTZ := trimSpace(req.Venue.Timezone)

	existing, err := h.queries.GetVenueByBil24ExternalID(ctx, externalIDString(req.Venue.VenueID))
	switch {
	case err == nil:
		vctx, ctxErr := h.queries.GetVenueImportContext(ctx, existing.ID)
		if ctxErr != nil {
			return nil, "", ctxErr
		}
		storedTZ := ""
		if vctx.Timezone != nil {
			storedTZ = trimSpace(*vctx.Timezone)
		}
		if storedTZ != "" {
			loc, loadErr := time.LoadLocation(storedTZ)
			if loadErr != nil {
				// A corrupt stored zone must not wedge the operator: fall
				// through to the payload value if there is one.
				if payloadTZ == "" {
					return nil, "", failImport(http.StatusUnprocessableEntity, "venue.timezone_required",
						"stored venue timezone "+storedTZ+" is not a known IANA zone and the payload carries no replacement")
				}
				break
			}
			warn := ""
			if payloadTZ != "" && payloadTZ != storedTZ {
				warn = "payload timezone " + payloadTZ + " ignored; venue keeps its stored timezone " + storedTZ
			}
			return loc, warn, nil
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, "", err
	}

	if payloadTZ == "" {
		return nil, "", failImport(http.StatusUnprocessableEntity, "venue.timezone_required",
			"venue.timezone is required (IANA zone name) when the venue is not yet known to arena")
	}
	loc, err := time.LoadLocation(payloadTZ)
	if err != nil {
		return nil, "", failImport(http.StatusUnprocessableEntity, "venue.timezone_required",
			"venue.timezone "+payloadTZ+" is not a known IANA zone name")
	}
	return loc, "", nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Audit
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) writeImportAudit(ctx context.Context, r *http.Request, orgID uuid.UUID, req bil24compat.ImportSessionRequest, result importResult) {
	if h.audit == nil {
		return
	}
	actor, _ := auth.ActorFromContext(ctx)
	action := "v1.import.bil24_session.update"
	if result.Created {
		action = "v1.import.bil24_session.create"
	}
	ev := audit.Event{
		OccurredAt:   h.now(),
		ActorType:    actorType(actor),
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "session",
		ResourceID:   result.SessionID.String(),
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata: map[string]any{
			"org_id":          orgID.String(),
			"event_id":        result.EventID.String(),
			"action_id":       req.Action.ActionID,
			"action_event_id": req.ActionEvent.ActionEventID,
			"venue_id":        req.Venue.VenueID,
			"tier_count":      len(result.TierIDs),
			"created":         result.Created,
			"published":       req.Publish,
		},
	}
	if err := h.audit.Write(ctx, ev); err != nil {
		h.logger.Error("import: audit write failed", slog.String("error", err.Error()))
	}
}

// actorType distinguishes an organization API key (the lampyris-ops plugin,
// spec §13.4) from a human operator so the audit trail stays meaningful.
func actorType(actor auth.Actor) string {
	if actor.Type == auth.ActorTypeService {
		return string(auth.ActorTypeService)
	}
	if actor.Type != "" {
		return string(actor.Type)
	}
	return string(auth.ActorTypeAnon)
}
