// cmd_catalog.go — Bil24-compatible catalog reads: GET_ALL_ACTIONS and
// the projection helpers that build the actionList entry body plus the
// countryList / cityList / venueList tree. Extracted from
// bil24_compat.go by feature #476 to keep every per-command file under
// ~700 lines; GET_SEAT_LIST and its branches then moved on to
// cmd_seat_list.go in feature #476 slice 22 so this file stays under
// the ceiling as the spec §7.1 actionList body catches up to spec.
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
