// bil24_session.go implements POST /v1/organizations/{org_id}/imports/bil24-session
// (feature #517, W1-C3c; spec §13.2) — the HTTP layer: decoding, payload
// validation, the pre-transaction poster side-load, transaction management
// and error mapping. The upsert algorithm itself lives in import_exec.go.
package himports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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
// import.source_mismatch rather than silently taking the arena path. It has
// no dry run: a `dry_run` query parameter is ignored there.
func (h *Handler) HandleBil24Session(w http.ResponseWriter, r *http.Request) {
	h.handleImport(w, r, bil24compat.SourceBil24, importMode{})
}

// HandleEventBundle serves POST /v1/organizations/{org_id}/imports/event-bundle
// (event-bundle spec §2) — the same handler with no pinned source, so the body
// must declare one. `?dry_run=true` checks the bundle with the same code and
// writes nothing (task 3-1, see dry_run.go).
func (h *Handler) HandleEventBundle(w http.ResponseWriter, r *http.Request) {
	mode, ok := parseImportMode(w, r)
	if !ok {
		return
	}
	h.handleImport(w, r, "", mode)
}

// handleImport is the shared implementation of both import routes.
// forcedSource is the source the route pins ("" on the event-bundle route,
// which requires the body to declare it). mode.DryRun answers with the dry-run
// report instead of importing.
func (h *Handler) handleImport(w http.ResponseWriter, r *http.Request, forcedSource string, mode importMode) {
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

	warnings := newWarningSink()
	issues := newIssueSink(mode)
	prep := h.prepareImport(ctx, orgID, req, forcedSource, mode, warnings, issues)
	if mode.DryRun {
		h.runDryImport(w, r, prep, warnings, issues)
		return
	}
	if err := issues.first(); err != nil {
		h.writeFirstImportError(w, r, err)
		return
	}
	plan := prep.plan

	// Poster side-load happens OUTSIDE the transaction: it is a network call
	// to a third-party host and must never hold row locks open. A failure is
	// downgraded to a warning — a missing poster does not invalidate an
	// otherwise correct catalog import.
	plan.PosterMediaID = h.sideLoadPoster(ctx, orgID, req,
		h.currentPosterMediaID(ctx, orgID, plan.Source, plan.ExternalRef, req), warnings)

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

	result, err := h.executorFor(plan.Source)(ctx, gen.New(tx), tx, plan, warnings)
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
	if plan.ExternalRef != "" {
		ref := plan.ExternalRef
		refOut = &ref
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
		Publications:         nonNilPublications(result.Publications),
	})
}

// importExecutor is the transactional half of an import (import_exec.go for
// source=bil24, import_arena.go for source=arena).
type importExecutor func(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, warnings *warningSink) (importResult, error)

func (h *Handler) executorFor(source string) importExecutor {
	if source == bil24compat.SourceArena {
		return h.executeArenaImport
	}
	return h.executeImport
}

// writeFirstImportError answers the first error of the pre-transaction
// ladder. A failed venue timezone LOOKUP keeps its own 500 wording.
func (h *Handler) writeFirstImportError(w http.ResponseWriter, r *http.Request, err error) {
	var tz *timezoneLookupError
	if errors.As(err, &tz) {
		h.writeTimezoneError(w, r, tz.err)
		return
	}
	h.writeImportError(w, r, err)
}

// timezoneLookupError marks an infrastructure failure of resolveTimezone, so
// it is answered by writeTimezoneError exactly as before.
type timezoneLookupError struct{ err error }

func (e *timezoneLookupError) Error() string { return e.err.Error() }
func (e *timezoneLookupError) Unwrap() error { return e.err }

// preparedImport is what the pre-transaction ladder resolved.
type preparedImport struct {
	plan importPlan
	// sourceOK reports that the identifier regime is known; nothing past
	// the source check means anything without it.
	sourceOK bool
	// startOK reports that actionEvent.day/time parse, so a start instant
	// can be computed once the venue zone is known.
	startOK bool
	// loc is the venue zone a source=bil24 payload resolved before the
	// transaction; nil for source=arena (resolved inside it) or on error.
	loc *time.Location
}

