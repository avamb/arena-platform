// event_bundle_read.go — GET /v1/organizations/{org_id}/events/{event_id}/event-bundle
// (task 3-2, plan docs/plans/event_center_unified_plan_2026-10-14_ru.md §8.2).
//
// The reverse of POST .../imports/event-bundle: the event as it is stored,
// written in exactly the shape the import accepts, so a client can load an
// event for editing (the web event center's "edit" and "copy") with ONE
// call instead of five and send the result back. It is the server twin of
// what the Telegram bot's eventbot/wizard_load.go rebuilds client-side from
// the REST lists.
//
// The import takes one date (session) per call, so the answer holds one
// bundle per session, oldest first, each importable as is:
//
//	{ event_id, status, poster_media_id, bundles: [bundle…], skipped: […], warnings: […] }
//
// Every bundle is source=arena and addresses its rows by arena UUID
// (action.arenaEventId, actionEvent.arenaSessionId, venue.arenaVenueId,
// categoryList[].arenaTierId), so it needs no externalRef — the session
// keeps the key it was created under — and mints no compat id. Posting a
// bundle back unchanged leaves the rows as they are, and reading again
// answers the same bundles (TestEventBundleRead_RoundTrip…). That is the
// round-trip guarantee; what makes it hold is that every field is written
// the way the importer reads it:
//
//   - times are the venue's wall clock (day DD.MM.YYYY, time and endTime
//     HH:MM) plus RFC 3339 instants for the sales end, the doors time and
//     the category windows; a value the importer would read back differently
//     (seconds, a DST gap, a session of 24 hours or more) is reported in
//     warnings instead of being silently rounded;
//   - availability is the quantity the category owns (held, sold and free
//     together). In a chain the head carries the WHOLE chain's places and
//     every successor 0, which is how the importer reads a chain;
//   - nextCategoryIndex is explicit on every category (-1 = no link), the
//     sellLimit only on a linked one, and priceSchedule is always a list
//     (empty clears), so a bundle fully describes the categories it names;
//   - bigPosterUrl is left empty: an import without one keeps the stored
//     poster, which is what an edit wants. The poster's media id is the
//     top-level poster_media_id (GET /v1/media/{id} signs a URL for it).
//
// Left out on purpose: sessions that are deleted or cancelled, sessions the
// bundle cannot carry (a seating plan, a pay-what-you-want category) — each
// of the last two is listed in skipped with the reason — the open/closed flag
// of a category, compat ids, the session's external ref, and anything secret
// or about buyers (the answer reads catalog rows only).
package himports

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Codes of the sessions and notes the read leaves out or flags. They are
// stable: a client may switch on them.
const (
	// SkipSessionCancelled — the session was cancelled; a bundle cannot
	// re-open it, and the importer would only carry its moves forward.
	SkipSessionCancelled = "event_bundle.session_cancelled"
	// SkipSessionSeated — the session has a seating plan or assigned seats;
	// an arena bundle carries general admission only.
	SkipSessionSeated = "event_bundle.session_seated"
	// SkipSessionPayWhatYouWant — a category is pay-what-you-want, which a
	// bundle cannot say (the importer would turn it into a fixed price).
	SkipSessionPayWhatYouWant = "event_bundle.session_pay_what_you_want"
	// SkipSessionVenueMissing — the session's venue is deleted.
	SkipSessionVenueMissing = "event_bundle.session_venue_missing"

	// WarnBundleTimeNotRepresentable — the start or the end of the session
	// cannot be written as the bundle's wall-clock day/time exactly.
	WarnBundleTimeNotRepresentable = "event_bundle.time_not_representable"
	// WarnBundleVenueTimezone — the venue has no usable timezone, so times
	// are written in UTC.
	WarnBundleVenueTimezone = "event_bundle.venue_timezone_missing"
	// WarnBundlePromoterArchived — the event's promoter is archived; the
	// bundle leaves the promoter alone (promoterId absent) instead of naming
	// one the import would refuse.
	WarnBundlePromoterArchived = "event_bundle.promoter_archived"
	// WarnBundleChainBroken — a category hands its places to one the session
	// no longer has; the link is written as removed.
	WarnBundleChainBroken = "event_bundle.chain_link_missing"
)

