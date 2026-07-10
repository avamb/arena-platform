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
//     Category.Name / PriceHint /
//     CurrencyHint import hints.
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
//   - GA sessions (admission_mode = general_admission) are not touched by
//     this endpoint. This handler only accepts assigned_seats or hybrid.
//     The inventory_ledger path for GA sessions remains unchanged.
package hseating

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
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
// accepts. general_admission is deliberately absent — GA sessions do not
// need a plan binding.
var validBindAdmissionModes = map[string]bool{
	"assigned_seats": true,
	"hybrid":         true,
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

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.queries.WithTx(tx)

	// Load the session binding scoped by (id, event_id). The GA sanity
	// projection returns the current admission_mode + seating_plan_version_id
	// which drives the rebind guardrail below.
	binding, err := qtx.GetSessionSeatingBinding(ctx, sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"session.not_found", "session not found", r,
			))
			return
		}
		h.logger.Error("seating: bind session lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.bind_failed", "failed to bind seating plan", r,
		))
		return
	}

	// Load the seating plan version + its parent plan so the geometry can
	// be canonicalized in-memory. A missing version is a client error, not
	// a server error, because plan_version_id is caller-supplied.
	version, err := qtx.GetSeatingPlanVersionByID(ctx, planVersionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"seating.version_not_found",
				"seating_plan_version_id does not exist", r,
				map[string]any{"field": "seating_plan_version_id"},
			))
			return
		}
		h.logger.Error("seating: bind version lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.bind_failed", "failed to bind seating plan", r,
		))
		return
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
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seating.bind_failed", "failed to bind seating plan", r,
			))
			return
		}
		tktCount, err := qtx.CountTicketsBySession(ctx, sessionID)
		if err != nil {
			h.logger.Error("seating: bind ticket count failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seating.bind_failed", "failed to bind seating plan", r,
			))
			return
		}
		if resCount > 0 || tktCount > 0 {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"seating.rebind_forbidden",
				"session already has reservations or tickets; create a new session to change the seating plan",
				r,
				map[string]any{
					"reservations": resCount,
					"tickets":      tktCount,
				},
			))
			return
		}
	}

	// Decode the canonical geometry. seating.Canonicalize is idempotent so
	// re-applying it here is safe and defends against any downstream drift
	// in what the DB returned (e.g. a hand-edited row).
	var geometry seating.Geometry
	if err := json.Unmarshal(version.Geometry, &geometry); err != nil {
		h.logger.Error("seating: bind geometry decode failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.bind_failed", "seating plan version geometry is not valid", r,
		))
		return
	}
	geometry = seating.Canonicalize(geometry)

	// Validate and materialize the category → tier map. On auto_create_tiers
	// = true, missing categories are provisioned as fresh ticket_tiers rows
	// bound to the target session; the created ids are returned to the
	// caller so the follow-up admin UI can hydrate them without an extra
	// GET.
	resolvedMap, createdTierIDs, ok := resolveCategoryTierMap(
		ctx, w, r, qtx, sessionID, geometry, req,
	)
	if !ok {
		return
	}

	// Rebind: wipe any previously materialized seats for this session so the
	// new bind starts from a clean slate. session_seats has (session_id,
	// seat_key) UNIQUE — leaving stale rows around risks silent capacity
	// drift on the assigned_seats/hybrid inventory paths.
	if rebound {
		if _, err := tx.Exec(ctx,
			`DELETE FROM reservation_seats
			 WHERE session_seat_id IN (
			     SELECT id FROM session_seats WHERE session_id = $1
			 )`,
			sessionID,
		); err != nil {
			h.logger.Error("seating: bind reservation_seats wipe failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seating.bind_failed", "failed to prepare session for rebind", r,
			))
			return
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM session_seats WHERE session_id = $1`,
			sessionID,
		); err != nil {
			h.logger.Error("seating: bind session_seats wipe failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seating.bind_failed", "failed to prepare session for rebind", r,
			))
			return
		}
	}

	// Materialize one session_seats row per geometry seat under the same
	// transaction. seat_key is copied verbatim from the geometry
	// (§5.3 stable identifier); tier_id comes from the resolved map.
	materialized := 0
	for _, section := range geometry.Sections {
		for _, row := range section.Rows {
			for _, seat := range row.Seats {
				tierID, hasTier := resolvedMap[seat.CategoryIndex]
				if !hasTier {
					// resolveCategoryTierMap guarantees every category
					// referenced by a seat has an entry; a missing key
					// here would be a canonicaliser bug, not user error.
					httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
						"seating.bind_failed",
						fmt.Sprintf("seat %q references unknown category_index %d",
							seat.Key, seat.CategoryIndex),
						r,
					))
					return
				}
				tierPtr := tierID // copy so &tierPtr is stable per iteration
				if _, err := qtx.InsertSessionSeat(
					ctx, sessionID,
					seat.Key, section.Name, row.Name, seat.Number,
					&tierPtr,
				); err != nil {
					h.logger.Error("seating: bind seat materialize failed",
						slog.String("error", err.Error()),
						slog.String("seat_key", seat.Key),
					)
					httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
						"seating.bind_failed", "failed to materialize session seats", r,
					))
					return
				}
				materialized++
			}
		}
	}

	// Lock the version's locked_at on first bind (idempotent for later
	// binds against the same version). This is the immutability latch
	// described in §5.1 of the seating backlog: once any session references
	// a version, its geometry MUST NOT change.
	lockedVersion, err := qtx.LockSeatingPlanVersion(ctx, planVersionID)
	if err != nil {
		h.logger.Error("seating: bind lock version failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.bind_failed", "failed to lock seating plan version", r,
		))
		return
	}

	// Recompute capacity_total from the seated (and, for hybrid, standing)
	// capacity of the bound version. Documented capacity-propagation hook
	// per 0016_sessions.sql:58.
	newCapacity := lockedVersion.CapacitySeated
	if req.AdmissionMode == "hybrid" {
		newCapacity += lockedVersion.CapacityStanding
	}
	updated, err := qtx.BindSessionSeatingPlan(
		ctx, sessionID, eventID, req.AdmissionMode, &planVersionID, newCapacity,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The session was soft-deleted between the initial lookup and
			// here — treat as 404 to match the sessions CRUD precedent.
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"session.not_found", "session not found", r,
			))
			return
		}
		h.logger.Error("seating: bind session update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.bind_failed", "failed to bind seating plan", r,
		))
		return
	}

	// Audit event on the session resource. The seating_plan_version_id and
	// materialization counts land in metadata so a post-hoc trace of the
	// binding is possible without walking session_seats.
	auditMap := map[string]string{}
	for k, v := range resolvedMap {
		auditMap[strconv.Itoa(k)] = v.String()
	}
	if err := h.writeBindAuditTx(ctx, tx, r, sessionID, map[string]any{
		"event_id":                eventID.String(),
		"seating_plan_version_id": planVersionID.String(),
		"admission_mode":          req.AdmissionMode,
		"materialized_seats":      materialized,
		"category_tier_map":       auditMap,
		"rebound":                 rebound,
		"capacity_total":          newCapacity,
	}); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.audit_failed", "failed to write audit event", r,
		))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	catStr := make(map[string]string, len(resolvedMap))
	for idx, id := range resolvedMap {
		catStr[strconv.Itoa(idx)] = id.String()
	}
	createdStr := make([]string, 0, len(createdTierIDs))
	for _, id := range createdTierIDs {
		createdStr = append(createdStr, id.String())
	}
	httputil.WriteJSON(w, http.StatusOK, bindResponse{
		Session:         sessionBindingFromRow(updated),
		Version:         SeatingPlanVersionFromRow(lockedVersion),
		Materialized:    materialized,
		CategoryTierMap: catStr,
		CreatedTierIDs:  createdStr,
		Rebound:         rebound,
	})
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
			"admission_mode must be one of assigned_seats|hybrid", r,
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
//     the Category name / price_hint / currency_hint.
//     - otherwise: 400 seating.category_tier_map_incomplete with the
//     list of missing categories.
//
// The returned createdTierIDs slice is empty when auto_create_tiers=false
// or when the caller-supplied map already covered every category.
func resolveCategoryTierMap(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	qtx *gen.Queries,
	sessionID uuid.UUID,
	geometry seating.Geometry,
	req bindRequest,
) (map[int]uuid.UUID, []uuid.UUID, bool) {
	// Build the index → *Category lookup for validation.
	byIndex := make(map[int]seating.Category, len(geometry.Categories))
	for _, c := range geometry.Categories {
		byIndex[c.Index] = c
	}

	// Categories actually referenced by at least one seat — this is what
	// we must have a tier for. Categories that appear in Geometry.Categories
	// but are not referenced by any seat are ignored (dangling legend
	// entries are possible per §6 rule 5).
	referenced := make(map[int]bool)
	for _, section := range geometry.Sections {
		for _, row := range section.Rows {
			for _, seat := range row.Seats {
				referenced[seat.CategoryIndex] = true
			}
		}
	}

	resolved := make(map[int]uuid.UUID)
	created := make([]uuid.UUID, 0)

	// Consume the caller-supplied map.
	for k, v := range req.CategoryTierMap {
		idx, err := strconv.Atoi(k)
		if err != nil || idx <= 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"seating.invalid_category_key",
				"category_tier_map key "+k+" is not a positive integer", r,
				map[string]any{"field": "category_tier_map"},
			))
			return nil, nil, false
		}
		if _, ok := byIndex[idx]; !ok {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"seating.unknown_category",
				"category_tier_map references category_index "+k+" that is not in the geometry",
				r,
				map[string]any{"field": "category_tier_map", "category_index": idx},
			))
			return nil, nil, false
		}
		if v == nil || *v == "" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"seating.invalid_category_tier_map",
				"category_tier_map["+k+"] must be a tier UUID", r,
				map[string]any{"field": "category_tier_map", "category_index": idx},
			))
			return nil, nil, false
		}
		tierID, err := uuid.Parse(*v)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"seating.invalid_category_tier_map",
				"category_tier_map["+k+"] must be a UUID", r,
				map[string]any{"field": "category_tier_map", "category_index": idx},
			))
			return nil, nil, false
		}
		if _, err := qtx.GetTicketTierByID(ctx, tierID, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"seating.tier_not_found",
					"category_tier_map["+k+"] references a tier that does not belong to this session",
					r,
					map[string]any{"field": "category_tier_map", "category_index": idx},
				))
				return nil, nil, false
			}
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seating.bind_failed", "failed to validate category_tier_map", r,
			))
			return nil, nil, false
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
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seating.bind_failed",
				"failed to auto-create ticket tier for category "+strconv.Itoa(idx)+": "+err.Error(), r,
			))
			return nil, nil, false
		}
		resolved[idx] = tierID
		created = append(created, tierID)
	}
	if len(missing) > 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating.category_tier_map_incomplete",
			"category_tier_map does not cover every referenced category; "+
				"set auto_create_tiers=true or add the missing entries",
			r,
			map[string]any{
				"field":                 "category_tier_map",
				"missing_categories":    missing,
				"referenced_categories": sortedInts(referenced),
			},
		))
		return nil, nil, false
	}
	return resolved, created, true
}

// autoCreateTier provisions a ticket_tier row for the given category. The
// import hints are applied on a best-effort basis: PriceHint is parsed as an
// integer number of minor units (cents), CurrencyHint falls back to USD.
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
	currency := cat.CurrencyHint
	if currency == "" {
		currency = "USD"
	}
	priceAmount := int64(0)
	pricingMode := "free"
	if cat.PriceHint != "" {
		if v, err := strconv.ParseInt(cat.PriceHint, 10, 64); err == nil && v > 0 {
			priceAmount = v
			pricingMode = "fixed"
		}
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
		nil, nil, nil, nil, nil,
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
	// tiny slice — insertion sort keeps this allocation-free.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Audit
// ─────────────────────────────────────────────────────────────────────────────

// writeBindAuditTx emits the audit trail entry for a successful bind under
// the caller's transaction.
func (h *Handler) writeBindAuditTx(
	ctx context.Context,
	tx pgx.Tx,
	r *http.Request,
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
		Action:       "v1.session.seating.bind",
		ResourceType: "session",
		ResourceID:   sessionID.String(),
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
		h.logger.Error("seating: bind audit write failed", slog.String("error", err.Error()))
		return err
	}
	return nil
}
