// cmd_catalog.go — Bil24-compatible catalog reads: GET_ALL_ACTIONS and
// GET_SEAT_LIST (plus the GA / per-unit branches and the shared
// bssStatusCode helper). Extracted from bil24_compat.go by feature #476
// to keep every per-command file under ~700 lines.
//
// GET_SCHEMA lives alongside these as schema.go — its file already
// existed before the split. The dispatcher (HandleBil24Command) stays in
// bil24_compat.go so its case-list keeps one central home.
package hbil24

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

// ─────────────────────────────────────────────────────────────────────────────
// GET_ALL_ACTIONS — list published events (GetCatalog)
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24GetAllActions maps GET_ALL_ACTIONS to the platform event catalog.
//
// Bil24 request fields used:
//   - locale: controls the language of event names/descriptions
//
// Response: { "resultCode": 0, "command": "GET_ALL_ACTIONS", "actionList": [...] }
// Each action item:
//
//	{
//	  "actionId":       "<uuid>",
//	  "actionName":     "...",
//	  "bigPosterUrl":   "...",
//	  "firstEventDate": "<RFC3339>"
//	}
func (h *Handler) handleBil24GetAllActions(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if h.eventQueries == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "catalog service unavailable",
		))
		return
	}

	// Feature #471 (W1-A1b, spec §5 / §7.1): resolve the fid → channel row
	// and enforce fid+token when requireToken=true. GET_ALL_ACTIONS returns
	// only the caller's own organization's published events; without a
	// resolved channel we cannot filter, so an unresolved fid under
	// requireToken=true is a hard -4. When requireToken=false and no fid
	// resolves, we fall back to the pre-W1 catalog for legacy compatibility.
	ctx := r.Context()
	channel, authed := h.authenticateCommand(ctx, w, req)
	if h.requireToken && !authed {
		return // envelope already written
	}

	locale := req.Locale
	if locale == "" {
		locale = "en"
	}

	var events []gen.EventRow
	var err error
	if authed {
		events, err = h.eventQueries.ListEventsByOrg(ctx, channel.OrgID, locale)
	} else {
		events, err = h.eventQueries.ListEvents(ctx, locale, "public")
	}
	if err != nil {
		h.logger.Error("bil24_compat: GET_ALL_ACTIONS: list events failed",
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to retrieve action list",
		))
		return
	}

	// Spec §7.1 (feature #476, W1-A2b slice 15): emit the countryList /
	// cityList / venueList tree when the caller is authenticated and the
	// SQL prep from slice 14 (ListActionVenuesByOrg) is wired. The tree
	// walks distinct (country → city → venue) triples for venues that host
	// at least one non-deleted session of a published event owned by the
	// caller's org. The nested venue entries carry venueId + venueName
	// only in this slice — spec-final address / geoLat / geoLon fields
	// will land in a follow-up slice that widens ListActionVenuesByOrg to
	// project them.
	//
	// The unauthed fallback (nil authed channel) keeps countryList /
	// cityList as empty arrays: the pre-W1 catalog is org-agnostic and
	// there is no venue-tree source for it. Emitting empty arrays keeps
	// the wire shape stable across the two branches so downstream JSON
	// consumers do not need to key-guard.
	countryList := make([]map[string]any, 0)
	cityList := make([]map[string]any, 0)
	if authed {
		venueRows, verr := h.eventQueries.ListActionVenuesByOrg(ctx, channel.OrgID, locale)
		if verr != nil {
			h.logger.Error("bil24_compat: GET_ALL_ACTIONS: list action venues failed",
				slog.String("org_id", channel.OrgID.String()),
				slog.String("error", verr.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "failed to retrieve action list",
			))
			return
		}
		countryList, cityList = h.buildCountryCityLists(ctx, venueRows)
	}

	// Spec §7.1 (feature #476 slice 19): organizerId +
	// organizerName. The catalog is org-scoped in the authed branch, so
	// every actionList entry carries the SAME organizer — we look it up
	// once here and pass it into buildActionEntry rather than joining it
	// per row. A lookup failure logs and degrades gracefully to the
	// pre-slice shape (both organizer keys omitted); the response is
	// still useful without the organizer chip.
	var (
		organizerID   int64
		organizerName string
	)
	if authed {
		org, oerr := h.eventQueries.GetOrganizationByID(ctx, channel.OrgID)
		if oerr != nil {
			h.logger.Warn("bil24_compat: GET_ALL_ACTIONS: org lookup failed; omitting organizerId/organizerName",
				slog.String("org_id", channel.OrgID.String()),
				slog.String("error", oerr.Error()),
			)
		} else {
			organizerID = org.DisplayNumber
			organizerName = org.Name
		}
	}

	actionList := make([]map[string]any, 0, len(events))
	for _, e := range events {
		// Spec §7.1: only status='published' events appear in the catalog.
		// The org-scoped list query is not visibility/status-filtered so we
		// filter here — the extra field is already on the row.
		if authed && e.Status != "published" {
			continue
		}
		actionList = append(actionList, h.buildActionEntry(ctx, e, organizerID, organizerName))
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"countryList": countryList,
		"cityList":    cityList,
		"actionList":  actionList,
	}))
}