// prepareImport runs every check that needs no import transaction, in the
// order the import has always run them. Each failure goes to issues: in an
// import the first one stops the ladder (issues.add answers true), in a dry
// run the ladder carries on and collects them all. Only an unknown source and
// an infrastructure fault stop a dry run.
func (h *Handler) prepareImport(
	ctx context.Context,
	orgID uuid.UUID,
	req bil24compat.ImportSessionRequest,
	forcedSource string,
	mode importMode,
	warnings *warningSink,
	issues *issueSink,
) preparedImport {
	prep := preparedImport{plan: importPlan{OrgID: orgID, Request: req, Mode: mode}}

	// Step 0 — which identifier regime applies (event-bundle spec §3 / §5).
	source, err := resolveImportSource(req, forcedSource)
	if err != nil {
		issues.add(err)
		return prep
	}
	prep.sourceOK = true
	prep.plan.Source = source
	externalRef, err := resolveExternalRef(req, source)
	if err != nil && issues.add(err) {
		return prep
	}
	prep.plan.ExternalRef = externalRef

	// Step 1 — identifier ranges. For bil24 every id is required and must stay
	// below the 1e9 compat ceiling; for arena every id is optional but a
	// supplied one must be at or above it.
	if source == bil24compat.SourceArena {
		for _, v := range req.ArenaIDViolations() {
			if issues.add(failImportAt(http.StatusUnprocessableEntity, "import.arena_id_out_of_range", v.Pointer, v.Error())) {
				return prep
			}
		}
	} else {
		for _, v := range req.ExternalIDViolations() {
			if issues.add(failImportAt(http.StatusUnprocessableEntity, "compat.external_id_out_of_range", v.Pointer, v.Error())) {
				return prep
			}
		}
	}
	if len(req.CategoryList) == 0 && issues.add(failImportAt(http.StatusUnprocessableEntity,
		"import.categories_required", "/categoryList", "categoryList must contain at least one category")) {
		return prep
	}
	if req.Action.Name() == "" && issues.add(failImportAt(http.StatusUnprocessableEntity,
		"import.action_name_required", "/action/actionName", "action.actionName or action.fullActionName is required")) {
		return prep
	}
	currency, err := normalizeCurrency(req.ActionEvent.Currency)
	if err != nil && issues.add(failImportAt(http.StatusUnprocessableEntity,
		"import.invalid_currency", "/actionEvent/currency", err.Error())) {
		return prep
	}
	prep.plan.Currency = currency

	// The day/time literals are validated against a fixed zone FIRST: their
	// syntax does not depend on the venue timezone, and rejecting a malformed
	// payload before touching the database keeps a broken caller from costing
	// a round-trip per request.
	_, startErr := req.ActionEvent.ParseLocalStart(time.UTC)
	if startErr != nil && issues.add(failImportAt(http.StatusUnprocessableEntity,
		"import.invalid_start_time", startField(req.ActionEvent), startErr.Error())) {
		return prep
	}
	prep.startOK = startErr == nil
	saleEnd, sellEndErr := req.ActionEvent.ParseSellEnd()
	if sellEndErr != nil && issues.add(failImportAt(http.StatusUnprocessableEntity,
		"import.invalid_sell_end_time", "/actionEvent/sellEndTime", sellEndErr.Error())) {
		return prep
	}
	// The event-bundle additions are syntax-checked against the same fixed
	// zone, for the same reason. An end time is only meaningful after a
	// valid start (a dry run reaches here with a broken one).
	if prep.startOK {
		if _, err := req.ActionEvent.ParseLocalEnd(time.UTC); err != nil && issues.add(failImportAt(
			http.StatusUnprocessableEntity, "import.end_time_invalid", "/actionEvent/endTime", err.Error())) {
			return prep
		}
	}
	saleStart, err := parseSellStart(req.ActionEvent, sellEndErr)
	if err != nil && issues.add(failImportAt(http.StatusUnprocessableEntity,
		"import.invalid_sell_start_time", "/actionEvent/sellStartTime", err.Error())) {
		return prep
	}
	prep.plan.SaleWindowStart = saleStart
	prep.plan.SaleWindowEnd = saleEnd

	// A dry run also checks, up front, every field the import only reaches
	// inside its transaction — with the same helpers the executors call.
	if mode.DryRun {
		checkBundleFields(prep.plan, issues)
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
			var ie *importError
			if !errors.As(tzErr, &ie) {
				tzErr = &timezoneLookupError{err: tzErr}
			}
			if issues.add(tzErr) {
				return prep
			}
		} else {
			if tzWarn != "" {
				warnings.add(WarnVenueTimezoneKept, tzWarn)
			}
			prep.loc = loc
			prep.plan.Timezone = loc.String()
			if prep.startOK {
				startAt, saErr := req.ActionEvent.ParseLocalStart(loc)
				if saErr != nil && issues.add(failImportAt(http.StatusUnprocessableEntity,
					"import.invalid_start_time", startField(req.ActionEvent), saErr.Error())) {
					return prep
				}
				endAt, eaErr := req.ActionEvent.ParseLocalEnd(loc)
				if eaErr != nil && issues.add(failImportAt(http.StatusUnprocessableEntity,
					"import.end_time_invalid", "/actionEvent/endTime", eaErr.Error())) {
					return prep
				}
				prep.plan.StartAt = startAt.UTC()
				prep.plan.EndAt = endAt
			}
		}
	}

	// The request's channelIds are checked against the organization BEFORE
	// anything is written: a foreign or unknown channel is a caller error
	// (422 import.invalid_channel), not a reason to lose the import.
	channelIDs, chErrs := h.checkChannelIDs(ctx, orgID, req, mode.DryRun)
	for _, chErr := range chErrs {
		if issues.add(chErr) {
			return prep
		}
	}
	prep.plan.ChannelIDs = channelIDs
	return prep
}

