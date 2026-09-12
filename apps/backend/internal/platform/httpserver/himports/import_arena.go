// import_arena.go implements the source=arena branch of the event bundle
// (feature #525, W1-E1c; event-bundle spec §3.2) — the matching order that
// replaces the Bil24 one when the caller owns no external identifiers and
// arena itself mints them.
//
// Everything here runs inside the caller's transaction, like import_exec.go,
// so a failure at any step leaves the catalog exactly as it was. The one
// deliberate difference from the Bil24 path is WHERE the venue timezone is
// resolved: a Bil24 venue is found by its global external id before the
// transaction opens, while an arena venue is found by compat id or by name
// inside it — so the session's start instant can only be computed once the
// venue is known, and that happens here rather than in the HTTP layer.
package himports

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

// arenaMatch is the outcome of spec §3.2 steps 1-2: which existing session (if
// any) the bundle addresses, and whether its externalRef row already exists.
type arenaMatch struct {
	// SessionID is uuid.Nil when the bundle creates a new session.
	SessionID uuid.UUID
	// Ctx is only meaningful when SessionID is set.
	Ctx gen.SessionImportContextRow
	// RefBound reports that session_external_refs already carries
	// (org, externalRef) → SessionID, so no insert is needed.
	RefBound bool
}

// executeArenaImport is the source=arena counterpart of executeImport.
func (h *Handler) executeArenaImport(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, warnings *warningSink) (importResult, error) {
	match, err := h.matchArenaSession(ctx, q, tx, plan)
	if err != nil {
		return importResult{}, err
	}
	eventID, err := h.resolveArenaEvent(ctx, q, tx, plan, match)
	if err != nil {
		return importResult{}, err
	}
	venueID, loc, err := h.resolveArenaVenue(ctx, q, tx, plan, match, warnings)
	if err != nil {
		return importResult{}, err
	}
	startAt, err := plan.Request.ActionEvent.ParseLocalStart(loc)
	if err != nil {
		return importResult{}, failImport(http.StatusUnprocessableEntity, "import.invalid_start_time", err.Error())
	}
	endAt, err := plan.Request.ActionEvent.ParseLocalEnd(loc)
	if err != nil {
		return importResult{}, failImport(http.StatusUnprocessableEntity, "import.end_time_invalid", err.Error())
	}
	startAt = startAt.UTC()
	finish := startAt.Add(defaultSessionDuration)
	if endAt != nil {
		finish = endAt.UTC()
	}

	sessionID, created, err := h.upsertArenaSession(ctx, q, plan, match, eventID, venueID, startAt, finish, warnings)
	if err != nil {
		return importResult{}, err
	}
	if !match.RefBound {
		if err := q.InsertSessionExternalRef(ctx, plan.OrgID, plan.ExternalRef, sessionID); err != nil {
			return importResult{}, arenaRefInsertError(err, plan.ExternalRef)
		}
	}

	orderedTiers, err := h.upsertArenaTiers(ctx, q, tx, plan, sessionID, warnings)
	if err != nil {
		return importResult{}, err
	}

	if err := h.syncInventoryLedger(ctx, q, eventID, sessionID); err != nil {
		return importResult{}, err
	}
	var publishedNow bool
	if plan.Request.Publish {
		publishedNow, err = h.applyPublish(ctx, q, plan, eventID, sessionID, warnings)
		if err != nil {
			return importResult{}, err
		}
	}

	compat, err := ensureCompatIDs(ctx, tx, eventID, sessionID, venueID, orderedTiers)
	if err != nil {
		return importResult{}, err
	}
	// tier_ids is keyed by the DECIMAL categoryPriceId, which for an arena
	// bundle is the id that was just minted — so the map can only be built
	// after ensureCompatIDs has run.
	keyed := make(map[string]uuid.UUID, len(orderedTiers))
	for i, tierID := range orderedTiers {
		keyed[strconv.FormatInt(compat.CategoryPriceIDs[i], 10)] = tierID
	}

	return importResult{
		EventID:      eventID,
		SessionID:    sessionID,
		TierIDs:      keyed,
		Created:      created,
		CompatIDs:    compat,
		PublishedNow: publishedNow,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Spec §3.2 steps 1-2 — session
// ─────────────────────────────────────────────────────────────────────────────

// matchArenaSession finds the session the bundle addresses: first by
// externalRef, then by actionEventId. A disagreement between the two is a
// conflict, because silently preferring one would move the caller's
// idempotency key onto a session it did not mean.
func (h *Handler) matchArenaSession(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan) (arenaMatch, error) {
	var m arenaMatch
	ae := plan.Request.ActionEvent

	byRef, _, err := q.GetSessionByExternalRef(ctx, plan.OrgID, plan.ExternalRef)
	switch {
	case err == nil:
		m.SessionID = byRef
		m.RefBound = true
	case !errors.Is(err, pgx.ErrNoRows):
		return m, fmt.Errorf("lookup session by external ref: %w", err)
	}

	if ae.ActionEventID > 0 {
		byID, resolveErr := compatids.Resolve(ctx, tx, compatids.KindActionEvent, ae.ActionEventID)
		switch {
		case errors.Is(resolveErr, compatids.ErrNotFound):
			return m, errCompatIDUnknown("actionEvent.actionEventId", ae.ActionEventID)
		case resolveErr != nil:
			return m, fmt.Errorf("resolve action event compat id: %w", resolveErr)
		}
		if m.SessionID != uuid.Nil && byID != m.SessionID {
			return m, failImport(http.StatusConflict, "import.external_ref_conflict",
				"externalRef "+plan.ExternalRef+" is bound to a different session than actionEventId "+
					strconv.FormatInt(ae.ActionEventID, 10))
		}
		m.SessionID = byID
	}

	if m.SessionID == uuid.Nil {
		return m, nil
	}

	sctx, err := q.GetSessionImportContext(ctx, m.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The ref row or the compat mapping outlived the session. Minting a
			// second session under the same key would break idempotency for
			// good, so this needs an operator, not a silent recovery.
			return m, failImport(http.StatusConflict, "import.session_deleted",
				"externalRef "+plan.ExternalRef+" points at a deleted arena session")
		}
		return m, fmt.Errorf("read session %s: %w", m.SessionID, err)
	}
	// A session of another organization is reported as unknown, not as a
	// conflict: the caller must not learn that the id exists at all.
	if sctx.OrgID != plan.OrgID {
		return m, errCompatIDUnknown("actionEvent.actionEventId", ae.ActionEventID)
	}
	m.Ctx = sctx

	if !m.RefBound {
		existing, refErr := q.GetExternalRefBySession(ctx, m.SessionID)
		switch {
		case refErr == nil:
			if existing != plan.ExternalRef {
				return m, failImport(http.StatusConflict, "import.external_ref_conflict",
					"session addressed by actionEventId "+strconv.FormatInt(ae.ActionEventID, 10)+
						" is already bound to a different externalRef")
			}
			m.RefBound = true
		case !errors.Is(refErr, pgx.ErrNoRows):
			return m, fmt.Errorf("read session external ref: %w", refErr)
		}
	}
	return m, nil
}

// arenaRefInsertError turns the two unique violations session_external_refs
// can raise into the caller-visible conflict. Anything else is infrastructure.
func arenaRefInsertError(err error, ref string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return failImport(http.StatusConflict, "import.external_ref_conflict",
			"externalRef "+ref+" is already bound to another session of this organization")
	}
	return fmt.Errorf("bind session external ref: %w", err)
}