// buildActionEntry projects one gen.EventRow into a single spec §7.1
// actionList entry. Extracted from handleBil24GetAllActions in slice 17
// so the field-by-field expansion of the entry body (per spec §7.1) can
// be unit-tested without spinning up the full handler with mock query
// services.
//
// Fields emitted from EventRow columns (all directly present on the row
// projected by ListEventsByOrg — no extra SQL round-trip):
//
//   - actionId       — int64 via compatActionID (UUID fallback for
//     nil-compatDB unit tests).
//   - actionName     — e.Name.
//   - firstEventDate — e.FirstSessionAt (earliest scheduled session,
//     RFC3339 UTC). Omitted when nil.
//   - lastEventDate  — e.LastSessionAt (latest scheduled session,
//     RFC3339 UTC). Omitted when nil. Spec §7.1 (feature #476 slice 17).
//   - age            — e.AgeRating with the "NR" sentinel normalised to
//     "" (spec §7.1: age string, "NR" → ""). Omitted when the column is
//     nil OR when the normalised value is empty (omit rather than empty).
//   - bigPosterUrl / smallPosterUrl — resolved cover URL. Preference
//     order (spec §7.1, feature #476 slice 18):
//     1. events.poster_media_id → /v1/media-files/{uuid} (AB-47b/c;
//     same URL shape hfeed's mediaFileURL emits so the WP plugin
//     and public feed agree on the artwork host).
//     2. legacy events.image_url — literal URL passthrough for events
//     migrated before AB-47 landed the poster_media_id column.
//     Both keys carry the same URL in this wave — sizing (thumb vs
//     hero) is deferred until media_objects grows a variants surface.
//   - description    — e.Description raw HTML.
//
// Fields deferred to later slices (still missing from the spec §7.1
// entry body): fullActionName (needs a source column), minPrice /
// maxPrice (need a tier join), actionEventList (whole subtree).
//
// Organizer identity is passed in (organizerID, organizerName) because
// the catalog is org-scoped in the authed branch — every entry carries
// the SAME organizer, so handleBil24GetAllActions resolves it once and
// threads the pair into this helper (feature #476 slice 19, spec §7.1
// "`organizerId` — `organizations.display_number`"). Passing 0 for
// organizerID (or empty organizerName) suppresses the respective key;
// the unauthed fallback branch does this since it has no org context.
//
// This helper is pure over EventRow; it does not touch the DB itself,
// so unit tests can pass a hand-built EventRow value.
func (h *Handler) buildActionEntry(ctx context.Context, e gen.EventRow, organizerID int64, organizerName string) map[string]any {
	action := map[string]any{
		// Spec §4 / §7.1 (feature #476): int64 wire form via compat map.
		// Fallback (nil compatDB) returns the legacy UUID string so pre-W1
		// unit-test Handlers stay green.
		"actionId":   h.compatActionID(ctx, e.ID),
		"actionName": e.Name,
	}
	// firstEventDate is the earliest session of the action (AB-37):
	// events carry no own dates; the trigger-maintained cache
	// first_session_at is the Bil24-correct source. Omitted entirely
	// for an event with no sessions.
	if e.FirstSessionAt != nil {
		action["firstEventDate"] = e.FirstSessionAt.UTC().Format(time.RFC3339)
	}
	// Spec §7.1 (slice 17): lastEventDate mirrors firstEventDate against
	// the trigger-maintained last_session_at cache. Same nil-handling
	// contract — an event without sessions omits the key.
	if e.LastSessionAt != nil {
		action["lastEventDate"] = e.LastSessionAt.UTC().Format(time.RFC3339)
	}
	// Spec §7.1 (slice 17): age is the events.age_rating column with the
	// documented "NR" ("not rated") sentinel normalised to "" per spec
	// section 7.1 remark ("`age` — `events.age_rating` (`NR` → `""`)").
	// The key is omitted entirely when the column is nil OR the
	// normalised value is empty — the WP plugin treats an absent key
	// the same as "" but emitting an empty string wastes wire bytes on
	// the majority of events that have no rating set.
	if e.AgeRating != nil {
		age := *e.AgeRating
		if age == "NR" {
			age = ""
		}
		if age != "" {
			action["age"] = age
		}
	}
	// Spec §7.1 (slice 18): prefer the AB-47b poster_media_id when set —
	// artwork uploaded through the media surface is authoritative over the
	// pre-AB-47 events.image_url free-form column. The URL shape mirrors
	// hfeed.mediaFileURL so the WP plugin and public feed agree on the
	// canonical /v1/media-files/{uuid} host.
	if url := posterURL(e); url != "" {
		action["bigPosterUrl"] = url
		action["smallPosterUrl"] = url
	}
	if e.Description != nil {
		action["description"] = *e.Description
	}
	// Spec §7.1 (slice 19): organizerId is organizations.display_number
	// (bigint, migration 0072) for the event's owning org. Passed in
	// because the catalog is org-scoped and the lookup happens once in
	// the handler; 0 signals "no organizer context" (unauthed branch or
	// a lookup error) and the key is OMITTED — WP consumers treat an
	// absent key the same as a nil chip and 0 is not a valid
	// display_number (the sequence starts at 1).
	if organizerID > 0 {
		action["organizerId"] = organizerID
	}
	// organizerName mirrors organizations.name. Emitted only when
	// non-empty so a rare partial fetch (organizerID present, name
	// blank) does not surface an empty string; WP treats an empty
	// string as an unset organizer chip so absence is the safer default.
	if organizerName != "" {
		action["organizerName"] = organizerName
	}
	return action
}

