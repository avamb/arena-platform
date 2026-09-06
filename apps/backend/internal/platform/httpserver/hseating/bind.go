// bind.go implements the seating-plan → session binding endpoint
// (feature #306, Wave SEAT-B2).
//
//	POST /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/seating
//
// Bind semantics
//
//   - Requires event_session.assign_seating_plan (mounted via applyAuth).
//   - Body carries:
//     seating_plan_version_id  uuid    (required)
//     admission_mode           string  (required: assigned_seats | hybrid)
//     category_tier_map        object  (required: 1-based category index →
//     ticket_tiers.id UUID). Every
//     category in the version geometry
//     MUST appear as a key.
//     auto_create_tiers        bool    (optional, default false). When
//     true, categories missing from the
//     map are provisioned as fresh
//     ticket_tiers rows using the
//     Category.Name / PriceHint import
//     hints (currency always follows the
//     session, AB-38).
//   - First bind materializes one session_seats row per geometry seat under
//     a single transaction, applies the category → tier mapping, locks the
//     seating_plan_versions.locked_at stamp on the very first bind
//     (LockSeatingPlanVersion is idempotent for subsequent binds against
//     the same version), and recomputes sessions.capacity_total from the
//     version's seated/standing capacity (documented capacity-propagation
//     hook in 0016_sessions.sql:58).
//   - Rebind is allowed only when the session has zero reservations AND
//     zero tickets. When either count is non-zero the request is rejected
//     with 409 seating.rebind_forbidden — the fully-consistent way to
//     replan a booked session is to create a new session (spec §7
//     SEAT-B2).
//   - Since AB-51, general_admission sessions may bind a GA-only plan:
//     the bind materializes one session_seats row per GA place
//     (kind='ga_unit', tier fixed to the category tier) — GA places are
//     inventory from setup, not a by-product of purchase. Plan-less GA
//     sessions get their pool units at session create instead and never
//     hit this endpoint. The session-level inventory_ledger row remains
//     the transactional capacity rollup for both.
//
// Wave 4 (AB-36 step 3): the same core flow also runs inline from the
// session CREATE path via BindSeatingForSessionCreate, so a seated session
// can be provisioned in a single call. The core is therefore factored into
// bindSessionSeatingCore, which reports failures as *BindError values
// instead of writing HTTP envelopes directly.
package hseating

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// bindBodyLimit caps the JSON payload for the binding endpoint. A single
// category_tier_map entry is ~50 bytes and even the largest realistic plan
// keeps well under 64 KiB.
const bindBodyLimit = 64 * 1024

// validBindAdmissionModes lists the admission modes the bind endpoint
// accepts. Since AB-51, general_admission sessions may bind a GA-only
// plan — the bind materializes one session_seats row per GA place
// (kind='ga_unit') exactly like seats. Plan-less GA sessions keep
// working without a binding (their pool units are materialized at
// session create).
var validBindAdmissionModes = map[string]bool{
	"assigned_seats":    true,
	"hybrid":            true,
	"general_admission": true,
}

// planTypesForAdmissionMode is the AB-40 B5 cross-check: which parent
// plan types may be bound under each admission mode. assigned_seats
// requires a pure seated plan; hybrid requires a combined (mixed) plan
// that actually carries GA capacity; general_admission requires a
// GA-only plan.
var planTypesForAdmissionMode = map[string]map[string]bool{
	"assigned_seats":    {"assigned_seats": true},
	"hybrid":            {"mixed": true},
	"general_admission": {"general_admission": true},
}

// BindError is the transport shape the bind core uses to report a failure:
// the HTTP status plus the error-envelope code/message/details the caller
// should emit. It exists so the same core serves both the standalone bind
// endpoint and the inline session-create bind (AB-36) with identical error
// surfaces.
type BindError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

// write emits the error as a standard envelope.
func (e *BindError) write(w http.ResponseWriter, r *http.Request) {
	if e.Details == nil {
		httputil.WriteJSON(w, e.Status, httputil.ErrorEnvelope(e.Code, e.Message, r))
		return
	}
	httputil.WriteJSON(w, e.Status, httputil.ErrorEnvelopeWithDetails(e.Code, e.Message, r, e.Details))
}

// bindErr is a convenience constructor.
func bindErr(status int, code, message string, details map[string]any) *BindError {
	return &BindError{Status: status, Code: code, Message: message, Details: details}
}