// ─────────────────────────────────────────────────────────────────────────────
// Spec §3.2 step 3 — event
// ─────────────────────────────────────────────────────────────────────────────

// resolveArenaEvent finds or creates the event. There is deliberately NO
// match-by-name: two different productions sharing a title is normal, and
// folding them together would be unrecoverable.
func (h *Handler) resolveArenaEvent(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, m arenaMatch) (uuid.UUID, error) {
	a := plan.Request.Action
	var eventID uuid.UUID

	switch {
	case a.ActionID > 0:
		resolved, err := compatids.Resolve(ctx, tx, compatids.KindAction, a.ActionID)
		switch {
		case errors.Is(err, compatids.ErrNotFound):
			return uuid.Nil, errCompatIDUnknown("action.actionId", a.ActionID)
		case err != nil:
			return uuid.Nil, fmt.Errorf("resolve action compat id: %w", err)
		}
		row, err := q.GetEventRaw(ctx, resolved)
		switch {
		case errors.Is(err, pgx.ErrNoRows), err == nil && row.OrgID != plan.OrgID:
			return uuid.Nil, errCompatIDUnknown("action.actionId", a.ActionID)
		case err != nil:
			return uuid.Nil, fmt.Errorf("read event %s: %w", resolved, err)
		}
		eventID = resolved
		if m.SessionID != uuid.Nil && m.Ctx.EventID != eventID {
			// A session cannot be moved between events by a bundle: its tiers,
			// tickets and publication state hang off the current one.
			return uuid.Nil, failImport(http.StatusConflict, "import.action_mismatch",
				"the addressed session belongs to a different action than actionId "+
					strconv.FormatInt(a.ActionID, 10))
		}
	case m.SessionID != uuid.Nil:
		eventID = m.Ctx.EventID
	default:
		created, err := q.InsertEvent(ctx, plan.OrgID, a.Name(), optString(a.Description), "draft", "public", nil)
		if err != nil {
			return uuid.Nil, fmt.Errorf("insert event: %w", err)
		}
		eventID = created.ID
	}

	if err := q.SetEventImportMetadata(ctx, eventID, plan.OrgID, a.Name(), optString(a.Description), optString(a.Age)); err != nil {
		return uuid.Nil, fmt.Errorf("update event metadata: %w", err)
	}
	if plan.PosterMediaID != nil {
		if err := q.SetEventPosterMediaID(ctx, eventID, plan.OrgID, plan.PosterMediaID); err != nil {
			return uuid.Nil, fmt.Errorf("attach event poster: %w", err)
		}
	}
	return eventID, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Spec §3.2 step 4 — venue
// ─────────────────────────────────────────────────────────────────────────────

// resolveArenaVenue finds or creates the venue and returns the location the
// payload's local day/time must be interpreted in.
//
// A venue matched BY NAME is reused as is: the bundle only proves the operator
// typed the same name, not that it may overwrite an address, geo point or
// timezone that other events already depend on. A venue addressed by its
// compat id is the caller's own and its geography is refreshed.
func (h *Handler) resolveArenaVenue(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, m arenaMatch, warnings *warningSink) (uuid.UUID, *time.Location, error) {
	v := plan.Request.Venue
	payloadTZ := trimSpace(v.Timezone)
	name := trimSpace(v.VenueName)

	if v.VenueID > 0 {
		venueID, err := compatids.Resolve(ctx, tx, compatids.KindVenue, v.VenueID)
		switch {
		case errors.Is(err, compatids.ErrNotFound):
			return uuid.Nil, nil, errCompatIDUnknown("venue.venueId", v.VenueID)
		case err != nil:
			return uuid.Nil, nil, fmt.Errorf("resolve venue compat id: %w", err)
		}
		vctx, err := q.GetVenueImportContext(ctx, venueID)
		switch {
		case errors.Is(err, pgx.ErrNoRows), err == nil && vctx.OrgID != plan.OrgID:
			return uuid.Nil, nil, errCompatIDUnknown("venue.venueId", v.VenueID)
		case err != nil:
			return uuid.Nil, nil, fmt.Errorf("read venue %s: %w", venueID, err)
		}
		loc, store, err := arenaLocation(derefString(vctx.Timezone), payloadTZ, warnings)
		if err != nil {
			return uuid.Nil, nil, err
		}
		cityID, country := h.resolveGeography(ctx, q, tx, plan, warnings)
		if err := q.UpdateImportedVenueGeography(ctx, venueID, cityID, optString(v.Address), store, v.GeoLat, v.GeoLon, country); err != nil {
			return uuid.Nil, nil, fmt.Errorf("update venue geography: %w", err)
		}
		return venueID, loc, nil
	}

	if name != "" {
		existing, err := q.FindActiveVenueByNormalizedName(ctx, plan.OrgID, name)
		switch {
		case err == nil:
			vctx, ctxErr := q.GetVenueImportContext(ctx, existing.ID)
			if ctxErr != nil {
				return uuid.Nil, nil, fmt.Errorf("read venue %s: %w", existing.ID, ctxErr)
			}
			warnings.add(WarnVenueMatchedByName,
				"venue \""+name+"\" matched an existing venue of this organization and was reused unchanged; "+
					"send venue.venueId to address it explicitly")
			loc, _, locErr := arenaLocation(derefString(vctx.Timezone), payloadTZ, warnings)
			if locErr != nil {
				return uuid.Nil, nil, locErr
			}
			return existing.ID, loc, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return uuid.Nil, nil, fmt.Errorf("lookup venue by name: %w", err)
		}
	}

	// Nothing to match against. An edit that simply omits the venue block
	// keeps the session where it is rather than failing.
	if name == "" {
		if m.SessionID != uuid.Nil {
			vctx, err := q.GetVenueImportContext(ctx, m.Ctx.VenueID)
			if err != nil {
				return uuid.Nil, nil, fmt.Errorf("read venue %s: %w", m.Ctx.VenueID, err)
			}
			loc, _, locErr := arenaLocation(derefString(vctx.Timezone), payloadTZ, warnings)
			if locErr != nil {
				return uuid.Nil, nil, locErr
			}
			return m.Ctx.VenueID, loc, nil
		}
		return uuid.Nil, nil, failImport(http.StatusUnprocessableEntity, "import.venue_name_required",
			"venue.venueName is required when venue.venueId is not supplied")
	}

	loc, store, err := arenaLocation("", payloadTZ, warnings)
	if err != nil {
		return uuid.Nil, nil, err
	}
	cityID, country := h.resolveGeography(ctx, q, tx, plan, warnings)
	created, err := q.InsertArenaVenue(ctx, plan.OrgID, cityID, name, optString(v.Address), store, v.GeoLat, v.GeoLon, country)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("insert venue: %w", err)
	}
	return created.ID, loc, nil
}