// parseImportMode reads the event-bundle route's `dry_run` query parameter:
// absent or false imports, true checks only. Anything else is 400.
func parseImportMode(w http.ResponseWriter, r *http.Request) (importMode, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("dry_run"))
	if raw == "" {
		return importMode{}, true
	}
	dry, err := strconv.ParseBool(raw)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"import.invalid_dry_run", "dry_run must be true or false", r,
		))
		return importMode{}, false
	}
	return importMode{DryRun: dry}, true
}

// startField is the JSON pointer an actionEvent day/time error belongs to:
// the day when it is missing or malformed, else the time.
func startField(e bil24compat.ImportSessionActionEvent) string {
	day := strings.TrimSpace(e.Day)
	if day == "" {
		return "/actionEvent/day"
	}
	// allow:timeformat: legacy Bil24 wire format, not RFC3339.
	if _, err := time.Parse("02.01.2006", day); err != nil {
		return "/actionEvent/day"
	}
	return "/actionEvent/time"
}

// parseSellStart is ParseSellStart, except that a broken sellEndTime (already
// reported on its own field, which only a dry run gets past) is not reported a
// second time against sellStartTime: only the start's own syntax is checked.
func parseSellStart(e bil24compat.ImportSessionActionEvent, sellEndErr error) (*time.Time, error) {
	if sellEndErr == nil {
		return e.ParseSellStart()
	}
	raw := strings.TrimSpace(e.SellStartTime)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("actionEvent.sellStartTime %q: %w", raw, err)
	}
	utc := t.UTC()
	return &utc, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Source and externalRef (event-bundle spec §3, §5)
// ─────────────────────────────────────────────────────────────────────────────

// resolveImportSource decides which identifier regime the payload runs under
// and refuses the body when it disagrees with the route.
//
//   - a `source` value outside {bil24, arena} is always 422
//     import.source_invalid, on either route;
//   - on the event-bundle route (forcedSource == "") a missing source is the
//     same error — the caller must be explicit about which regime it wants;
//   - on the legacy route a missing source keeps meaning bil24 (that is what
//     every existing #517/#518 caller sends), while an explicit source that
//     contradicts the route is 422 import.source_mismatch.
func resolveImportSource(req bil24compat.ImportSessionRequest, forcedSource string) (string, error) {
	declared := trimSpace(req.Source)
	if declared != "" && !bil24compat.KnownImportSource(declared) {
		return "", failImportAt(http.StatusUnprocessableEntity, "import.source_invalid", "/source",
			"source must be one of: bil24, arena")
	}
	if forcedSource == "" {
		if declared == "" {
			return "", failImportAt(http.StatusUnprocessableEntity, "import.source_invalid", "/source",
				"source is required and must be one of: bil24, arena")
		}
		return declared, nil
	}
	if declared != "" && declared != forcedSource {
		return "", failImportAt(http.StatusUnprocessableEntity, "import.source_mismatch", "/source",
			"this route only accepts source="+forcedSource+"; use /imports/event-bundle for source="+declared)
	}
	return forcedSource, nil
}