// bindRequest is the strict-decoded shape of the request body. Every field
// is required except AutoCreateTiers.
type bindRequest struct {
	SeatingPlanVersionID string             `json:"seating_plan_version_id"`
	AdmissionMode        string             `json:"admission_mode"`
	CategoryTierMap      map[string]*string `json:"category_tier_map"`
	AutoCreateTiers      bool               `json:"auto_create_tiers"`
}

// bindResponse is the JSON envelope returned on success. It echoes the
// updated session binding + the (possibly newly-locked) plan version + a
// summary of the materialization step so tests and admin UIs can pin
// invariants without re-issuing the ListSessionSeats call.
type bindResponse struct {
	Session         SessionSeatingBindingResponse `json:"session"`
	Version         SeatingPlanVersionResponse    `json:"seating_plan_version"`
	Materialized    int                           `json:"materialized_seats"`
	CategoryTierMap map[string]string             `json:"category_tier_map"`
	CreatedTierIDs  []string                      `json:"created_tier_ids,omitempty"`
	Rebound         bool                          `json:"rebound"`
}

// SessionSeatingBindingResponse is the JSON shape of the session projection
// returned by the bind endpoint.
type SessionSeatingBindingResponse struct {
	ID                   string  `json:"id"`
	EventID              string  `json:"event_id"`
	AdmissionMode        string  `json:"admission_mode"`
	SeatingPlanVersionID *string `json:"seating_plan_version_id"`
	SeatStatusVersion    int64   `json:"seat_status_version"`
	CapacityTotal        int32   `json:"capacity_total"`
}

// sessionBindingFromRow projects the gen row into the response shape.
func sessionBindingFromRow(r gen.SessionSeatingBindingRow) SessionSeatingBindingResponse {
	out := SessionSeatingBindingResponse{
		ID:                r.ID.String(),
		EventID:           r.EventID.String(),
		AdmissionMode:     r.AdmissionMode,
		SeatStatusVersion: r.SeatStatusVersion,
		CapacityTotal:     r.CapacityTotal,
	}
	if r.SeatingPlanVersionID != nil {
		s := r.SeatingPlanVersionID.String()
		out.SeatingPlanVersionID = &s
	}
	return out
}

