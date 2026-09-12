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
	"strings"
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
//
// The legacy route is a thin alias of the event-bundle handler with the source
// PINNED to bil24 (event-bundle spec §2): a body without `source` behaves
// exactly as before, and a body declaring source=arena is refused with
// import.source_mismatch rather than silently taking the arena path.
func (h *Handler) HandleBil24Session(w http.ResponseWriter, r *http.Request) {
	h.handleImport(w, r, bil24compat.SourceBil24)
}

// HandleEventBundle serves POST /v1/organizations/{org_id}/imports/event-bundle
// (event-bundle spec §2) — the same handler with no pinned source, so the body
// must declare one.
func (h *Handler) HandleEventBundle(w http.ResponseWriter, r *http.Request) {
	h.handleImport(w, r, "")
}

// handleImport is the shared implementation of both import routes.
// forcedSource is the source the route pins ("" on the event-bundle route,
// which requires the body to declare it).
func (h *Handler) handleImport(w http.ResponseWriter, r *http.Request, forcedSource string) {
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

	// Step 0 — which identifier regime applies (event-bundle spec §3 / §5).
	source, ok := resolveImportSource(w, r, req, forcedSource)
	if !ok {
		return
	}
	externalRef, ok := resolveExternalRef(w, r, req, source)
	if !ok {
		return
	}

	// Step 1 — identifier ranges. For bil24 every id is required and must stay
	// below the 1e9 compat ceiling; for arena every id is optional but a
	// supplied one must be at or above it.
	if source == bil24compat.SourceArena {
		if err := req.ValidateArenaIDs(); err != nil {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"import.arena_id_out_of_range", err.Error(), r,
			))
			return
		}
	} else if err := req.ValidateExternalIDs(); err != nil {
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
	// The event-bundle additions are syntax-checked against the same fixed
	// zone, for the same reason.
	if _, err := req.ActionEvent.ParseLocalEnd(time.UTC); err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"import.end_time_invalid", err.Error(), r,
		))
		return
	}
	saleStart, err := req.ActionEvent.ParseSellStart()
	if err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"import.invalid_sell_start_time", err.Error(), r,
		))
		return
	}

	plan := importPlan{
		OrgID:           orgID,
		Source:          source,
		ExternalRef:     externalRef,
		Request:         req,
		Currency:        currency,
		SaleWindowStart: saleStart,
		SaleWindowEnd:   saleEnd,
	}

	// An arena bundle carries no Bil24 venue id, so its venue — and therefore
	// its timezone and start instant — can only be found inside the import
	// transaction (see import_arena.go). A Bil24 payload resolves both here,
	// before anything is written, because a wrong timezone guess would
	// silently schedule the session at the wrong moment.
	if source == bil24compat.SourceArena {
		noteArenaIgnoredFields(req, warnings)
	} else {
		loc, tzWarn, tzErr := h.resolveTimezone(ctx, req)
		if tzErr != nil {
			h.writeTimezoneError(w, r, tzErr)
			return
		}
		if tzWarn != "" {
			warnings.add(WarnVenueTimezoneKept, tzWarn)
		}
		startAt, saErr := req.ActionEvent.ParseLocalStart(loc)
		if saErr != nil {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"import.invalid_start_time", saErr.Error(), r,
			))
			return
		}
		endAt, eaErr := req.ActionEvent.ParseLocalEnd(loc)
		if eaErr != nil {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"import.end_time_invalid", eaErr.Error(), r,
			))
			return
		}
		plan.StartAt = startAt.UTC()
		plan.EndAt = endAt
		plan.Timezone = loc.String()
	}

	// Poster side-load happens OUTSIDE the transaction: it is a network call
	// to a third-party host and must never hold row locks open. A failure is
	// downgraded to a warning — a missing poster does not invalidate an
	// otherwise correct catalog import.
	plan.PosterMediaID = h.sideLoadPoster(ctx, orgID, req,
		h.currentPosterMediaID(ctx, orgID, source, externalRef, req), warnings)

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

	exec := h.executeImport
	if source == bil24compat.SourceArena {
		exec = h.executeArenaImport
	}
	result, err := exec(ctx, gen.New(tx), tx, plan, warnings)
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

	// Fires only now — after the commit — and only when applyPublish actually
	// performed the transition this call: a mirror (the Bil24 gateway's wp
	// webhook dispatcher included) must see publish-via-import the same way
	// it sees a manual PATCH .../events/{id} status=published, but never
	// before the row it describes is durable.
	if result.PublishedNow && h.publishCatalogEvent != nil {
		h.publishCatalogEvent(ctx, eventPublishedEventType, result.EventID.String(), orgID.String(),
			[]string{result.SessionID.String()})
	}

	h.writeImportAudit(ctx, r, orgID, req, result)

	var refOut *string
	if externalRef != "" {
		refOut = &externalRef
	}
	httputil.WriteJSON(w, http.StatusOK, ImportSessionResponse{
		EventID:              result.EventID,
		SessionID:            result.SessionID,
		TierIDs:              result.TierIDs,
		SeatingPlanVersionID: result.Seating.PlanVersionID,
		SeatsMaterialized:    result.Seating.SeatsMaterialized,
		Warnings:             warnings.list(),
		Created:              result.Created,
		ExternalRef:          refOut,
		CompatIDs:            result.CompatIDs,
		Publication:          result.Publication,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Source and externalRef (event-bundle spec §3, §5)
// ─────────────────────────────────────────────────────────────────────────────

// resolveImportSource decides which identifier regime the payload runs under
// and answers the request itself when the body disagrees with the route.
//
//   - a `source` value outside {bil24, arena} is always 422
//     import.source_invalid, on either route;
//   - on the event-bundle route (forcedSource == "") a missing source is the
//     same error — the caller must be explicit about which regime it wants;
//   - on the legacy route a missing source keeps meaning bil24 (that is what
//     every existing #517/#518 caller sends), while an explicit source that
//     contradicts the route is 422 import.source_mismatch.
func resolveImportSource(w http.ResponseWriter, r *http.Request, req bil24compat.ImportSessionRequest, forcedSource string) (string, bool) {
	declared := trimSpace(req.Source)
	if declared != "" && !bil24compat.KnownImportSource(declared) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"import.source_invalid", "source must be one of: bil24, arena", r,
		))
		return "", false
	}
	if forcedSource == "" {
		if declared == "" {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"import.source_invalid", "source is required and must be one of: bil24, arena", r,
			))
			return "", false
		}
		return declared, true
	}
	if declared != "" && declared != forcedSource {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"import.source_mismatch",
			"this route only accepts source="+forcedSource+"; use /imports/event-bundle for source="+declared, r,
		))
		return "", false
	}
	return forcedSource, true
}