// posterURL resolves the poster URL for a catalog event per spec §7.1
// (feature #476 slice 18). Preference: poster_media_id (AB-47b) rendered
// as /v1/media-files/{uuid} — the canonical media host used by hfeed's
// public feed and the widget so the WP plugin sees the same artwork
// as the browser. Fallback: legacy events.image_url (free-form URL from
// the pre-AB-47 CMS). Returns "" when neither is set so the caller can
// omit the JSON keys (matches the pre-slice behaviour: cover keys are
// absent, not empty, when the event has no artwork).
//
// Pure over gen.EventRow — no DB round-trip. The media_objects row does
// not need to be resolved here because /v1/media-files/{id} streams the
// bytes on demand and the WP plugin already follows that URL.
func posterURL(e gen.EventRow) string {
	if e.PosterMediaID != nil {
		return "/v1/media-files/" + e.PosterMediaID.String()
	}
	if e.ImageURL != nil && *e.ImageURL != "" {
		return *e.ImageURL
	}
	return ""
}

// buildCountryCityLists projects a ListActionVenuesByOrg result set into
// the spec §7.1 countryList and cityList blocks. The country tier is a
// distinct set of {countryId, countryName} pairs; the city tier is a
// distinct set of {cityId, cityName, countryId} triples with a nested
// venueList of {venueId, venueName} entries. Rows whose country_id is
// nil are skipped from countryList (no reference to attach), and rows
// whose city_id is nil are skipped from cityList — the site's plugin
// treats absent geography exactly the same way (bil24-acf-sync.php).
//
// IDs are emitted through compatCountryID / compatCityID / compatVenueID
// so the wire form is int64 on production (compatDB wired) and UUID
// strings on the fallback path (unit tests without a pool). Output
// slices preserve the SQL ORDER BY (country_iso2, city_slug,
// v.display_number) so downstream JSON is stable.
//
// Feature #476 W1-A2b slice 15 (spec §7.1).
func (h *Handler) buildCountryCityLists(ctx context.Context, rows []gen.ActionVenueRow) ([]map[string]any, []map[string]any) {
	countryList := make([]map[string]any, 0)
	cityList := make([]map[string]any, 0)

	// Track distinct countries and cities in first-seen order so the SQL
	// ORDER BY is honored end-to-end. Keys are UUIDs from the source rows.
	seenCountry := make(map[uuid.UUID]bool)
	// cityIdx maps city UUID to its index inside cityList so successive
	// rows for the same city append to the same venueList.
	cityIdx := make(map[uuid.UUID]int)

	for _, r := range rows {
		// countryList entry — one per distinct country_id.
		if r.CountryID != nil && !seenCountry[*r.CountryID] {
			seenCountry[*r.CountryID] = true
			name := ""
			if r.CountryName != nil {
				name = *r.CountryName
			}
			countryList = append(countryList, map[string]any{
				"countryId":   h.compatCountryID(ctx, *r.CountryID),
				"countryName": name,
			})
		}

		// cityList entry — one per distinct city_id, with a nested
		// venueList that accumulates every venue row hitting that city.
		if r.CityID == nil {
			continue
		}
		// Spec §7.1 venueList entry — venueId, venueName plus optional
		// address / geoLat / geoLon. Address is emitted only when the
		// venue has one (structured line1[,line2] fallback to the legacy
		// free-form address column, resolved SQL-side). Geo coordinates
		// are emitted as JSON numbers only when both are populated —
		// half a coordinate is not useful to the site plugin.
		venue := map[string]any{
			"venueId":   h.compatVenueID(ctx, r.VenueID),
			"venueName": r.VenueName,
		}
		if r.Address != nil && *r.Address != "" {
			venue["address"] = *r.Address
		}
		if r.GeoLat != nil && r.GeoLng != nil {
			venue["geoLat"] = *r.GeoLat
			venue["geoLon"] = *r.GeoLng
		}
		if idx, ok := cityIdx[*r.CityID]; ok {
			existing := cityList[idx]["venueList"].([]map[string]any)
			cityList[idx]["venueList"] = append(existing, venue)
			continue
		}
		cityName := ""
		if r.CityName != nil {
			cityName = *r.CityName
		}
		entry := map[string]any{
			"cityId":    h.compatCityID(ctx, *r.CityID),
			"cityName":  cityName,
			"venueList": []map[string]any{venue},
		}
		if r.CountryID != nil {
			entry["countryId"] = h.compatCountryID(ctx, *r.CountryID)
		}
		cityIdx[*r.CityID] = len(cityList)
		cityList = append(cityList, entry)
	}

	return countryList, cityList
}