// bindCoreResult carries the successful outcome of the bind core back to the
// HTTP layer (or to the session-create path, which only needs Session).
type bindCoreResult struct {
	Session         gen.SessionSeatingBindingRow
	Version         gen.SeatingPlanVersionRow
	Materialized    int
	CategoryTierMap map[string]string
	CreatedTierIDs  []string
	Rebound         bool
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleBindSessionSeating
// ─────────────────────────────────────────────────────────────────────────────

// HandleBindSessionSeating serves POST
// /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/seating. See
// the file-level docstring for the full contract.
func (h *Handler) HandleBindSessionSeating(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	if _, ok := httputil.UUIDPathParam(w, r, "org_id"); !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	req, ok := readBindRequest(w, r)
	if !ok {
		return
	}
	planVersionID, ok := parseBindRequest(w, r, req)
	if !ok {
		return
	}

	result, bErr := h.bindSessionSeatingCore(ctx, r, eventID, sessionID, planVersionID, req)
	if bErr != nil {
		bErr.write(w, r)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, bindResponse{
		Session:         sessionBindingFromRow(result.Session),
		Version:         SeatingPlanVersionFromRow(result.Version),
		Materialized:    result.Materialized,
		CategoryTierMap: result.CategoryTierMap,
		CreatedTierIDs:  result.CreatedTierIDs,
		Rebound:         result.Rebound,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// BindSeatingForSessionCreate — inline bind for the session-create path
// ─────────────────────────────────────────────────────────────────────────────

// BindSeatingForSessionCreate runs the SEAT-B2 bind for a session that was
// created moments ago by the hcatalog create handler (AB-36 step 3). Tiers
// are auto-created from the SVG category legend (one tier per referenced
// price category — Bil24's category == tier model); the session's currency
// governs the created tiers via autoCreateTier. Returns nil on success or
// the same *BindError the standalone endpoint would have emitted.
func (h *Handler) BindSeatingForSessionCreate(
	ctx context.Context,
	r *http.Request,
	eventID, sessionID, planVersionID uuid.UUID,
	admissionMode string,
) *BindError {
	if h.queries == nil || h.pool == nil {
		return bindErr(http.StatusServiceUnavailable,
			"dependency.database_unavailable", "database is not available", nil)
	}
	if !validBindAdmissionModes[admissionMode] {
		return bindErr(http.StatusBadRequest,
			"seating.invalid_admission_mode",
			"admission_mode must be one of assigned_seats|hybrid|general_admission",
			map[string]any{"field": "admission_mode"})
	}
	req := bindRequest{
		AdmissionMode:   admissionMode,
		CategoryTierMap: map[string]*string{},
		AutoCreateTiers: true,
	}
	_, bErr := h.bindSessionSeatingCore(ctx, r, eventID, sessionID, planVersionID, req)
	return bErr
}

// ─────────────────────────────────────────────────────────────────────────────
// Core bind flow (shared by the endpoint and the session-create path)
// ─────────────────────────────────────────────────────────────────────────────

// bindSessionSeatingCore runs the full bind transaction: session row lock,
// rebind guardrail, geometry canonicalization, category → tier resolution,
// seat materialization, version lock, capacity recompute, audit, commit.
// The *http.Request is used only for audit metadata (client IP).
func (h *Handler) bindSessionSeatingCore(
	ctx context.Context,
	r *http.Request,
	eventID, sessionID, planVersionID uuid.UUID,
	req bindRequest,
) (*bindCoreResult, *BindError) {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, bindErr(http.StatusServiceUnavailable,
			"dependency.database_unavailable", "failed to begin transaction", nil)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.queries.WithTx(tx)

	// Load the session binding scoped by (id, event_id) and take a row
	// lock on the sessions row for the remainder of the transaction. The
	// seated-reservation path locks the same row (via its
	// IncrementSessionSeatStatusVersion UPDATE), so the FOR UPDATE here
	// serializes binds against in-flight seat holds and closes the TOCTOU
	// window between the rebind zero-reservations check below and the
	// session_seats wipe. The projection also returns the current
	// admission_mode + seating_plan_version_id which drives the rebind
	// guardrail.
	binding, err := qtx.GetSessionSeatingBindingForUpdate(ctx, sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, bindErr(http.StatusNotFound,
				"session.not_found", "session not found", nil)
		}
		h.logger.Error("seating: bind session lookup failed", slog.String("error", err.Error()))
		return nil, bindErr(http.StatusInternalServerError,
			"seating.bind_failed", "failed to bind seating plan", nil)
	}

	// Load the seating plan version + its parent plan so the geometry can
	// be canonicalized in-memory. A missing version is a client error, not
	// a server error, because plan_version_id is caller-supplied.
	version, err := qtx.GetSeatingPlanVersionByID(ctx, planVersionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, bindErr(http.StatusBadRequest,
				"seating.version_not_found",
				"seating_plan_version_id does not exist",
				map[string]any{"field": "seating_plan_version_id"})
		}
		h.logger.Error("seating: bind version lookup failed", slog.String("error", err.Error()))
		return nil, bindErr(http.StatusInternalServerError,
			"seating.bind_failed", "failed to bind seating plan", nil)
	}

	// AB-40 B5: cross-check the parent plan's type against the requested
	// admission mode. Before this gate an assigned_seats plan could be
	// bound as hybrid, silently promising GA capacity the plan does not
	// have. assigned_seats mode needs a pure seated plan; hybrid needs a
	// combined (mixed) one.
	parentPlan, err := qtx.GetSeatingPlanByID(ctx, version.SeatingPlanID)
	if err != nil {
		h.logger.Error("seating: bind parent plan lookup failed", slog.String("error", err.Error()))
		return nil, bindErr(http.StatusInternalServerError,
			"seating.bind_failed", "failed to bind seating plan", nil)
	}
	if allowed, ok := planTypesForAdmissionMode[req.AdmissionMode]; ok && !allowed[parentPlan.PlanType] {
		return nil, bindErr(http.StatusBadRequest,
			"seating.plan_type_mismatch",
			fmt.Sprintf("a %s plan cannot be bound with admission_mode %q",
				parentPlan.PlanType, req.AdmissionMode),
			map[string]any{
				"plan_type":      parentPlan.PlanType,
				"admission_mode": req.AdmissionMode,
			})
	}

	// Rebind guardrail: any historical reservation or ticket on the session
	// blocks a re-bind (§7 SEAT-B2). GA sessions have no plan binding to
	// begin with — this handler is not the general-admission code path.
	rebound := binding.SeatingPlanVersionID != nil
	if rebound {
		// Only enforce the guardrail when actually rebinding (not on the
		// first bind, where no prior state exists).
		resCount, err := qtx.CountReservationsBySession(ctx, sessionID)
		if err != nil {
			h.logger.Error("seating: bind reservation count failed", slog.String("error", err.Error()))
			return nil, bindErr(http.StatusInternalServerError,
				"seating.bind_failed", "failed to bind seating plan", nil)
		}
		tktCount, err := qtx.CountTicketsBySession(ctx, sessionID)
		if err != nil {
			h.logger.Error("seating: bind ticket count failed", slog.String("error", err.Error()))
			return nil, bindErr(http.StatusInternalServerError,
				"seating.bind_failed", "failed to bind seating plan", nil)
		}
		if resCount > 0 || tktCount > 0 {
			return nil, bindErr(http.StatusConflict,
				"seating.rebind_forbidden",
				"session already has reservations or tickets; create a new session to change the seating plan",
				map[string]any{
					"reservations": resCount,
					"tickets":      tktCount,
				})
		}
	}

	// Decode the canonical geometry. seating.Canonicalize is idempotent so
	// re-applying it here is safe and defends against any downstream drift
	// in what the DB returned (e.g. a hand-edited row).
	var geometry seating.Geometry
	if err := json.Unmarshal(version.Geometry, &geometry); err != nil {
		h.logger.Error("seating: bind geometry decode failed", slog.String("error", err.Error()))
		return nil, bindErr(http.StatusInternalServerError,
			"seating.bind_failed", "seating plan version geometry is not valid", nil)
	}
	geometry = seating.Canonicalize(geometry)

	// Validate and materialize the category → tier map. On auto_create_tiers
	// = true, missing categories are provisioned as fresh ticket_tiers rows
	// bound to the target session; the created ids are returned to the
	// caller so the follow-up admin UI can hydrate them without an extra
	// GET.
	resolvedMap, createdTierIDs, bErr := resolveCategoryTierMap(
		ctx, qtx, sessionID, geometry, req,
	)
	if bErr != nil {
		return nil, bErr
	}

	// Rebind: wipe any previously materialized seats for this session so the
	// new bind starts from a clean slate. session_seats has (session_id,
	// seat_key) UNIQUE — leaving stale rows around risks silent capacity
	// drift on the assigned_seats/hybrid inventory paths.
	if rebound {
		if _, err := qtx.DeleteReservationSeatsBySession(ctx, sessionID); err != nil {
			h.logger.Error("seating: bind reservation_seats wipe failed", slog.String("error", err.Error()))
			return nil, bindErr(http.StatusInternalServerError,
				"seating.bind_failed", "failed to prepare session for rebind", nil)
		}
		if _, err := qtx.DeleteSessionSeatsBySession(ctx, sessionID); err != nil {
			h.logger.Error("seating: bind session_seats wipe failed", slog.String("error", err.Error()))
			return nil, bindErr(http.StatusInternalServerError,
				"seating.bind_failed", "failed to prepare session for rebind", nil)
		}
	}

	// Materialize one session_seats row per geometry seat under the same
	// transaction, batched into a single multi-row INSERT (unnest arrays)
	// so a large plan costs one round-trip instead of one per seat.
	// seat_key is copied verbatim from the geometry (§5.3 stable
	// identifier); tier_id comes from the resolved map.
	//
	// W1-C3b (spec §3.1 / §13.2 step 6): when the geometry carries
	// upstream seat identities (Seat.ExternalID — the Bil24 seatId parsed
	// by seating.ImportSBTSVG), they are written verbatim into
	// session_seats.system_seat_id with system_seat_id_source='bil24'.
	// Because the geometry is immutable once locked, re-running the bind
	// re-derives exactly the same integers: seat ids survive a rebind,
	// which is what GET_SEAT_LIST / GET_SCHEMA / the sbt-SVG image put on
	// the wire. Seats without an external id keep falling back to the
	// arena sequence.
	seatCount := geometry.SeatCount()
	seatKeys := make([]string, 0, seatCount)
	sectorNames := make([]string, 0, seatCount)
	rowNames := make([]string, 0, seatCount)
	seatNumbers := make([]string, 0, seatCount)
	tierIDs := make([]*string, 0, seatCount)
	systemSeatIDs := make([]*int64, 0, seatCount)
	var maxExternalID int64
	for _, section := range geometry.Sections {
		for _, row := range section.Rows {
			for _, seat := range row.Seats {
				tierID, hasTier := resolvedMap[seat.CategoryIndex]
				if !hasTier {
					// resolveCategoryTierMap guarantees every category
					// referenced by a seat has an entry; a missing key
					// here would be a canonicaliser bug, not user error.
					return nil, bindErr(http.StatusInternalServerError,
						"seating.bind_failed",
						fmt.Sprintf("seat %q references unknown category_index %d",
							seat.Key, seat.CategoryIndex),
						nil)
				}
				tierStr := tierID.String()
				seatKeys = append(seatKeys, seat.Key)
				sectorNames = append(sectorNames, section.Name)
				rowNames = append(rowNames, row.Name)
				seatNumbers = append(seatNumbers, seat.Number)
				tierIDs = append(tierIDs, &tierStr)
				if seat.ExternalID > 0 {
					ext := seat.ExternalID
					systemSeatIDs = append(systemSeatIDs, &ext)
					if ext > maxExternalID {
						maxExternalID = ext
					}
				} else {
					systemSeatIDs = append(systemSeatIDs, nil)
				}
			}
		}
	}
	seatIDSource := gen.SeatIDSourceArena
	if maxExternalID > 0 {
		seatIDSource = gen.SeatIDSourceBil24
		// Push the arena sequence past every explicitly assigned id so a
		// later plain materialization can never mint a colliding value
		// (system_seat_id is globally UNIQUE). Monotone and idempotent.
		if err := qtx.AdvanceSessionSeatSystemIDSeq(ctx, maxExternalID); err != nil {
			h.logger.Error("seating: bind seat id sequence advance failed",
				slog.String("error", err.Error()),
			)
			return nil, bindErr(http.StatusInternalServerError,
				"seating.bind_failed", "failed to reserve seat id range", nil)
		}
	}
	materialized := 0
	if len(seatKeys) > 0 {
		inserted, err := qtx.InsertSessionSeats(
			ctx, sessionID, seatKeys, sectorNames, rowNames, seatNumbers, tierIDs,
			systemSeatIDs, seatIDSource,
		)
		if err != nil {
			h.logger.Error("seating: bind seat materialize failed",
				slog.String("error", err.Error()),
			)
			return nil, bindErr(http.StatusInternalServerError,
				"seating.bind_failed", "failed to materialize session seats", nil)
		}
		materialized = int(inserted)
	}

	// AB-51: materialize one ga_unit row per General Admission place —
	// "ga|c<categoryIndex>|<n>" keys, tier fixed to the category tier.
	// GA places exist as inventory from setup, not as a by-product of
	// purchase; they share the seat status machine and transition table.
	gaMaterialized := 0
	if req.AdmissionMode != "assigned_seats" {
		for _, cat := range geometry.GACategories() {
			tierID, hasTier := resolvedMap[cat.Index]
			if !hasTier {
				return nil, bindErr(http.StatusInternalServerError,
					"seating.bind_failed",
					fmt.Sprintf("GA category %d has no resolved tier", cat.Index),
					nil)
			}
			if cat.Capacity <= 0 || cat.Capacity > math.MaxInt32 {
				return nil, bindErr(http.StatusInternalServerError,
					"seating.bind_failed",
					fmt.Sprintf("GA category %d capacity %d out of range", cat.Index, cat.Capacity),
					nil)
			}
			inserted, err := qtx.InsertGAUnits(
				ctx, sessionID,
				fmt.Sprintf("ga|c%d", cat.Index),
				0, &tierID,
				int32(cat.Capacity), //nolint:gosec // bound-checked above
			)
			if err != nil {
				h.logger.Error("seating: bind GA unit materialize failed",
					slog.String("error", err.Error()),
				)
				return nil, bindErr(http.StatusInternalServerError,
					"seating.bind_failed", "failed to materialize GA units", nil)
			}
			gaMaterialized += int(inserted)
		}
	}

	// Lock the version's locked_at on first bind (idempotent for later
	// binds against the same version). This is the immutability latch
	// described in §5.1 of the seating backlog: once any session references
	// a version, its geometry MUST NOT change.
	lockedVersion, err := qtx.LockSeatingPlanVersion(ctx, planVersionID)
	if err != nil {
		h.logger.Error("seating: bind lock version failed", slog.String("error", err.Error()))
		return nil, bindErr(http.StatusInternalServerError,
			"seating.bind_failed", "failed to lock seating plan version", nil)
	}

	// Recompute capacity_total from the bound version per admission mode
	// (AB-40 B6: bound seat count + sum of GA category capacities).
	// Documented capacity-propagation hook per 0016_sessions.sql:58.
	var newCapacity int32
	switch req.AdmissionMode {
	case "hybrid":
		newCapacity = lockedVersion.CapacitySeated + lockedVersion.CapacityStanding
	case "general_admission":
		newCapacity = lockedVersion.CapacityStanding
	default:
		newCapacity = lockedVersion.CapacitySeated
	}
	updated, err := qtx.BindSessionSeatingPlan(
		ctx, sessionID, eventID, req.AdmissionMode, &planVersionID, newCapacity,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The session was soft-deleted between the initial lookup and
			// here — treat as 404 to match the sessions CRUD precedent.
			return nil, bindErr(http.StatusNotFound,
				"session.not_found", "session not found", nil)
		}
		h.logger.Error("seating: bind session update failed", slog.String("error", err.Error()))
		return nil, bindErr(http.StatusInternalServerError,
			"seating.bind_failed", "failed to bind seating plan", nil)
	}

	// Stringify the resolved category → tier map once; the same map feeds
	// both the audit metadata and the response envelope.
	catStr := make(map[string]string, len(resolvedMap))
	for idx, id := range resolvedMap {
		catStr[strconv.Itoa(idx)] = id.String()
	}

	// Audit event on the session resource. The seating_plan_version_id and
	// materialization counts land in metadata so a post-hoc trace of the
	// binding is possible without walking session_seats.
	if err := h.writeSessionAuditTx(ctx, tx, r, "v1.session.seating.bind", sessionID, map[string]any{
		"event_id":                eventID.String(),
		"seating_plan_version_id": planVersionID.String(),
		"admission_mode":          req.AdmissionMode,
		"materialized_seats":      materialized,
		"materialized_ga_units":   gaMaterialized,
		"category_tier_map":       catStr,
		"rebound":                 rebound,
		"capacity_total":          newCapacity,
	}); err != nil {
		return nil, bindErr(http.StatusInternalServerError,
			"seating.audit_failed", "failed to write audit event", nil)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, bindErr(http.StatusInternalServerError,
			"seating.commit_failed", "failed to commit transaction", nil)
	}

	createdStr := make([]string, 0, len(createdTierIDs))
	for _, id := range createdTierIDs {
		createdStr = append(createdStr, id.String())
	}
	return &bindCoreResult{
		Session:         updated,
		Version:         lockedVersion,
		Materialized:    materialized,
		CategoryTierMap: catStr,
		CreatedTierIDs:  createdStr,
		Rebound:         rebound,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Body decoding
// ─────────────────────────────────────────────────────────────────────────────

// readBindRequest slurps and strictly-decodes the bind request body.
func readBindRequest(w http.ResponseWriter, r *http.Request) (bindRequest, bool) {
	var out bindRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, bindBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"seating.invalid_body", "request body is not valid JSON: "+err.Error(), r,
		))
		return bindRequest{}, false
	}
	return out, true
}