// resolveExternalRef normalises and validates the idempotency key. It is
// mandatory for source=arena (import.external_ref_required) unless the
// bundle addresses an existing session by actionEvent.arenaSessionId (the
// session then keeps the key it was created under), and optional for
// bil24; in both cases a present-but-blank or over-long value is rejected with
// import.external_ref_invalid, matching the length CHECK on
// session_external_refs (migration 0099).
func resolveExternalRef(req bil24compat.ImportSessionRequest, source string) (string, error) {
	ref := req.NormalizedExternalRef()
	if ref == "" {
		if req.ExternalRef != "" {
			return "", failImportAt(http.StatusUnprocessableEntity, "import.external_ref_invalid", "/externalRef",
				"externalRef must not be blank")
		}
		if source == bil24compat.SourceArena && strings.TrimSpace(req.ActionEvent.ArenaSessionID) == "" {
			return "", failImportAt(http.StatusUnprocessableEntity, "import.external_ref_required", "/externalRef",
				"externalRef is required when source=arena")
		}
		return "", nil
	}
	if len([]rune(ref)) > bil24compat.MaxExternalRefLength {
		return "", failImportAt(http.StatusUnprocessableEntity, "import.external_ref_invalid", "/externalRef",
			"externalRef must be at most 200 characters")
	}
	return ref, nil
}

// noteArenaIgnoredFields records the non-fatal "this field belongs to the
// other source" warnings of spec §3: fee and Bil24 seating-plan metadata, the
// sbt svg and the seat list mean nothing for an arena-native bundle, whose
// seats come from actionEvent.arenaSeatingPlanVersionId (task S1b).
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
	// Task S1b: an arena bundle takes its places from a seating plan version
	// of the organization (actionEvent.arenaSeatingPlanVersionId), never from
	// a Bil24 svg or seat list. WarnSeatingNotImported keeps its one Bil24
	// meaning (a seatList without an svg).
	hint := ""
	if trimSpace(req.SVG) != "" {
		ignored = append(ignored, "svg")
	}
	if len(req.SeatList) > 0 {
		ignored = append(ignored, "seatList")
	}
	if trimSpace(req.SVG) != "" || len(req.SeatList) > 0 {
		hint = " (a seated session is created from actionEvent.arenaSeatingPlanVersionId)"
	}
	if len(ignored) > 0 {
		warnings.add(WarnFieldIgnoredForSource,
			"ignored for source=arena: "+strings.Join(ignored, ", ")+hint)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Error mapping
// ─────────────────────────────────────────────────────────────────────────────

// importError carries an HTTP status + machine code out of the transactional
// import so the HTTP layer can map it without inspecting database internals.
//
// field is the RFC 6901 JSON pointer of the bundle field the error is about
// ("/categoryList/2/price"), "" when it concerns the bundle as a whole. Only
// the dry run reports it (errors[].field); the import's error envelope is
// unchanged.
type importError struct {
	status  int
	code    string
	message string
	field   string
}

func (e *importError) Error() string { return e.code + ": " + e.message }

func failImport(status int, code, message string) error {
	return &importError{status: status, code: code, message: message}
}

// failImportAt is failImport with the JSON pointer of the offending field.
func failImportAt(status int, code, field, message string) error {
	return &importError{status: status, code: code, message: message, field: field}
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
					return nil, "", failImportAt(http.StatusUnprocessableEntity, "venue.timezone_required", "/venue/timezone",
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
		return nil, "", failImportAt(http.StatusUnprocessableEntity, "venue.timezone_required", "/venue/timezone",
			"venue.timezone is required (IANA zone name) when the venue is not yet known to arena")
	}
	loc, err := time.LoadLocation(payloadTZ)
	if err != nil {
		return nil, "", failImportAt(http.StatusUnprocessableEntity, "venue.timezone_required", "/venue/timezone",
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