// EventBundleSkipped is a session the read could not write as a bundle.
type EventBundleSkipped struct {
	SessionID uuid.UUID `json:"session_id"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
}

// EventBundleReadResponse is the 200 body of the read.
type EventBundleReadResponse struct {
	EventID uuid.UUID `json:"event_id"`
	// Status is the event's own status (draft, published, cancelled,
	// archived); a draft event is readable.
	Status string `json:"status"`
	// PosterMediaID is the event's poster; the bundles carry no bigPosterUrl.
	PosterMediaID *uuid.UUID `json:"poster_media_id"`
	// Bundles holds one importable bundle per session, oldest first. Never null.
	Bundles []bil24compat.ImportSessionRequest `json:"bundles"`
	// Skipped lists the live sessions that have no bundle, with the reason.
	// Never null.
	Skipped []EventBundleSkipped `json:"skipped"`
	// Warnings are notes about what a bundle cannot say exactly. Never null.
	Warnings []Warning `json:"warnings"`
}

// bundleSessionSource is everything the mapping needs about one session.
type bundleSessionSource struct {
	Session gen.SessionRow
	Venue   *gen.VenueRow // nil when the venue is deleted
	Tiers   []gen.TicketTierRow
	Chain   []gen.TierChainRow
	Stats   map[uuid.UUID]gaquota.Stats
}

// bundleSource is what the read loaded, already scoped to one organization.
type bundleSource struct {
	Event gen.EventRow
	// PromoterID is the promoter linked to the event, nil for none.
	PromoterID       *uuid.UUID
	PromoterArchived bool
	ChannelIDs       []uuid.UUID
	Sessions         []bundleSessionSource
	Windows          map[uuid.UUID][]gen.TicketTierPriceRow
}

// HandleEventBundleRead serves GET .../events/{event_id}/event-bundle.
func (h *Handler) HandleEventBundleRead(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, orgID) {
		return
	}

	// One snapshot for the whole read: an organizer saving in another tab
	// must not hand this answer a half-new event.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		h.readFailed(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	src, err := loadBundleSource(ctx, gen.New(tx), orgID, eventID)
	switch {
	case errors.Is(err, errBundleEventNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
		return
	case err != nil:
		h.readFailed(w, r, "load", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, buildEventBundles(src))
}

func (h *Handler) readFailed(w http.ResponseWriter, r *http.Request, step string, err error) {
	h.logger.Error("event bundle read failed", slog.String("step", step), slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"event_bundle.read_failed", "failed to read the event", r))
}

var errBundleEventNotFound = errors.New("event not found")

// loadBundleSource reads the event of orgID and everything its bundles need.
// A deleted event, an unknown one and another organization's all answer
// errBundleEventNotFound.
func loadBundleSource(ctx context.Context, q *gen.Queries, orgID, eventID uuid.UUID) (bundleSource, error) {
	ev, err := q.GetEventRaw(ctx, eventID)
	switch {
	case errors.Is(err, pgx.ErrNoRows), err == nil && (ev.OrgID != orgID || ev.DeletedAt != nil):
		return bundleSource{}, errBundleEventNotFound
	case err != nil:
		return bundleSource{}, fmt.Errorf("read event: %w", err)
	}
	src := bundleSource{Event: ev, Windows: map[uuid.UUID][]gen.TicketTierPriceRow{}}

	links, err := q.ListEventPromoters(ctx, []uuid.UUID{eventID})
	if err != nil {
		return bundleSource{}, fmt.Errorf("read event promoter: %w", err)
	}
	if ref, linked := links[eventID]; linked {
		id := ref.PromoterID
		src.PromoterID = &id
		p, err := q.GetOrgPromoter(ctx, id, orgID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			src.PromoterID = nil
		case err != nil:
			return bundleSource{}, fmt.Errorf("read promoter: %w", err)
		default:
			src.PromoterArchived = p.ArchivedAt != nil
		}
	}

	if src.ChannelIDs, err = q.ListEventPublicationChannelIDs(ctx, eventID, orgID); err != nil {
		return bundleSource{}, fmt.Errorf("read event channels: %w", err)
	}

	sessions, err := q.ListSessionsByEvent(ctx, eventID)
	if err != nil {
		return bundleSource{}, fmt.Errorf("list sessions: %w", err)
	}
	venues := map[uuid.UUID]*gen.VenueRow{}
	var tierIDs []uuid.UUID
	for _, s := range sessions {
		ss := bundleSessionSource{Session: s}
		v, seen := venues[s.VenueID]
		if !seen {
			row, vErr := q.GetVenueByID(ctx, s.VenueID)
			switch {
			case vErr == nil && row.OrgID == orgID:
				v = &row
			case vErr == nil, errors.Is(vErr, pgx.ErrNoRows):
				v = nil
			default:
				return bundleSource{}, fmt.Errorf("read venue: %w", vErr)
			}
			venues[s.VenueID] = v
		}
		ss.Venue = v
		if ss.Tiers, err = q.ListTicketTiersBySession(ctx, s.ID); err != nil {
			return bundleSource{}, fmt.Errorf("list ticket tiers: %w", err)
		}
		if ss.Chain, err = q.ListTierChainForSession(ctx, s.ID); err != nil {
			return bundleSource{}, fmt.Errorf("read category chain: %w", err)
		}
		if ss.Stats, err = gaquota.SessionStats(ctx, q, s.ID); err != nil {
			return bundleSource{}, err
		}
		for _, t := range ss.Tiers {
			tierIDs = append(tierIDs, t.ID)
		}
		src.Sessions = append(src.Sessions, ss)
	}
	if len(tierIDs) > 0 {
		windows, err := q.ListTierPriceWindows(ctx, tierIDs)
		if err != nil {
			return bundleSource{}, fmt.Errorf("read price schedules: %w", err)
		}
		for _, win := range windows {
			src.Windows[win.TierID] = append(src.Windows[win.TierID], win)
		}
	}
	return src, nil
}

// buildEventBundles is the mapping itself — a pure function of what was
// loaded, so it is unit-tested without a database.
func buildEventBundles(src bundleSource) EventBundleReadResponse {
	out := EventBundleReadResponse{
		EventID:       src.Event.ID,
		Status:        src.Event.Status,
		PosterMediaID: src.Event.PosterMediaID,
		Bundles:       []bil24compat.ImportSessionRequest{},
		Skipped:       []EventBundleSkipped{},
		Warnings:      []Warning{},
	}
	warn := func(code, format string, args ...any) {
		out.Warnings = append(out.Warnings, Warning{Code: code, Message: fmt.Sprintf(format, args...)})
	}

	var promoter *string
	switch {
	case src.PromoterID != nil && src.PromoterArchived:
		warn(WarnBundlePromoterArchived,
			"the event's promoter is archived: the bundles leave the promoter as it is (action.promoterId is absent)")
	case src.PromoterID != nil:
		id := src.PromoterID.String()
		promoter = &id
	default:
		// "" = the organization itself promotes the event.
		none := ""
		promoter = &none
	}
	channelIDs := make([]string, 0, len(src.ChannelIDs))
	for _, id := range src.ChannelIDs {
		channelIDs = append(channelIDs, id.String())
	}

	for _, ss := range src.Sessions {
		s := ss.Session
		skip := func(code, msg string) {
			out.Skipped = append(out.Skipped, EventBundleSkipped{SessionID: s.ID, Code: code, Message: msg})
		}
		switch {
		case s.Status == "cancelled":
			skip(SkipSessionCancelled, "the session is cancelled")
			continue
		case ss.Venue == nil:
			skip(SkipSessionVenueMissing, "the session's venue was deleted")
			continue
		case s.SeatingPlanVersionID != nil || s.AdmissionMode != "general_admission":
			skip(SkipSessionSeated, "the session has a seating plan: an event bundle carries general admission only")
			continue
		}
		if hasPayWhatYouWant(ss.Tiers) {
			skip(SkipSessionPayWhatYouWant, "a category is pay-what-you-want, which an event bundle cannot carry")
			continue
		}

		loc := time.UTC
		if tz := derefString(ss.Venue.Timezone); tz != "" {
			if l, err := time.LoadLocation(tz); err == nil {
				loc = l
			} else {
				warn(WarnBundleVenueTimezone, "session %s: venue timezone %q is not known, times are written in UTC", s.ID, tz)
			}
		} else {
			warn(WarnBundleVenueTimezone, "session %s: the venue has no timezone, times are written in UTC", s.ID)
		}

		ae := bil24compat.ImportSessionActionEvent{
			ArenaSessionID: s.ID.String(),
			Day:            s.StartAt.In(loc).Format("02.01.2006"), // allow:timeformat: legacy Bil24 wire format
			Time:           s.StartAt.In(loc).Format("15:04"),      // allow:timeformat: wall-clock time of the bundle
			EndTime:        s.EndAt.In(loc).Format("15:04"),        // allow:timeformat: wall-clock time of the bundle
			Currency:       s.Currency,
			SellEndTime:    s.SalesEndAt.In(loc).Format(time.RFC3339),
		}
		doors := ""
		if s.DoorsOpenAt != nil {
			doors = s.DoorsOpenAt.In(loc).Format(time.RFC3339)
		}
		ae.DoorsOpenTime = &doors
		checkBundleTimes(ae, s, loc, warn)
		checkBundleInstants(ss, src.Windows, warn)

		req := bil24compat.ImportSessionRequest{
			Source: bil24compat.SourceArena,
			Action: bil24compat.ImportSessionAction{
				ArenaEventID: src.Event.ID.String(),
				ActionName:   src.Event.Name,
				Description:  derefString(src.Event.Description),
				Age:          derefString(src.Event.AgeRating),
				PromoterID:   promoter,
			},
			ActionEvent: ae,
			Venue: bil24compat.ImportSessionVenue{
				ArenaVenueID: ss.Venue.ID.String(),
				VenueName:    ss.Venue.Name,
				Address:      derefString(ss.Venue.Address),
				Timezone:     derefString(ss.Venue.Timezone),
			},
			CategoryList: bundleCategories(ss, src.Windows, loc, warn),
			SeatList:     []bil24compat.ImportSessionSeat{},
			Publish:      src.Event.Status == "published",
			ChannelIDs:   channelIDs,
		}
		out.Bundles = append(out.Bundles, req)
	}
	return out
}

// hasPayWhatYouWant reports a category a bundle cannot represent.
func hasPayWhatYouWant(tiers []gen.TicketTierRow) bool {
	for _, t := range tiers {
		if t.PricingMode == "pwyw" {
			return true
		}
	}
	return false
}

// checkBundleTimes reads the day/time pair back with the importer's own
// parser and warns when it does not land on the stored instants.
func checkBundleTimes(ae bil24compat.ImportSessionActionEvent, s gen.SessionRow, loc *time.Location, warn func(string, string, ...any)) {
	start, err := ae.ParseLocalStart(loc)
	if err != nil || !start.Equal(s.StartAt.Truncate(time.Minute)) {
		warn(WarnBundleTimeNotRepresentable,
			"session %s: the start time cannot be written as a local day and time exactly", s.ID)
	}
	end, err := ae.ParseLocalEnd(loc)
	if err != nil || end == nil || !end.Equal(s.EndAt.Truncate(time.Minute)) {
		warn(WarnBundleTimeNotRepresentable,
			"session %s: the end time cannot be written as a local time exactly (a session of 24 hours or more, or seconds)", s.ID)
	}
}

// checkBundleInstants warns once per session when an RFC 3339 instant of its
// bundle (sales end, doors, a category window, a price window) carries a
// fraction of a second: the format has none, so the import would store the
// value cut to the whole second.
func checkBundleInstants(ss bundleSessionSource, windows map[uuid.UUID][]gen.TicketTierPriceRow, warn func(string, string, ...any)) {
	fractional := func(t *time.Time) bool { return t != nil && t.Nanosecond() != 0 }
	s := ss.Session
	bad := fractional(&s.SalesEndAt) || fractional(s.DoorsOpenAt)
	for _, t := range ss.Tiers {
		bad = bad || fractional(t.SaleWindowStart) || fractional(t.SaleWindowEnd)
		for _, w := range windows[t.ID] {
			bad = bad || fractional(&w.ValidFrom) || fractional(w.ValidTo)
		}
	}
	if bad {
		warn(WarnBundleTimeNotRepresentable,
			"session %s: a sales end, doors or sale-window time has a fraction of a second; the bundle writes whole seconds", s.ID)
	}
}

// bundleCategories writes the session's categories in their stored order.
func bundleCategories(
	ss bundleSessionSource,
	windows map[uuid.UUID][]gen.TicketTierPriceRow,
	loc *time.Location,
	warn func(string, string, ...any),
) []bil24compat.ImportSessionCategory {
	index := make(map[uuid.UUID]int, len(ss.Tiers))
	for i, t := range ss.Tiers {
		index[t.ID] = i
	}
	next := make(map[uuid.UUID]gen.TierChainRow, len(ss.Chain))
	incoming := make(map[uuid.UUID]bool, len(ss.Chain))
	for _, l := range ss.Chain {
		if _, ok := index[l.TierID]; !ok {
			continue
		}
		if _, ok := index[l.NextTierID]; !ok {
			warn(WarnBundleChainBroken,
				"session %s: category %s hands its places to a category the session no longer has", ss.Session.ID, l.TierID)
			continue
		}
		next[l.TierID] = l
		incoming[l.NextTierID] = true
	}
	quantity := func(t gen.TicketTierRow) int32 {
		if st, ok := ss.Stats[t.ID]; ok {
			return st.Quantity
		}
		if t.Capacity != nil {
			return *t.Capacity
		}
		return 0
	}
	byID := make(map[uuid.UUID]gen.TicketTierRow, len(ss.Tiers))
	for _, t := range ss.Tiers {
		byID[t.ID] = t
	}
	// chainPlaces is the hall a chain sells: the sum over its members, which
	// is what the head's availability means to the importer.
	chainPlaces := func(head uuid.UUID) int32 {
		var total int32
		seen := map[uuid.UUID]bool{}
		for cur := head; !seen[cur]; {
			seen[cur] = true
			total += quantity(byID[cur])
			l, ok := next[cur]
			if !ok {
				break
			}
			cur = l.NextTierID
		}
		return total
	}

	list := make([]bil24compat.ImportSessionCategory, 0, len(ss.Tiers))
	for _, t := range ss.Tiers {
		c := bil24compat.ImportSessionCategory{
			ArenaTierID:       t.ID.String(),
			CategoryPriceName: t.Name,
			Price:             money.Major(t.PriceAmount),
			Availability:      quantity(t),
		}
		if t.SaleWindowStart != nil {
			c.SellStartTime = t.SaleWindowStart.In(loc).Format(time.RFC3339)
		}
		if t.SaleWindowEnd != nil {
			c.SellEndTime = t.SaleWindowEnd.In(loc).Format(time.RFC3339)
		}

		minusOne := -1
		c.NextCategoryIndex = &minusOne
		if l, linked := next[t.ID]; linked {
			j := index[l.NextTierID]
			c.NextCategoryIndex = &j
			limit := int32(0)
			if l.SellLimit != nil {
				limit = *l.SellLimit
			}
			c.SellLimit = &limit
		}
		_, hasNext := next[t.ID]
		switch {
		case incoming[t.ID]:
			// A successor owns no place until the category before it hands
			// them over; the head's availability counts them.
			c.Availability = 0
		case hasNext:
			c.Availability = chainPlaces(t.ID)
		}

		schedule := make([]bil24compat.ImportPriceWindow, 0, len(windows[t.ID]))
		for _, win := range windows[t.ID] {
			iw := bil24compat.ImportPriceWindow{
				ValidFrom: win.ValidFrom.In(loc).Format(time.RFC3339),
				Price:     money.Major(win.PriceAmount),
			}
			if win.ValidTo != nil {
				iw.ValidTo = win.ValidTo.In(loc).Format(time.RFC3339)
			}
			schedule = append(schedule, iw)
		}
		c.PriceSchedule = &schedule
		list = append(list, c)
	}
	return list
}