// ─────────────────────────────────────────────────────────────────────────────
// GET_SEAT_LIST — list ticket tiers for a session
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24GetSeatList maps GET_SEAT_LIST to either ticket-tier listing
// (general_admission) or the real assigned-seat inventory
// (assigned_seats / hybrid) for a specific event session. Feature #312
// Wave SEAT-D1 introduced the admission_mode branch on top of the
// pre-existing tier-facade behavior.
//
// Bil24 request fields used:
//   - actionEventId: platform session UUID (Bil24 event instance)
//
// Response shapes:
//
//   - general_admission (or admissionQ nil / session not resolvable to a
//     seating binding) — one entry per ticket_tier, unchanged from
//     pre-#312 behavior:
//
//     {
//     "categoryPriceId": "<uuid>", "categoryName": "...",
//     "price": <cents>, "currency": "USD",
//     "pricingMode": "fixed"|"free"|"pwyw",
//     "availableCount": <int or null>
//     }
//
//   - assigned_seats / hybrid — one entry per session_seat, per ADR-005
//     the seat identifier is the platform session_seats.id serialised
//     as a plain UUID string:
//
//     {
//     "seatId":          "<uuid>",       // session_seats.id as string
//     "categoryPriceId": "<uuid>",       // tier UUID (nullable)
//     "sector":          "...",
//     "row":             "...",
//     "number":          "...",
//     "price":           <cents>,        // 0 if no tier bound yet
//     "currency":        "USD",
//     "status":          <BSS int>       // 0 unavailable, 1 available, 3 held, 4 sold
//     }
//
// BSS status codes are the Bil24 seat-status wire values (§6 of the
// Bil24 gateway spec): 0 = unavailable (admin), 1 = available, 3 = held
// (reservation active), 4 = sold. The mapping never surfaces the internal
// row status string.
//
// Operator note: stadium-scale seat maps can push the seatList payload
// past 1 MiB. Enable gzip on the reverse proxy fronting POST
// /compat/bil24/json (nginx: gzip_types application/json; Cloudflare:
// Auto-Minify JSON + Brotli; Caddy: encode zstd gzip) so callers with
// Accept-Encoding: gzip receive a compressed response and the wire foot-
// print stays predictable.
func (h *Handler) handleBil24GetSeatList(w http.ResponseWriter, r *http.Request, req bil24Request) {
	// tier and seat services can be independently unwired; the outer
	// guard fails fast only if BOTH are missing (no data source at all
	// for either branch).
	if h.tierQueries == nil && h.seatQ == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "seat service unavailable",
		))
		return
	}

	ctx := r.Context()

	// Spec §4 / §7.2 (feature #476, W1-A2b): actionEventId is int64 on the
	// wire; resolveActionEventID rejects UUID input with -2 when compatDB is
	// wired and falls back to TranslateLegacyID for unit tests that omit the
	// pool.
	sessionID, err := h.resolveActionEventID(ctx, req.ActionEventID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId must be a valid session identifier",
		))
		return
	}

	// Feature #471 (spec §5, §7.2): validate fid+token and enforce that the
	// requested session belongs to the channel's org. Cross-tenant reads
	// through the compat surface are rejected as "not found in this
	// channel's organization" (-3).
	channel, authed := h.authenticateCommand(ctx, w, req)
	if h.requireToken && !authed {
		return
	}
	if authed {
		if !h.enforceSessionOrg(ctx, w, req, sessionID, channel.OrgID) {
			return
		}
	}

	// Resolve admission_mode when the seating dependencies are wired.
	// Missing dependencies / lookup failures silently fall back to the
	// tier-facade behavior — legacy GA clients keep working during the
	// SEAT-D rollout even when the seating tables are empty.
	admissionMode := "general_admission"
	if h.admissionQ != nil {
		row, aerr := h.admissionQ.GetSessionAdmissionModeByID(ctx, sessionID)
		if aerr == nil && row.AdmissionMode != "" {
			admissionMode = row.AdmissionMode
		}
	}

	// Route: sessions with materialized seat/GA-unit rows emit per-unit
	// entries (AB-51 restored compat parity — every ticketable place has
	// a seatId); the tier facade remains the fallback for unwired seat
	// queries and legacy GA sessions without unit rows.
	if h.seatQ != nil {
		seats, serr := h.seatQ.ListSessionSeats(ctx, sessionID)
		if serr != nil && admissionMode != "general_admission" {
			h.logger.Error("bil24_compat: GET_SEAT_LIST: list session seats failed",
				slog.String("session_id", sessionID.String()),
				slog.String("error", serr.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "failed to retrieve seat list",
			))
			return
		}
		if serr == nil && (admissionMode != "general_admission" || len(seats) > 0) {
			h.getSeatListUnits(w, ctx, req, sessionID, admissionMode, seats)
			return
		}
	}
	if h.tierQueries == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "tier service unavailable",
		))
		return
	}
	h.getSeatListGA(w, ctx, req, sessionID)
}