// parseBindRequest validates the required top-level fields and returns the
// parsed seating_plan_version_id. Category tier map contents are validated
// later in resolveCategoryTierMap once the geometry is known.
func parseBindRequest(w http.ResponseWriter, r *http.Request, req bindRequest) (uuid.UUID, bool) {
	if req.SeatingPlanVersionID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating.invalid_body",
			"seating_plan_version_id is required", r,
			map[string]any{"field": "seating_plan_version_id"},
		))
		return uuid.Nil, false
	}
	planVersionID, err := uuid.Parse(req.SeatingPlanVersionID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating.invalid_body",
			"seating_plan_version_id must be a UUID", r,
			map[string]any{"field": "seating_plan_version_id"},
		))
		return uuid.Nil, false
	}
	if !validBindAdmissionModes[req.AdmissionMode] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating.invalid_admission_mode",
			"admission_mode must be one of assigned_seats|hybrid|general_admission", r,
			map[string]any{"field": "admission_mode"},
		))
		return uuid.Nil, false
	}
	if req.CategoryTierMap == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating.invalid_body",
			"category_tier_map is required", r,
			map[string]any{"field": "category_tier_map"},
		))
		return uuid.Nil, false
	}
	return planVersionID, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Category → tier resolution
// ─────────────────────────────────────────────────────────────────────────────