// resolveExternalRef normalises and validates the idempotency key. It is
// mandatory for source=arena (import.external_ref_required) and optional for
// bil24; in both cases a present-but-blank or over-long value is rejected with
// import.external_ref_invalid, matching the length CHECK on
// session_external_refs (migration 0099).
func resolveExternalRef(w http.ResponseWriter, r *http.Request, req bil24compat.ImportSessionRequest, source string) (string, bool) {
	ref := req.NormalizedExternalRef()
	if ref == "" {
		if req.ExternalRef != "" {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"import.external_ref_invalid", "externalRef must not be blank", r,
			))
			return "", false
		}
		if source == bil24compat.SourceArena {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"import.external_ref_required", "externalRef is required when source=arena", r,
			))
			return "", false
		}
		return "", true
	}
	if len([]rune(ref)) > bil24compat.MaxExternalRefLength {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"import.external_ref_invalid", "externalRef must be at most 200 characters", r,
		))
		return "", false
	}
	return ref, true
}

// noteArenaIgnoredFields records the non-fatal "this field belongs to the
// other source" warnings of spec §3: fee and seating-plan metadata mean
// nothing for an arena-native bundle, and seating itself is out of scope for
// this wave.
func noteArenaIgnoredFields(req bil24compat.ImportSessionRequest, warnings *warningSink) {
	var ignored []string
	if req.ActionEvent.ChargePercent != 0 {
		ignored = append(ignored, "actionEvent.chargePercent")
	}
	if req.ActionEvent.SeatingPlanID != 0 {
		ignored = append(ignored, "actionEvent.seatingPlanId")
	}
	if trimSpace(req.ActionEvent.SeatingPlanName) != "" {
		ignored = append(ignored, "actionEvent.seatingPlanName")
	}
	if len(ignored) > 0 {
		warnings.add(WarnFieldIgnoredForSource,
			"ignored for source=arena: "+strings.Join(ignored, ", "))
	}
	if len(req.SeatList) > 0 || trimSpace(req.SVG) != "" {
		warnings.add(WarnSeatingNotImported,
			"seating is not supported for source=arena in this wave; the session stays general admission")
	}
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