// getSeatListGA is the pre-#312 tier-facade GET_SEAT_LIST response for
// general_admission sessions (and the fallback whenever the SEAT-D
// dependencies are not wired). Kept factored out so the assigned-seat
// branch can remain a self-contained addition.
func (h *Handler) getSeatListGA(w http.ResponseWriter, ctx context.Context, req bil24Request, sessionID uuid.UUID) {
	tiers, err := h.tierQueries.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		h.logger.Error("bil24_compat: GET_SEAT_LIST: list tiers failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to retrieve seat list",
		))
		return
	}

	// AB-48: scheduled prices via the ONE resolver (base on lookup failure).
	effPrices, effErr := priceresolve.ForTiers(ctx, h.tierQueries, tiers, time.Now().UTC())
	if effErr != nil {
		h.logger.Error("bil24_compat: GET_SEAT_LIST: price window lookup failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", effErr.Error()),
		)
		effPrices = nil
	}
	effectiveOf := func(t gen.TicketTierRow) int64 {
		if eff, ok := effPrices[t.ID]; ok {
			return eff.Amount
		}
		return t.PriceAmount
	}

	seatList := make([]map[string]any, 0, len(tiers))
	for _, t := range tiers {
		seat := map[string]any{
			// Spec §4 / §7.2 (feature #476): int64 wire form via compat map.
			// Fallback (nil compatDB) returns the legacy UUID string so the
			// pre-W1 unit-test Handlers stay green.
			"categoryPriceId": h.compatCategoryPriceID(ctx, t.ID),
			"categoryName":    t.Name,
			"price":           effectiveOf(t),
			"currency":        t.Currency,
			"pricingMode":     t.PricingMode,
		}
		if t.Capacity != nil {
			seat["availableCount"] = *t.Capacity
		}
		seatList = append(seatList, seat)
	}

	// Spec §7.2 (feature #476 slice 21): the response envelope carries the
	// session-level currency at the top level. Bil24 goldens under
	// testdata/wp/golden/GET_SEAT_LIST/basic.json expect this key; every
	// tier of a session shares one currency (a mixed-currency session is
	// rejected at ticket_tier admission time), so the first non-empty tier
	// currency is the correct source. Omitted entirely when there is no
	// tier at all — the pre-slice callers see no wire regression because
	// the empty-tier path never emitted a currency to begin with.
	body := map[string]any{
		"seatList": seatList,
	}
	if cur := seatListCurrency(tiers); cur != "" {
		body["currency"] = cur
	}
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, body))
}