// resolveCategoryTierMap returns an index → tier_id map for every category
// present in the geometry. Steps:
//
//  1. Parse every incoming key (must be a positive int, must reference a
//     category actually present in the geometry).
//  2. Verify each tier UUID exists AND is scoped to the target session
//     (GetTicketTierByID rejects cross-session leaks).
//  3. For any category NOT in the incoming map:
//     - if auto_create_tiers = true, provision a ticket_tiers row from
//     the Category name / price_hint (currency follows the session).
//     - otherwise: 400 seating.category_tier_map_incomplete with the
//     list of missing categories.
//
// The returned createdTierIDs slice is empty when auto_create_tiers=false
// or when the caller-supplied map already covered every category.
func resolveCategoryTierMap(
	ctx context.Context,
	qtx *gen.Queries,
	sessionID uuid.UUID,
	geometry seating.Geometry,
	req bindRequest,
) (map[int]uuid.UUID, []uuid.UUID, *BindError) {
	// Build the index → *Category lookup for validation.
	byIndex := make(map[int]seating.Category, len(geometry.Categories))
	for _, c := range geometry.Categories {
		byIndex[c.Index] = c
	}

	// Categories actually referenced by at least one seat, plus every
	// GA category (AB-51: each GA category materializes units carrying
	// its tier) — this is what we must have a tier for. Seated
	// categories not referenced by any seat are ignored (dangling
	// legend entries are possible per §6 rule 5).
	referenced := make(map[int]bool)
	for _, section := range geometry.Sections {
		for _, row := range section.Rows {
			for _, seat := range row.Seats {
				referenced[seat.CategoryIndex] = true
			}
		}
	}
	for _, c := range geometry.Categories {
		if c.IsGA() {
			referenced[c.Index] = true
		}
	}

	resolved := make(map[int]uuid.UUID)
	created := make([]uuid.UUID, 0)

	// Consume the caller-supplied map.
	for k, v := range req.CategoryTierMap {
		idx, err := strconv.Atoi(k)
		if err != nil || idx <= 0 {
			return nil, nil, bindErr(http.StatusBadRequest,
				"seating.invalid_category_key",
				"category_tier_map key "+k+" is not a positive integer",
				map[string]any{"field": "category_tier_map"})
		}
		if _, ok := byIndex[idx]; !ok {
			return nil, nil, bindErr(http.StatusBadRequest,
				"seating.unknown_category",
				"category_tier_map references category_index "+k+" that is not in the geometry",
				map[string]any{"field": "category_tier_map", "category_index": idx})
		}
		if v == nil || *v == "" {
			return nil, nil, bindErr(http.StatusBadRequest,
				"seating.invalid_category_tier_map",
				"category_tier_map["+k+"] must be a tier UUID",
				map[string]any{"field": "category_tier_map", "category_index": idx})
		}
		tierID, err := uuid.Parse(*v)
		if err != nil {
			return nil, nil, bindErr(http.StatusBadRequest,
				"seating.invalid_category_tier_map",
				"category_tier_map["+k+"] must be a UUID",
				map[string]any{"field": "category_tier_map", "category_index": idx})
		}
		if _, err := qtx.GetTicketTierByID(ctx, tierID, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil, bindErr(http.StatusBadRequest,
					"seating.tier_not_found",
					"category_tier_map["+k+"] references a tier that does not belong to this session",
					map[string]any{"field": "category_tier_map", "category_index": idx})
			}
			return nil, nil, bindErr(http.StatusInternalServerError,
				"seating.bind_failed", "failed to validate category_tier_map", nil)
		}
		resolved[idx] = tierID
	}

	// Fill in the gaps.
	missing := make([]int, 0)
	for idx := range referenced {
		if _, ok := resolved[idx]; ok {
			continue
		}
		if !req.AutoCreateTiers {
			missing = append(missing, idx)
			continue
		}
		cat := byIndex[idx]
		tierID, err := autoCreateTier(ctx, qtx, sessionID, cat, len(resolved))
		if err != nil {
			return nil, nil, bindErr(http.StatusInternalServerError,
				"seating.bind_failed",
				"failed to auto-create ticket tier for category "+strconv.Itoa(idx)+": "+err.Error(),
				nil)
		}
		resolved[idx] = tierID
		created = append(created, tierID)
	}
	if len(missing) > 0 {
		return nil, nil, bindErr(http.StatusBadRequest,
			"seating.category_tier_map_incomplete",
			"category_tier_map does not cover every referenced category; "+
				"set auto_create_tiers=true or add the missing entries",
			map[string]any{
				"field":                 "category_tier_map",
				"missing_categories":    missing,
				"referenced_categories": sortedInts(referenced),
			})
	}
	return resolved, created, nil
}

