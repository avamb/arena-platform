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

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/seatmint"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
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
	if err := applyArenaPromoter(ctx, q, plan, eventID); err != nil {
		return importResult{}, err
	}
	if err := applyArenaEmailNote(ctx, q, plan, eventID); err != nil {
		return importResult{}, err
	}
	venueID, loc, err := h.resolveArenaVenue(ctx, q, tx, plan, match, warnings)
	if err != nil {
		return importResult{}, err
	}
	startAt, err := plan.Request.ActionEvent.ParseLocalStart(loc)
	if err != nil {
		return importResult{}, failImportAt(http.StatusUnprocessableEntity, "import.invalid_start_time",
			startField(plan.Request.ActionEvent), err.Error())
	}
	endAt, err := plan.Request.ActionEvent.ParseLocalEnd(loc)
	if err != nil {
		return importResult{}, failImportAt(http.StatusUnprocessableEntity, "import.end_time_invalid",
			"/actionEvent/endTime", err.Error())
	}
	startAt = startAt.UTC()
	finish := startAt.Add(defaultSessionDuration)
	if endAt != nil {
		finish = endAt.UTC()
	}

	// Task S1b: a new session may be created from a seating plan version
	// (import_arena_seated.go); an existing one keeps its plan.
	seated, err := resolveArenaSeating(ctx, q, plan, match, venueID)
	if err != nil {
		return importResult{}, err
	}

	sessionID, created, err := h.upsertArenaSession(ctx, q, tx, plan, match, eventID, venueID, startAt, finish, seated, warnings)
	if err != nil {
		return importResult{}, err
	}
	if !match.RefBound {
		if err := q.InsertSessionExternalRef(ctx, plan.OrgID, plan.ExternalRef, sessionID); err != nil {
			return importResult{}, arenaRefInsertError(err, plan.ExternalRef)
		}
	}

	orderedTiers, cats, err := h.upsertArenaTiers(ctx, q, tx, plan, sessionID, seated.seatedEntries(), warnings)
	if err != nil {
		return importResult{}, err
	}
	if seated != nil {
		if err := materializeArenaSeating(ctx, q, eventID, sessionID, seated, orderedTiers); err != nil {
			return importResult{}, err
		}
	}
	if err := refuseSeatedChainLinks(ctx, q, plan, sessionID, orderedTiers); err != nil {
		return importResult{}, err
	}

	// Plan 08_architecture/23 step 7 — the same category-quota rules the
	// Bil24-format importer follows. A category without places mints its own
	// General Admission places; a category whose places are plan seats is
	// left alone (gaquota.CategoryKind), and a plan GA zone already owns the
	// places seatmint wrote, so the bundle's availability only resizes it.
	chain, err := applyArenaCategoryExtras(ctx, q, plan, sessionID, orderedTiers)
	if err != nil {
		return importResult{}, err
	}
	if err := syncImportedCategoryQuotas(ctx, q, sessionID, cats, true, true, chain, warnings); err != nil {
		return importResult{}, err
	}
	// A quantity step selling now owns exactly its limit; the rest of the hall
	// waits in the next, still closed category (migration 0114).
	if _, err := gaquota.RebalanceSession(ctx, q, sessionID); err != nil {
		return importResult{}, fmt.Errorf("apply category sell limits: %w", err)
	}

	if err := h.syncInventoryLedger(ctx, q, eventID, sessionID); err != nil {
		return importResult{}, err
	}
	seatingOut, err := arenaSeatingOutcome(ctx, q, eventID, sessionID)
	if err != nil {
		return importResult{}, err
	}
	var publishedNow bool
	if plan.Request.Publish {
		publishedNow, err = h.applyPublish(ctx, q, plan, eventID, sessionID, warnings)
		if err != nil {
			return importResult{}, err
		}
	}

	// Same channel binding as the bil24 path — an arena-native bundle posted by
	// the site's own key must reach the site just as reliably (feature #536).
	publications, err := h.ensureChannelPublication(ctx, q, plan, eventID, venueID, warnings)
	if err != nil {
		return importResult{}, err
	}

	// A dry run mints no compat id: the sequence would move for rows that
	// are rolled back, and the ids would mean nothing to the caller.
	if plan.Mode.DryRun {
		return importResult{EventID: eventID, SessionID: sessionID, Created: created, Seating: seatingOut}, nil
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
		Seating:      seatingOut,
		CompatIDs:    compat,
		PublishedNow: publishedNow,
		Publication:  firstPublication(publications),
		Publications: publications,
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

	if plan.ExternalRef != "" {
		byRef, _, err := q.GetSessionByExternalRef(ctx, plan.OrgID, plan.ExternalRef)
		switch {
		case err == nil:
			m.SessionID = byRef
			m.RefBound = true
		case !errors.Is(err, pgx.ErrNoRows):
			return m, fmt.Errorf("lookup session by external ref: %w", err)
		}
	}

	// An event center addresses the session it is editing by its arena UUID
	// (the id it listed a moment ago); like venue.arenaVenueId, a UUID that
	// is not this organization's is reported as invalid, never as foreign.
	if raw := trimSpace(ae.ArenaSessionID); raw != "" {
		sessionID, err := uuid.Parse(raw)
		if err != nil {
			return m, failImportAt(http.StatusUnprocessableEntity, "import.invalid_session", "/actionEvent/arenaSessionId",
				"actionEvent.arenaSessionId "+raw+" is not a UUID")
		}
		sctx, err := q.GetSessionImportContext(ctx, sessionID)
		switch {
		case errors.Is(err, pgx.ErrNoRows), err == nil && sctx.OrgID != plan.OrgID:
			return m, failImportAt(http.StatusUnprocessableEntity, "import.invalid_session", "/actionEvent/arenaSessionId",
				"actionEvent.arenaSessionId "+raw+" is not a session of this organization")
		case err != nil:
			return m, fmt.Errorf("read session %s: %w", sessionID, err)
		}
		if m.SessionID != uuid.Nil && m.SessionID != sessionID {
			return m, failImportAt(http.StatusConflict, "import.external_ref_conflict", "/externalRef",
				"externalRef "+plan.ExternalRef+" is bound to a different session than actionEvent.arenaSessionId "+raw)
		}
		m.SessionID = sessionID
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
			return m, failImportAt(http.StatusConflict, "import.external_ref_conflict", "/externalRef",
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
			return m, failImportAt(http.StatusConflict, "import.session_deleted", "/externalRef",
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

	if !m.RefBound && plan.ExternalRef == "" {
		// Addressed by arenaSessionId without a key: the session keeps the
		// key it was created under (or stays without one).
		m.RefBound = true
	}
	if !m.RefBound {
		existing, refErr := q.GetExternalRefBySession(ctx, m.SessionID)
		switch {
		case refErr == nil:
			if existing != plan.ExternalRef {
				return m, failImportAt(http.StatusConflict, "import.external_ref_conflict", "/externalRef",
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
		return failImportAt(http.StatusConflict, "import.external_ref_conflict", "/externalRef",
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
	desc, err := importDescription(plan.Source, a)
	if err != nil {
		return uuid.Nil, err
	}
	eventID, err := h.findArenaEvent(ctx, q, tx, plan, m)
	if err != nil {
		return uuid.Nil, err
	}
	if eventID == uuid.Nil {
		created, err := q.InsertEvent(ctx, plan.OrgID, a.Name(), desc, "draft", "public", nil, richtext.FormatART)
		if err != nil {
			return uuid.Nil, fmt.Errorf("insert event: %w", err)
		}
		eventID = created.ID
		if err := assignEventSlug(ctx, q, plan.OrgID, eventID, a.Name()); err != nil {
			return uuid.Nil, err
		}
	}

	if err := upgradeEventDescriptions(ctx, q, plan.OrgID, eventID, desc); err != nil {
		return uuid.Nil, err
	}
	if err := q.SetEventImportMetadata(ctx, eventID, plan.OrgID, a.Name(), desc, optString(a.Age), richtext.FormatART); err != nil {
		return uuid.Nil, fmt.Errorf("update event metadata: %w", err)
	}
	if plan.PosterMediaID != nil {
		if err := q.SetEventPosterMediaID(ctx, eventID, plan.OrgID, plan.PosterMediaID); err != nil {
			return uuid.Nil, fmt.Errorf("attach event poster: %w", err)
		}
	}
	return eventID, nil
}

// findArenaEvent finds the existing event the bundle addresses — by
// action.arenaEventId, by action.actionId, or as the addressed session's own
// event. uuid.Nil means the import creates one.
func (h *Handler) findArenaEvent(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, m arenaMatch) (uuid.UUID, error) {
	a := plan.Request.Action
	var eventID uuid.UUID

	switch {
	case trimSpace(a.ArenaEventID) != "":
		// An event center addresses the event by the UUID it listed; a UUID
		// that is not this organization's is invalid, never foreign.
		raw := trimSpace(a.ArenaEventID)
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return uuid.Nil, failImportAt(http.StatusUnprocessableEntity, "import.invalid_event", "/action/arenaEventId",
				"action.arenaEventId "+raw+" is not a UUID")
		}
		row, err := q.GetEventRaw(ctx, parsed)
		switch {
		case errors.Is(err, pgx.ErrNoRows), err == nil && row.OrgID != plan.OrgID:
			return uuid.Nil, failImportAt(http.StatusUnprocessableEntity, "import.invalid_event", "/action/arenaEventId",
				"action.arenaEventId "+raw+" is not an event of this organization")
		case err != nil:
			return uuid.Nil, fmt.Errorf("read event %s: %w", parsed, err)
		}
		eventID = parsed
		if m.SessionID != uuid.Nil && m.Ctx.EventID != eventID {
			return uuid.Nil, failImportAt(http.StatusConflict, "import.action_mismatch", "/action/arenaEventId",
				"the addressed session belongs to a different event than action.arenaEventId "+raw)
		}
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
			return uuid.Nil, failImportAt(http.StatusConflict, "import.action_mismatch", "/action/actionId",
				"the addressed session belongs to a different action than actionId "+
					strconv.FormatInt(a.ActionID, 10))
		}
	case m.SessionID != uuid.Nil:
		eventID = m.Ctx.EventID
	}
	return eventID, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Spec §3.2 step 4 — venue
// ─────────────────────────────────────────────────────────────────────────────

// resolveArenaVenue finds or creates the venue and returns the location the
// payload's local day/time must be interpreted in. planArenaVenue decides;
// this function performs the writes the decision calls for.
func (h *Handler) resolveArenaVenue(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, m arenaMatch, warnings *warningSink) (uuid.UUID, *time.Location, error) {
	v := plan.Request.Venue
	d, err := h.planArenaVenue(ctx, q, tx, plan, m, warnings)
	if err != nil {
		return uuid.Nil, nil, err
	}
	switch d.action {
	case venueRefresh:
		cityID, country, geoErr := h.resolveGeography(ctx, q, tx, plan, warnings)
		if geoErr != nil {
			return uuid.Nil, nil, geoErr
		}
		if err := q.UpdateImportedVenueGeography(ctx, d.venueID, cityID, optString(v.Address), d.storeTZ, v.GeoLat, v.GeoLon, country); err != nil {
			return uuid.Nil, nil, fmt.Errorf("update venue geography: %w", err)
		}
		return d.venueID, d.loc, nil
	case venueCreate:
		cityID, country, geoErr := h.resolveGeography(ctx, q, tx, plan, warnings)
		if geoErr != nil {
			return uuid.Nil, nil, geoErr
		}
		created, err := q.InsertArenaVenue(ctx, plan.OrgID, cityID, trimSpace(v.VenueName), optString(v.Address), d.storeTZ, v.GeoLat, v.GeoLon, country)
		if err != nil {
			return uuid.Nil, nil, fmt.Errorf("insert venue: %w", err)
		}
		return created.ID, d.loc, nil
	default:
		return d.venueID, d.loc, nil
	}
}

// venueAction is what resolveArenaVenue does with the venue planArenaVenue
// picked.
type venueAction int

const (
	// venueUse reuses an existing venue as it is.
	venueUse venueAction = iota
	// venueRefresh reuses the venue addressed by its compat id and refreshes
	// its geography from the payload.
	venueRefresh
	// venueCreate creates a new venue from the payload.
	venueCreate
)

// venueDecision is planArenaVenue's answer. venueID is uuid.Nil for
// venueCreate; storeTZ is the zone to write to venues.timezone ("" = keep).
type venueDecision struct {
	action  venueAction
	venueID uuid.UUID
	loc     *time.Location
	storeTZ string
}

// planArenaVenue decides, reading only, which venue the bundle means and the
// location its local day/time is interpreted in.
//
// A venue matched BY NAME is reused as is: the bundle only proves the operator
// typed the same name, not that it may overwrite an address, geo point or
// timezone that other events already depend on. A venue addressed by its
// compat id is the caller's own and its geography is refreshed.
func (h *Handler) planArenaVenue(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, m arenaMatch, warnings *warningSink) (venueDecision, error) {
	v := plan.Request.Venue
	payloadTZ := trimSpace(v.Timezone)
	name := trimSpace(v.VenueName)

	// An event center picks the venue from the organization's own list: use
	// it as it is, geography included — the pick is not an edit.
	if raw := trimSpace(v.ArenaVenueID); raw != "" {
		venueID, err := uuid.Parse(raw)
		if err != nil {
			return venueDecision{}, failImportAt(http.StatusUnprocessableEntity, "import.invalid_venue", "/venue/arenaVenueId",
				"venue.arenaVenueId "+raw+" is not a UUID")
		}
		vctx, err := q.GetVenueImportContext(ctx, venueID)
		switch {
		case errors.Is(err, pgx.ErrNoRows), err == nil && vctx.OrgID != plan.OrgID:
			return venueDecision{}, failImportAt(http.StatusUnprocessableEntity, "import.invalid_venue", "/venue/arenaVenueId",
				"venue.arenaVenueId "+raw+" is not a venue of this organization")
		case err != nil:
			return venueDecision{}, fmt.Errorf("read venue %s: %w", venueID, err)
		}
		loc, _, err := arenaLocation(derefString(vctx.Timezone), payloadTZ, warnings)
		if err != nil {
			return venueDecision{}, err
		}
		return venueDecision{action: venueUse, venueID: venueID, loc: loc}, nil
	}

	if v.VenueID > 0 {
		venueID, err := compatids.Resolve(ctx, tx, compatids.KindVenue, v.VenueID)
		switch {
		case errors.Is(err, compatids.ErrNotFound):
			return venueDecision{}, errCompatIDUnknown("venue.venueId", v.VenueID)
		case err != nil:
			return venueDecision{}, fmt.Errorf("resolve venue compat id: %w", err)
		}
		vctx, err := q.GetVenueImportContext(ctx, venueID)
		switch {
		case errors.Is(err, pgx.ErrNoRows), err == nil && vctx.OrgID != plan.OrgID:
			return venueDecision{}, errCompatIDUnknown("venue.venueId", v.VenueID)
		case err != nil:
			return venueDecision{}, fmt.Errorf("read venue %s: %w", venueID, err)
		}
		loc, store, err := arenaLocation(derefString(vctx.Timezone), payloadTZ, warnings)
		if err != nil {
			return venueDecision{}, err
		}
		return venueDecision{action: venueRefresh, venueID: venueID, loc: loc, storeTZ: store}, nil
	}

	if name != "" {
		existing, err := q.FindActiveVenueByNormalizedName(ctx, plan.OrgID, name)
		switch {
		case err == nil:
			vctx, ctxErr := q.GetVenueImportContext(ctx, existing.ID)
			if ctxErr != nil {
				return venueDecision{}, fmt.Errorf("read venue %s: %w", existing.ID, ctxErr)
			}
			warnings.add(WarnVenueMatchedByName,
				"venue \""+name+"\" matched an existing venue of this organization and was reused unchanged; "+
					"send venue.venueId to address it explicitly")
			loc, _, locErr := arenaLocation(derefString(vctx.Timezone), payloadTZ, warnings)
			if locErr != nil {
				return venueDecision{}, locErr
			}
			return venueDecision{action: venueUse, venueID: existing.ID, loc: loc}, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return venueDecision{}, fmt.Errorf("lookup venue by name: %w", err)
		}
	}

	// Nothing to match against. An edit that simply omits the venue block
	// keeps the session where it is rather than failing.
	if name == "" {
		if m.SessionID != uuid.Nil {
			vctx, err := q.GetVenueImportContext(ctx, m.Ctx.VenueID)
			if err != nil {
				return venueDecision{}, fmt.Errorf("read venue %s: %w", m.Ctx.VenueID, err)
			}
			loc, _, locErr := arenaLocation(derefString(vctx.Timezone), payloadTZ, warnings)
			if locErr != nil {
				return venueDecision{}, locErr
			}
			return venueDecision{action: venueUse, venueID: m.Ctx.VenueID, loc: loc}, nil
		}
		return venueDecision{}, failImportAt(http.StatusUnprocessableEntity, "import.venue_name_required", "/venue/venueName",
			"venue.venueName is required when venue.venueId is not supplied")
	}

	loc, store, err := arenaLocation("", payloadTZ, warnings)
	if err != nil {
		return venueDecision{}, err
	}
	return venueDecision{action: venueCreate, loc: loc, storeTZ: store}, nil
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
		return nil, "", failImportAt(http.StatusUnprocessableEntity, "venue.timezone_required", "/venue/timezone",
			"venue.timezone is required (IANA zone name) when the venue is not yet known to arena")
	}
	loc, err := time.LoadLocation(payloadTZ)
	if err != nil {
		return nil, "", failImportAt(http.StatusUnprocessableEntity, "venue.timezone_required", "/venue/timezone",
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
	tx pgx.Tx,
	plan importPlan,
	m arenaMatch,
	eventID, venueID uuid.UUID,
	startAt, endAt time.Time,
	seated *arenaSeatedPlan,
	warnings *warningSink,
) (uuid.UUID, bool, error) {
	capacity := plan.Request.TotalAvailability()

	if m.SessionID == uuid.Nil {
		if seated != nil {
			// A session created from a plan starts with the plan's declared
			// capacity; the places written next make it exact (Recompute).
			capacity = seatmint.PlanCapacity(seated.mode, seated.version)
		}
		if capacity <= 0 {
			return uuid.Nil, false, errCapacityRequired()
		}
		status := "draft"
		if plan.Request.Publish {
			status = "scheduled"
		}
		created, err := q.InsertSession(ctx, eventID, venueID, startAt, endAt, capacity, nil, status, nil, plan.Currency, "override")
		if err != nil {
			return uuid.Nil, false, fmt.Errorf("insert session: %w", err)
		}
		if err := applyImportSaleTimes(ctx, q, plan, created.ID, eventID, startAt); err != nil {
			return uuid.Nil, false, err
		}
		// The places themselves are minted per CATEGORY once the bundle's
		// categoryList has been upserted (materializeArenaCategoryPlaces).
		// Before migration 0101 this spot materialized one fungible
		// "ga|pool|<n>" batch with tier_id NULL; a category now owns its
		// places, and such a pool would be unsellable.
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
	// A session bound to a seating plan sizes itself from its places
	// (syncInventoryLedger → gaquota.Recompute); the bundle's availability sum
	// says nothing about its seats, so it is not written even for a moment.
	if capacity > 0 && m.Ctx.AdmissionMode == "general_admission" {
		capacityPtr = &capacity
	}
	status := ""
	if plan.Request.Publish && m.Ctx.Status == "draft" {
		status = "scheduled"
	}
	before, err := sessionchange.Load(ctx, tx, m.SessionID, true)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("lock session: %w", err)
	}
	if _, err := q.UpdateSession(ctx, m.SessionID, eventID, &venueID, &startAt, &endAt, capacityPtr, nil, status, nil, &currency, "override"); err != nil {
		return uuid.Nil, false, fmt.Errorf("update session: %w", err)
	}
	if err := applyImportSaleTimes(ctx, q, plan, m.SessionID, eventID, startAt); err != nil {
		return uuid.Nil, false, err
	}
	if err := applySessionChange(ctx, tx, plan, before); err != nil {
		return uuid.Nil, false, err
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
//
// seatedEntries marks the entries that price a seated plan category of a
// session being created from a seating plan (task S1b): their new tier
// carries no quantity, because its places are the plan's seats.
func (h *Handler) upsertArenaTiers(
	ctx context.Context,
	q *gen.Queries,
	tx pgx.Tx,
	plan importPlan,
	sessionID uuid.UUID,
	seatedEntries map[int]bool,
	warnings *warningSink,
) ([]uuid.UUID, []importedCategory, error) {
	existing, err := q.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("list ticket tiers: %w", err)
	}
	byName := make(map[string]gen.TicketTierRow, len(existing))
	for _, t := range existing {
		key := normalizeTierName(t.Name)
		if _, dup := byName[key]; !dup {
			byName[key] = t
		}
	}

	ordered := make([]uuid.UUID, 0, len(plan.Request.CategoryList))
	cats := make([]importedCategory, 0, len(plan.Request.CategoryList))
	touched := make(map[uuid.UUID]struct{}, len(plan.Request.CategoryList))

	for i, c := range plan.Request.CategoryList {
		name := trimSpace(c.CategoryPriceName)
		if name == "" {
			name = "Category " + strconv.Itoa(i+1)
		}
		price := c.PriceMinorUnits()
		if err := checkCategoryPrice(i, price); err != nil {
			return nil, nil, err
		}
		mode := "fixed"
		if price == 0 {
			mode = "free"
		}
		var capacity *int32
		if c.Availability > 0 && !seatedEntries[i] {
			cap32 := c.Availability
			capacity = &cap32
		}
		sortOrder := int32(i) //nolint:gosec // categoryList length is bounded by the request body cap
		// The bundle's actionEvent.sellEndTime is the SESSION's sales end
		// (migration 0128): it is not copied onto the categories any more,
		// or extending the session's sale later would leave every category
		// closing at the old time. A category's own sellEndTime still sets
		// its own window.
		saleStart, saleEnd, winErr := categorySaleWindow(i, c, plan.SaleWindowStart)
		if winErr != nil {
			return nil, nil, winErr
		}

		target, refErr := arenaTierRef(ctx, tx, i, c, existing)
		if refErr != nil {
			return nil, nil, refErr
		}
		if trimSpace(c.ArenaTierID) == "" && c.CategoryPriceID <= 0 {
			if match, ok := byName[normalizeTierName(name)]; ok {
				target = match.ID
			}
		}

		if target == uuid.Nil {
			row, insErr := q.InsertTicketTier(ctx, sessionID, name, mode, price, plan.Currency, nil, nil, capacity,
				saleStart, saleEnd, sortOrder)
			if insErr != nil {
				return nil, nil, fmt.Errorf("insert ticket tier: %w", insErr)
			}
			ordered = append(ordered, row.ID)
			cats = append(cats, importedCategory{TierID: row.ID, Availability: c.Availability, Created: true})
			touched[row.ID] = struct{}{}
			byName[normalizeTierName(name)] = row
			continue
		}

		// capacity is deliberately NOT passed on the UPDATE: a repeat import
		// never changes a category's quantity (plan 08_architecture/23
		// decision 7) — price, sale window and sort order do keep updating.
		updated, updErr := q.UpdateTicketTier(ctx, target, sessionID, name, mode, &price, plan.Currency, nil, nil, nil,
			saleStart, saleEnd, &sortOrder)
		if errors.Is(updErr, pgx.ErrNoRows) {
			// The compat id points at a tier of ANOTHER session. Re-pointing it
			// would corrupt that session's outbound ids.
			return nil, nil, failImportAt(http.StatusConflict, "import.category_bound_elsewhere",
				fmt.Sprintf("/categoryList/%d/categoryPriceId", i),
				"categoryPriceId "+strconv.FormatInt(c.CategoryPriceID, 10)+
					" is already bound to a ticket tier of a different session")
		}
		if updErr != nil {
			return nil, nil, fmt.Errorf("update ticket tier: %w", updErr)
		}
		ordered = append(ordered, updated.ID)
		cats = append(cats, importedCategory{TierID: updated.ID, Availability: c.Availability})
		touched[updated.ID] = struct{}{}
	}

	var untouched []uuid.UUID
	for _, t := range existing {
		if _, ok := touched[t.ID]; !ok {
			untouched = append(untouched, t.ID)
		}
	}
	if len(untouched) > 0 {
		labels, err := untouchedTierLabels(ctx, q, tx, plan, untouched)
		if err != nil {
			return nil, nil, err
		}
		warnings.add(WarnTierNotInPayload,
			"the session carries ticket tiers the bundle did not mention; they were closed "+
				"(never deleted) and keep every place they already sold: "+
				strings.Join(labels, ", "))
	}

	return ordered, cats, nil
}

// untouchedTierLabels names the session's categories the bundle did not
// mention by their compat ids, minting the missing ones. A dry run mints
// nothing: a category without an id yet is named by its arena UUID.
func untouchedTierLabels(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, untouched []uuid.UUID) ([]string, error) {
	labels := make([]string, 0, len(untouched))
	if plan.Mode.DryRun {
		rows, err := q.ListCompatibilityIDsByPlatformIDs(ctx, string(compatids.KindCategoryPrice), untouched)
		if err != nil {
			return nil, fmt.Errorf("read compat ids of untouched tiers: %w", err)
		}
		known := make(map[uuid.UUID]int64, len(rows))
		for _, r := range rows {
			known[r.PlatformID] = r.SystemID
		}
		for _, id := range untouched {
			if sys, ok := known[id]; ok {
				labels = append(labels, strconv.FormatInt(sys, 10))
			} else {
				labels = append(labels, id.String())
			}
		}
		return labels, nil
	}
	ids, err := compatids.EnsureMany(ctx, tx, compatids.KindCategoryPrice, untouched)
	if err != nil {
		return nil, fmt.Errorf("mint compat ids for untouched tiers: %w", err)
	}
	for _, id := range untouched {
		labels = append(labels, strconv.FormatInt(ids[id], 10))
	}
	return labels, nil
}

// categorySaleWindow is a category's own sale window (its sellStartTime /
// sellEndTime over the session's sales start), refused when it does not
// parse or ends before it starts.
func categorySaleWindow(i int, c bil24compat.ImportSessionCategory, sessionStart *time.Time) (*time.Time, *time.Time, error) {
	start, end, err := c.ParseSellWindow(sessionStart, nil)
	if err != nil {
		field := fmt.Sprintf("/categoryList/%d/sellEndTime", i)
		if strings.HasPrefix(err.Error(), "sellStartTime") {
			field = fmt.Sprintf("/categoryList/%d/sellStartTime", i)
		}
		return nil, nil, failImportAt(http.StatusUnprocessableEntity, "import.invalid_sale_window", field,
			fmt.Sprintf("categoryList[%d]: %s", i, err.Error()))
	}
	return start, end, nil
}

// arenaTierRef resolves the category a categoryList entry addresses
// explicitly — by arenaTierId among the session's categories (existing), or
// by its compat categoryPriceId. uuid.Nil when the entry addresses none.
func arenaTierRef(ctx context.Context, tx pgx.Tx, i int, c bil24compat.ImportSessionCategory, existing []gen.TicketTierRow) (uuid.UUID, error) {
	switch {
	case trimSpace(c.ArenaTierID) != "":
		// An event center addresses the category it is editing by the UUID
		// it listed, so a renamed category is updated, not minted anew.
		raw := trimSpace(c.ArenaTierID)
		field := fmt.Sprintf("/categoryList/%d/arenaTierId", i)
		parsed, parseErr := uuid.Parse(raw)
		if parseErr != nil {
			return uuid.Nil, failImportAt(http.StatusUnprocessableEntity, "import.invalid_category", field,
				fmt.Sprintf("categoryList[%d].arenaTierId %s is not a UUID", i, raw))
		}
		for _, t := range existing {
			if t.ID == parsed {
				return parsed, nil
			}
		}
		return uuid.Nil, failImportAt(http.StatusUnprocessableEntity, "import.invalid_category", field,
			fmt.Sprintf("categoryList[%d].arenaTierId %s is not a category of this session", i, raw))
	case c.CategoryPriceID > 0:
		resolved, resErr := compatids.Resolve(ctx, tx, compatids.KindCategoryPrice, c.CategoryPriceID)
		switch {
		case errors.Is(resErr, compatids.ErrNotFound):
			return uuid.Nil, errCompatIDUnknown(fmt.Sprintf("categoryList[%d].categoryPriceId", i), c.CategoryPriceID)
		case resErr != nil:
			return uuid.Nil, fmt.Errorf("resolve category compat id: %w", resErr)
		}
		return resolved, nil
	}
	return uuid.Nil, nil
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
	return failImportAt(http.StatusNotFound, "import.compat_id_unknown", dottedToPointer(field),
		field+"="+strconv.FormatInt(id, 10)+" is not known to this organization")
}

// dottedToPointer turns the dotted field name used in error messages
// ("categoryList[2].categoryPriceId") into its RFC 6901 JSON pointer
// ("/categoryList/2/categoryPriceId").
func dottedToPointer(field string) string {
	r := strings.NewReplacer("[", ".", "]", "")
	return "/" + strings.ReplaceAll(r.Replace(field), ".", "/")
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// applyArenaPromoter applies action.promoterId (migration 0113) inside the
// import transaction: absent/null keeps the event's promoter, "" removes the
// link (the organization itself is the promoter), a UUID links one of the
// organization's ACTIVE promoters. Anything else is 422
// import.invalid_promoter — the same rule as
// PUT /v1/organizations/{org_id}/events/{id}/promoter (hcatalog.SetEventPromoterTx).
func applyArenaPromoter(ctx context.Context, q *gen.Queries, plan importPlan, eventID uuid.UUID) error {
	change, err := resolveArenaPromoter(ctx, q, plan)
	if err != nil || !change.set {
		return err
	}
	if change.promoterID == uuid.Nil {
		if err := q.DeleteEventPromoter(ctx, eventID, plan.OrgID); err != nil {
			return fmt.Errorf("clear event promoter: %w", err)
		}
		return nil
	}
	if err := q.SetEventPromoter(ctx, eventID, plan.OrgID, change.promoterID); err != nil {
		return fmt.Errorf("set event promoter: %w", err)
	}
	return nil
}

// promoterChange is what action.promoterId asks for: nothing (set false),
// removing the link (promoterID uuid.Nil) or linking one promoter.
type promoterChange struct {
	set        bool
	promoterID uuid.UUID
}

// resolveArenaPromoter reads action.promoterId and checks, reading only, that
// a named promoter is an active one of the organization.
func resolveArenaPromoter(ctx context.Context, q *gen.Queries, plan importPlan) (promoterChange, error) {
	raw := plan.Request.Action.PromoterID
	if raw == nil {
		return promoterChange{}, nil
	}
	value := strings.TrimSpace(*raw)
	if value == "" {
		return promoterChange{set: true}, nil
	}
	invalid := failImportAt(http.StatusUnprocessableEntity, "import.invalid_promoter", "/action/promoterId",
		"action.promoterId is not an active promoter of this organization")
	promoterID, err := uuid.Parse(value)
	if err != nil {
		return promoterChange{}, invalid
	}
	p, err := q.GetOrgPromoter(ctx, promoterID, plan.OrgID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return promoterChange{}, invalid
	case err != nil:
		return promoterChange{}, fmt.Errorf("read promoter %s: %w", promoterID, err)
	case p.ArchivedAt != nil:
		return promoterChange{}, invalid
	}
	return promoterChange{set: true, promoterID: p.ID}, nil
}