// getSeatListUnits is the per-unit GET_SEAT_LIST branch (SEAT-D1,
// extended by AB-51 to GA sessions). It emits one entry per
// session_seats row — assigned seats carry sector/row/number, GA units
// carry empty coordinates exactly like the Bil24 seat-management table —
// joining tier metadata (price/currency) from the session's
// ticket_tiers snapshot.
func (h *Handler) getSeatListUnits(w http.ResponseWriter, ctx context.Context, req bil24Request, sessionID uuid.UUID, admissionMode string, seats []gen.SessionSeatRow) {
	// Load tier snapshot for price / currency projection. When the tier
	// dependency is unwired (nil) or fails, we degrade gracefully with
	// price=0 / currency omitted rather than failing the whole
	// response — seat inventory is still meaningful without prices.
	var tiers []gen.TicketTierRow
	if h.tierQueries != nil {
		var terr error
		tiers, terr = h.tierQueries.ListTicketTiersBySession(ctx, sessionID)
		if terr != nil {
			h.logger.Warn("bil24_compat: GET_SEAT_LIST: tier snapshot failed; emitting seats with zero price",
				slog.String("session_id", sessionID.String()),
				slog.String("error", terr.Error()),
			)
			tiers = nil
		}
	}
	tierByID := make(map[uuid.UUID]gen.TicketTierRow, len(tiers))
	for _, t := range tiers {
		tierByID[t.ID] = t
	}
	// AB-48: scheduled prices via the ONE resolver (base on failure).
	var effPrices map[uuid.UUID]priceresolve.Effective
	if h.tierQueries != nil && len(tiers) > 0 {
		if m, effErr := priceresolve.ForTiers(ctx, h.tierQueries, tiers, time.Now().UTC()); effErr != nil {
			h.logger.Warn("bil24_compat: GET_SEAT_LIST: price window lookup failed; using base prices",
				slog.String("error", effErr.Error()))
		} else {
			effPrices = m
		}
	}
	effectiveOf := func(t gen.TicketTierRow) int64 {
		if eff, ok := effPrices[t.ID]; ok {
			return eff.Amount
		}
		return t.PriceAmount
	}

	seatList := make([]map[string]any, 0, len(seats))
	for _, s := range seats {
		entry := map[string]any{
			// Spec §4 / §7.2 (W1-A2b feature #476): seatId on the wire is
			// session_seats.system_seat_id (bigint, migration 0088 /
			// AB-50a). Legacy ADR-005 UUID projection has been retired —
			// callers that need the platform UUID resolve it via
			// compatids on the way back in.
			"seatId": s.SystemSeatID,
			"sector": s.SectorName,
			"row":    s.RowName,
			"number": s.SeatNumber,
			"status": bssStatusCode(s.Status),
		}
		if s.TierID != nil {
			// Spec §4 / §7.2 (feature #476): int64 wire form via compat map.
			entry["categoryPriceId"] = h.compatCategoryPriceID(ctx, *s.TierID)
			if t, ok := tierByID[*s.TierID]; ok {
				entry["price"] = effectiveOf(t)
				entry["currency"] = t.Currency
			} else {
				entry["price"] = int64(0)
			}
		} else {
			entry["price"] = int64(0)
		}
		seatList = append(seatList, entry)
	}

	// Spec §7.2 (feature #476 slice 21): top-level currency mirrors the GA
	// branch. The tier snapshot is best-effort here (a stale/failed load
	// leaves `tiers` empty and we simply omit the key rather than emit an
	// empty string), so pre-slice callers on the unit branch that had no
	// tier snapshot at all still see the same admissionMode+seatList shape.
	body := map[string]any{
		"seatList":      seatList,
		"admissionMode": admissionMode,
	}
	if cur := seatListCurrency(tiers); cur != "" {
		body["currency"] = cur
	}
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, body))
}

// seatListCurrency projects a session's ticket-tier snapshot onto the
// spec §7.2 top-level `currency` key. Every tier of one session shares a
// currency (mixed-currency inserts are rejected at ticket_tier admission)
// so the first non-empty tier currency is the correct value; empty input
// returns "" so callers can OMIT the key rather than emit an empty
// string. Pure over the tier slice — no DB round-trip — so the wire-shape
// contract can be unit-tested without spinning up a live pool.
//
// Feature #476 W1-A2b slice 21 (spec §7.2).
func seatListCurrency(tiers []gen.TicketTierRow) string {
	for _, t := range tiers {
		if t.Currency != "" {
			return t.Currency
		}
	}
	return ""
}

// bssStatusCode maps an internal session_seats.status string to the Bil24
// BSS wire code documented in §6 of the gateway spec:
//
//	unavailable → 0  (admin-withheld)
//	available   → 1
//	held        → 3  (a reservation currently owns the seat)
//	sold        → 4
//
// Any unknown status maps to 0 so legacy clients never see a hole in
// the enum surface.
func bssStatusCode(status string) int {
	switch status {
	case "available":
		return 1
	case "held":
		return 3
	case "sold":
		return 4
	case "unavailable":
		return 0
	default:
		return 0
	}
}