// autoCreateTier provisions a ticket_tier row for the given category. The
// import hints are applied on a best-effort basis: PriceHint is parsed as an
// integer number of minor units (cents). The currency ALWAYS follows the
// owning session (AB-38 — one currency per session; the composite FK
// ticket_tiers_currency_matches_session would reject anything else), so the
// geometry CurrencyHint is deliberately ignored.
// Callers ordering multiple auto-creates should pass a monotonically
// increasing sortOffset so the display order is stable.
func autoCreateTier(
	ctx context.Context,
	qtx *gen.Queries,
	sessionID uuid.UUID,
	cat seating.Category,
	sortOffset int,
) (uuid.UUID, error) {
	name := cat.Name
	if name == "" {
		name = "Category " + strconv.Itoa(cat.Index)
	}
	sess, err := qtx.GetSessionCurrency(ctx, sessionID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve session currency: %w", err)
	}
	currency := sess
	priceAmount := int64(0)
	pricingMode := "free"
	if cat.PriceHint != "" {
		if v, err := strconv.ParseInt(cat.PriceHint, 10, 64); err == nil && v > 0 {
			priceAmount = v
			pricingMode = "fixed"
		}
	}
	// AB-51: a GA category's tier is capped at the category capacity so
	// tier-level selling can never exceed the declared zone size.
	var tierCapacity *int32
	if cat.IsGA() && cat.Capacity > 0 && cat.Capacity <= math.MaxInt32 {
		c := int32(cat.Capacity) //nolint:gosec // bound-checked above
		tierCapacity = &c
	}
	// Clamp the sort-order to the int32 range. Category.Index is a small
	// positive int seeded from geometry (typically < 100), so overflow is
	// only reachable via a maliciously hand-crafted geometry blob.
	sortOrder := cat.Index + sortOffset
	if sortOrder < 0 {
		sortOrder = 0
	} else if sortOrder > math.MaxInt32 {
		sortOrder = math.MaxInt32
	}
	row, err := qtx.InsertTicketTier(
		ctx, sessionID,
		name, pricingMode, priceAmount, currency,
		nil, nil, tierCapacity, nil, nil,
		int32(sortOrder), //nolint:gosec // clamped to [0, MaxInt32] above
	)
	if err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}

// sortedInts materializes the keys of set as an ascending slice. Used only
// for deterministic error-envelope payloads.
func sortedInts(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Audit
// ─────────────────────────────────────────────────────────────────────────────

// writeSessionAuditTx emits a session-scoped audit trail entry under the
// caller's transaction. It is the session-resource counterpart of the
// plan-scoped writeAuditTx (plans.go) and is shared by the SEAT-B2 bind
// endpoint ("v1.session.seating.bind") and the SEAT-B4 seat block /
// unblock endpoint ("v1.session.seats.block" / ".unblock") — callers pass
// the action explicitly.
func (h *Handler) writeSessionAuditTx(
	ctx context.Context,
	tx pgx.Tx,
	r *http.Request,
	action string,
	sessionID uuid.UUID,
	metadata map[string]any,
) error {
	if h.audit == nil {
		return nil
	}
	actor, _ := auth.ActorFromContext(ctx)
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "session",
		ResourceID:   sessionID.String(),
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
		h.logger.Error("seating: session audit write failed", slog.String("error", err.Error()))
		return err
	}
	return nil
}