// arenaLocation decides which timezone interprets the payload's local
// day/time. The STORED value of an existing venue always wins — changing it
// would silently move every session already scheduled there — and a
// disagreement is surfaced as a warning. The returned second value is what
// should be written to venues.timezone ("" = leave as is).
func arenaLocation(storedTZ, payloadTZ string, warnings *warningSink) (*time.Location, string, error) {
	stored := trimSpace(storedTZ)
	if stored != "" {
		if loc, err := time.LoadLocation(stored); err == nil {
			if payloadTZ != "" && payloadTZ != stored {
				warnings.add(WarnVenueTimezoneKept,
					"payload timezone "+payloadTZ+" ignored; venue keeps its stored timezone "+stored)
			}
			return loc, "", nil
		}
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
	return loc, payloadTZ, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Session upsert
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) upsertArenaSession(
	ctx context.Context,
	q *gen.Queries,
	plan importPlan,
	m arenaMatch,
	eventID, venueID uuid.UUID,
	startAt, endAt time.Time,
	warnings *warningSink,
) (uuid.UUID, bool, error) {
	capacity := plan.Request.TotalAvailability()

	if m.SessionID == uuid.Nil {
		if capacity <= 0 {
			return uuid.Nil, false, failImport(http.StatusUnprocessableEntity, "import.capacity_required",
				"categoryList must declare a positive total availability for a new general-admission session")
		}
		status := "draft"
		if plan.Request.Publish {
			status = "scheduled"
		}
		created, err := q.InsertSession(ctx, eventID, venueID, startAt, endAt, capacity, nil, status, nil, plan.Currency, "override")
		if err != nil {
			return uuid.Nil, false, fmt.Errorf("insert session: %w", err)
		}
		// AB-51 (mirrored from hcatalog/sessions.go): a plan-less GA session's
		// capacity is enforced by a fungible pool of session_seats rows
		// ("ga|pool|<n>", tier NULL until held), not by inventory_ledger — no
		// production RESERVATION path reads that table. The event-bundle
		// importer never binds a seating plan, so every session it creates is
		// plan-less GA and needs its pool materialized here, or the very first
		// RESERVATION against it short-allocates and answers "sold out"
		// regardless of the bundle's declared availability.
		if _, err := q.InsertGAUnits(ctx, created.ID, "ga|pool", 0, nil, capacity); err != nil {
			return uuid.Nil, false, fmt.Errorf("materialize general-admission inventory: %w", err)
		}
		return created.ID, true, nil
	}

	currency := plan.Currency
	if currency != m.Ctx.Currency {
		sold, err := q.CountTicketsBySession(ctx, m.SessionID)
		if err != nil {
			return uuid.Nil, false, fmt.Errorf("count tickets: %w", err)
		}
		if sold > 0 {
			warnings.add(WarnCurrencyLocked,
				"payload currency "+currency+" ignored; session keeps "+m.Ctx.Currency+" because it already has tickets")
			currency = m.Ctx.Currency
		}
	}

	var capacityPtr *int32
	if capacity > 0 {
		capacityPtr = &capacity
	}
	status := ""
	if plan.Request.Publish && m.Ctx.Status == "draft" {
		status = "scheduled"
	}
	if _, err := q.UpdateSession(ctx, m.SessionID, eventID, &venueID, &startAt, &endAt, capacityPtr, nil, status, nil, &currency, "override"); err != nil {
		return uuid.Nil, false, fmt.Errorf("update session: %w", err)
	}
	return m.SessionID, false, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Spec §3.2 step 5 — ticket tiers
// ─────────────────────────────────────────────────────────────────────────────

// upsertArenaTiers maps each categoryList entry onto a tier of THIS session,
// in this order: by categoryPriceId, then by normalised name, then create.
// Tiers of the session the bundle did not mention are left untouched — a
// bundle can add and edit, never delete — and reported as a warning.
//
// The returned slice is positionally aligned with plan.Request.CategoryList so
// compat_ids.category_price_ids can be built from it.
func (h *Handler) upsertArenaTiers(
	ctx context.Context,
	q *gen.Queries,
	tx pgx.Tx,
	plan importPlan,
	sessionID uuid.UUID,
	warnings *warningSink,
) ([]uuid.UUID, error) {
	existing, err := q.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list ticket tiers: %w", err)
	}
	byName := make(map[string]gen.TicketTierRow, len(existing))
	for _, t := range existing {
		key := normalizeTierName(t.Name)
		if _, dup := byName[key]; !dup {
			byName[key] = t
		}
	}

	ordered := make([]uuid.UUID, 0, len(plan.Request.CategoryList))
	touched := make(map[uuid.UUID]struct{}, len(plan.Request.CategoryList))

	for i, c := range plan.Request.CategoryList {
		name := trimSpace(c.CategoryPriceName)
		if name == "" {
			name = "Category " + strconv.Itoa(i+1)
		}
		price := c.PriceMinorUnits()
		if price < 0 {
			return nil, failImport(http.StatusUnprocessableEntity, "import.invalid_price",
				fmt.Sprintf("categoryList[%d].price must not be negative", i))
		}
		mode := "fixed"
		if price == 0 {
			mode = "free"
		}
		var capacity *int32
		if c.Availability > 0 {
			cap32 := c.Availability
			capacity = &cap32
		}
		sortOrder := int32(i) //nolint:gosec // categoryList length is bounded by the request body cap

		var target uuid.UUID
		switch {
		case c.CategoryPriceID > 0:
			resolved, resErr := compatids.Resolve(ctx, tx, compatids.KindCategoryPrice, c.CategoryPriceID)
			switch {
			case errors.Is(resErr, compatids.ErrNotFound):
				return nil, errCompatIDUnknown(fmt.Sprintf("categoryList[%d].categoryPriceId", i), c.CategoryPriceID)
			case resErr != nil:
				return nil, fmt.Errorf("resolve category compat id: %w", resErr)
			}
			target = resolved
		default:
			if match, ok := byName[normalizeTierName(name)]; ok {
				target = match.ID
			}
		}

		if target == uuid.Nil {
			row, insErr := q.InsertTicketTier(ctx, sessionID, name, mode, price, plan.Currency, nil, nil, capacity,
				plan.SaleWindowStart, plan.SaleWindowEnd, sortOrder)
			if insErr != nil {
				return nil, fmt.Errorf("insert ticket tier: %w", insErr)
			}
			ordered = append(ordered, row.ID)
			touched[row.ID] = struct{}{}
			byName[normalizeTierName(name)] = row
			continue
		}

		updated, updErr := q.UpdateTicketTier(ctx, target, sessionID, name, mode, &price, plan.Currency, nil, nil, capacity,
			plan.SaleWindowStart, plan.SaleWindowEnd, &sortOrder)
		if errors.Is(updErr, pgx.ErrNoRows) {
			// The compat id points at a tier of ANOTHER session. Re-pointing it
			// would corrupt that session's outbound ids.
			return nil, failImport(http.StatusConflict, "import.category_bound_elsewhere",
				"categoryPriceId "+strconv.FormatInt(c.CategoryPriceID, 10)+
					" is already bound to a ticket tier of a different session")
		}
		if updErr != nil {
			return nil, fmt.Errorf("update ticket tier: %w", updErr)
		}
		ordered = append(ordered, updated.ID)
		touched[updated.ID] = struct{}{}
	}

	var untouched []uuid.UUID
	for _, t := range existing {
		if _, ok := touched[t.ID]; !ok {
			untouched = append(untouched, t.ID)
		}
	}
	if len(untouched) > 0 {
		ids, err := compatids.EnsureMany(ctx, tx, compatids.KindCategoryPrice, untouched)
		if err != nil {
			return nil, fmt.Errorf("mint compat ids for untouched tiers: %w", err)
		}
		labels := make([]string, 0, len(untouched))
		for _, id := range untouched {
			labels = append(labels, strconv.FormatInt(ids[id], 10))
		}
		warnings.add(WarnTierNotInPayload,
			"the session carries ticket tiers the bundle did not mention; they were left untouched: "+
				strings.Join(labels, ", "))
	}

	return ordered, nil
}

// normalizeTierName is the case- and whitespace-insensitive key spec §3.2 step
// 5 matches tier names on. It mirrors the SQL lower(btrim(name)).
func normalizeTierName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// errCompatIDUnknown is the single answer for "this id does not resolve to an
// object of YOUR organization" (spec §5). A foreign-organization id is
// deliberately indistinguishable from a nonexistent one.
func errCompatIDUnknown(field string, id int64) error {
	return failImport(http.StatusNotFound, "import.compat_id_unknown",
		field+"="+strconv.FormatInt(id, 10)+" is not known to this organization")
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
