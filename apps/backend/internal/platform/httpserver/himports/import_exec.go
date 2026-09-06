// import_exec.go carries the transactional half of the Bil24 session import
// (spec §13.2 steps 2-5 and 7-8): venue → event → session → ticket-tier upsert,
// the charge-percent note and the optional publish transition. Every function
// here runs inside the caller's transaction, so a failure at any step leaves
// the catalog exactly as it was.
package himports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	catalogdomain "github.com/abhteam/arena_new/apps/backend/internal/domain/catalog"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// defaultSessionDuration is applied to end_at because the Bil24 payload never
// carries a duration and sessions.end_at is NOT NULL. Three hours covers the
// overwhelming majority of the imported repertoire; an operator can refine it
// afterwards through the normal session editor.
const defaultSessionDuration = 3 * time.Hour

// importPlan is everything the HTTP layer resolved before opening the
// transaction: it is deliberately free of *http.Request so the executor cannot
// reach back into transport concerns.
type importPlan struct {
	OrgID         uuid.UUID
	Request       bil24compat.ImportSessionRequest
	Currency      string
	StartAt       time.Time
	SaleWindowEnd *time.Time
	PosterMediaID *uuid.UUID
	Timezone      string
}

// importResult is the identifier set the response is built from.
type importResult struct {
	EventID   uuid.UUID
	SessionID uuid.UUID
	TierIDs   map[string]uuid.UUID
	Created   bool
}

// executeImport runs spec §13.2 steps 2-5 and 7-8 inside tx.
//
// Created reports whether the SESSION was created (not the event): idempotency
// of this endpoint is defined on actionEvent.actionEventId, so a second import
// of a new session under a known event still answers created:true.
func (h *Handler) executeImport(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, warnings *warningSink) (importResult, error) {
	venueID, err := h.resolveVenue(ctx, q, tx, plan, warnings)
	if err != nil {
		return importResult{}, err
	}
	eventID, err := h.resolveEvent(ctx, q, tx, plan)
	if err != nil {
		return importResult{}, err
	}
	sessionID, created, err := h.resolveSession(ctx, q, tx, plan, eventID, venueID, warnings)
	if err != nil {
		return importResult{}, err
	}
	tierIDs, err := h.upsertTiers(ctx, q, tx, plan, sessionID)
	if err != nil {
		return importResult{}, err
	}

	// Step 7 — the sales channel is NEVER modified by an import. A declared
	// chargePercent is surfaced as a warning so the operator can reconcile it
	// deliberately.
	if plan.Request.ActionEvent.ChargePercent > 0 {
		warnings.add(WarnChargePercentMismatch, fmt.Sprintf(
			"actionEvent.chargePercent=%.2f was not applied; channel fee_percent is never changed by an import",
			plan.Request.ActionEvent.ChargePercent))
	}

	if plan.Request.Publish {
		if err := h.applyPublish(ctx, q, plan, eventID, sessionID, warnings); err != nil {
			return importResult{}, err
		}
	}

	return importResult{
		EventID:   eventID,
		SessionID: sessionID,
		TierIDs:   tierIDs,
		Created:   created,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — venue
// ─────────────────────────────────────────────────────────────────────────────

// resolveVenue finds the venue by its Bil24 external id or creates it with the
// geography the payload carries. The timezone was already validated by the HTTP
// layer (plan.Timezone is a loadable IANA zone).
func (h *Handler) resolveVenue(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, warnings *warningSink) (uuid.UUID, error) {
	v := plan.Request.Venue
	ext := externalIDString(v.VenueID)

	cityID, country := h.resolveGeography(ctx, q, tx, plan, warnings)
	address := optString(v.Address)

	existing, err := q.GetVenueByBil24ExternalID(ctx, ext)
	switch {
	case err == nil:
		if existing.OrgID != plan.OrgID {
			return uuid.Nil, failImport(http.StatusConflict, "import.venue_owned_by_other_org",
				"venue "+ext+" is already imported into a different organization")
		}
		if err := q.UpdateImportedVenueGeography(ctx, existing.ID, cityID, address, "", v.GeoLat, v.GeoLon, country); err != nil {
			return uuid.Nil, fmt.Errorf("update venue geography: %w", err)
		}
		if err := registerExternal(ctx, tx, compatids.KindVenue, existing.ID, v.VenueID); err != nil {
			return uuid.Nil, err
		}
		return existing.ID, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("lookup venue by external id: %w", err)
	}

	name := trimSpace(v.VenueName)
	if name == "" {
		name = "Bil24 venue " + ext
	}
	created, err := q.InsertImportedVenue(ctx, plan.OrgID, cityID, name, address, plan.Timezone, v.GeoLat, v.GeoLon, country, ext)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert venue: %w", err)
	}
	if err := registerExternal(ctx, tx, compatids.KindVenue, created.ID, v.VenueID); err != nil {
		return uuid.Nil, err
	}
	return created.ID, nil
}

// resolveGeography maps venue.countryName / venue.cityName onto arena's geo
// tables. Both are best-effort: arena's countries table needs ISO codes and a
// currency that the Bil24 payload simply does not carry, so an unknown country
// is reported as a warning and the venue is stored without a city instead of
// failing the whole import.
func (h *Handler) resolveGeography(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, warnings *warningSink) (*uuid.UUID, *string) {
	v := plan.Request.Venue

	countryRow, ok := h.lookupCountry(ctx, q, v.CountryName)
	if !ok {
		if trimSpace(v.CountryName) != "" {
			warnings.add(WarnCountryUnresolved,
				"country \""+trimSpace(v.CountryName)+"\" is not known to arena; the venue was stored without country and city")
		}
		return nil, nil
	}
	if v.CountryID > 0 {
		// A failed registration must not sink the import: the mapping is only
		// needed by the outbound Bil24 gateway, not by the catalog itself.
		if err := registerExternal(ctx, tx, compatids.KindCountry, countryRow.ID, v.CountryID); err != nil {
			h.logger.Warn("import: country compat id not registered", "error", err.Error())
		}
	}
	iso2 := strings.ToUpper(countryRow.Iso2)

	citySlug := slugify(v.CityName)
	if citySlug == "" {
		return nil, &iso2
	}
	city, err := q.GetCityBySlug(ctx, citySlug)
	if errors.Is(err, pgx.ErrNoRows) {
		city, err = q.InsertCity(ctx, countryRow.ID, citySlug)
	}
	if err != nil {
		warnings.add(WarnCityUnresolved,
			"city \""+trimSpace(v.CityName)+"\" could not be resolved: "+err.Error())
		return nil, &iso2
	}
	if v.CityID > 0 {
		if err := registerExternal(ctx, tx, compatids.KindCity, city.ID, v.CityID); err != nil {
			h.logger.Warn("import: city compat id not registered", "error", err.Error())
		}
	}
	cityID := city.ID
	return &cityID, &iso2
}

// lookupCountry resolves a Bil24 countryName against arena's countries table,
// accepting either a bare ISO-3166-1 alpha-2 code or a name that slugifies to
// a known country slug.
func (h *Handler) lookupCountry(ctx context.Context, q *gen.Queries, name string) (gen.CountryRow, bool) {
	raw := trimSpace(name)
	if raw == "" {
		return gen.CountryRow{}, false
	}
	if len(raw) == 2 {
		if row, err := q.GetCountryByISO2(ctx, strings.ToUpper(raw)); err == nil {
			return row, true
		}
	}
	slug := slugify(raw)
	if slug == "" {
		return gen.CountryRow{}, false
	}
	if row, err := q.GetCountryBySlug(ctx, slug); err == nil {
		return row, true
	}
	return gen.CountryRow{}, false
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — event
// ─────────────────────────────────────────────────────────────────────────────

// resolveEvent finds the arena event behind action.actionId, creating it when
// this is the first import of the action. The compat mapping is authoritative;
// events.external_bil24_id is the human-readable mirror kept in sync with it.
func (h *Handler) resolveEvent(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan) (uuid.UUID, error) {
	a := plan.Request.Action
	ext := externalIDString(a.ActionID)

	eventID, err := compatids.Resolve(ctx, tx, compatids.KindAction, a.ActionID)
	if err != nil && !errors.Is(err, compatids.ErrNotFound) {
		return uuid.Nil, fmt.Errorf("resolve action compat id: %w", err)
	}
	if errors.Is(err, compatids.ErrNotFound) {
		row, lookupErr := q.GetEventByBil24ExternalID(ctx, ext)
		switch {
		case lookupErr == nil:
			eventID = row.ID
		case !errors.Is(lookupErr, pgx.ErrNoRows):
			return uuid.Nil, fmt.Errorf("lookup event by external id: %w", lookupErr)
		}
	}

	if eventID != uuid.Nil {
		existing, getErr := q.GetEventRaw(ctx, eventID)
		if getErr != nil {
			return uuid.Nil, fmt.Errorf("read event %s: %w", eventID, getErr)
		}
		if existing.OrgID != plan.OrgID {
			return uuid.Nil, failImport(http.StatusConflict, "import.event_owned_by_other_org",
				"action "+ext+" is already imported into a different organization")
		}
	} else {
		created, insErr := q.InsertEvent(ctx, plan.OrgID, a.Name(), optString(a.Description), "draft", "public", nil)
		if insErr != nil {
			return uuid.Nil, fmt.Errorf("insert event: %w", insErr)
		}
		eventID = created.ID
		if err := q.SetEventBil24ExternalID(ctx, eventID, plan.OrgID, ext); err != nil {
			return uuid.Nil, fmt.Errorf("stamp event external id: %w", err)
		}
	}

	if err := q.SetEventImportMetadata(ctx, eventID, plan.OrgID, a.Name(), optString(a.Description), optString(a.Age)); err != nil {
		return uuid.Nil, fmt.Errorf("update event metadata: %w", err)
	}
	if plan.PosterMediaID != nil {
		if err := q.SetEventPosterMediaID(ctx, eventID, plan.OrgID, plan.PosterMediaID); err != nil {
			return uuid.Nil, fmt.Errorf("attach event poster: %w", err)
		}
	}
	if err := registerExternal(ctx, tx, compatids.KindAction, eventID, a.ActionID); err != nil {
		return uuid.Nil, err
	}
	return eventID, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — session
// ─────────────────────────────────────────────────────────────────────────────

// resolveSession upserts the session behind actionEvent.actionEventId. It also
// enforces spec §13.2 step 10: a session that already sold tickets keeps its
// currency, and one whose event moved to another organization is a conflict.
func (h *Handler) resolveSession(
	ctx context.Context,
	q *gen.Queries,
	tx pgx.Tx,
	plan importPlan,
	eventID, venueID uuid.UUID,
	warnings *warningSink,
) (uuid.UUID, bool, error) {
	ae := plan.Request.ActionEvent
	endAt := plan.StartAt.Add(defaultSessionDuration)
	capacity := plan.Request.TotalAvailability()

	sessionID, err := compatids.Resolve(ctx, tx, compatids.KindActionEvent, ae.ActionEventID)
	if err != nil && !errors.Is(err, compatids.ErrNotFound) {
		return uuid.Nil, false, fmt.Errorf("resolve action event compat id: %w", err)
	}

	if errors.Is(err, compatids.ErrNotFound) {
		if capacity <= 0 {
			return uuid.Nil, false, failImport(http.StatusUnprocessableEntity, "import.capacity_required",
				"categoryList must declare a positive total availability for a new general-admission session")
		}
		status := "draft"
		if plan.Request.Publish {
			status = "scheduled"
		}
		created, insErr := q.InsertSession(ctx, eventID, venueID, plan.StartAt, endAt, capacity, nil, status, nil, plan.Currency, "override")
		if insErr != nil {
			return uuid.Nil, false, fmt.Errorf("insert session: %w", insErr)
		}
		if regErr := registerExternal(ctx, tx, compatids.KindActionEvent, created.ID, ae.ActionEventID); regErr != nil {
			return uuid.Nil, false, regErr
		}
		return created.ID, true, nil
	}

	sctx, err := q.GetSessionImportContext(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The mapping outlived the session (soft-deleted catalog row). The
			// operator must clean that up; silently minting a second session
			// under the same Bil24 id would break idempotency for good.
			return uuid.Nil, false, failImport(http.StatusConflict, "import.session_deleted",
				"actionEvent "+externalIDString(ae.ActionEventID)+" maps to a deleted arena session")
		}
		return uuid.Nil, false, fmt.Errorf("read session %s: %w", sessionID, err)
	}
	if sctx.OrgID != plan.OrgID {
		return uuid.Nil, false, failImport(http.StatusConflict, "import.session_owned_by_other_org",
			"actionEvent "+externalIDString(ae.ActionEventID)+" is already imported into a different organization")
	}

	// Currency is immutable once money changed hands (spec §13.2 step 10).
	currency := plan.Currency
	if currency != sctx.Currency {
		sold, countErr := q.CountTicketsBySession(ctx, sessionID)
		if countErr != nil {
			return uuid.Nil, false, fmt.Errorf("count tickets: %w", countErr)
		}
		if sold > 0 {
			warnings.add(WarnCurrencyLocked,
				"payload currency "+currency+" ignored; session keeps "+sctx.Currency+" because it already has tickets")
			currency = sctx.Currency
		}
	}

	var capacityPtr *int32
	if capacity > 0 {
		capacityPtr = &capacity
	}
	status := ""
	if plan.Request.Publish && sctx.Status == "draft" {
		status = "scheduled"
	}
	if _, err := q.UpdateSession(ctx, sessionID, sctx.EventID, &venueID, &plan.StartAt, &endAt, capacityPtr, nil, status, nil, &currency, "override"); err != nil {
		return uuid.Nil, false, fmt.Errorf("update session: %w", err)
	}
	return sessionID, false, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5 — ticket tiers
// ─────────────────────────────────────────────────────────────────────────────

// upsertTiers maps every categoryList entry onto a ticket tier, keyed by
// categoryPriceId through the compat mapping. The returned map is what the
// response's tier_ids object is built from, so its keys are the DECIMAL STRING
// form of the Bil24 categoryPriceId.
func (h *Handler) upsertTiers(ctx context.Context, q *gen.Queries, tx pgx.Tx, plan importPlan, sessionID uuid.UUID) (map[string]uuid.UUID, error) {
	out := make(map[string]uuid.UUID, len(plan.Request.CategoryList))

	for i, c := range plan.Request.CategoryList {
		name := trimSpace(c.CategoryPriceName)
		if name == "" {
			name = "Category " + externalIDString(c.CategoryPriceID)
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

		tierID, err := compatids.Resolve(ctx, tx, compatids.KindCategoryPrice, c.CategoryPriceID)
		if err != nil && !errors.Is(err, compatids.ErrNotFound) {
			return nil, fmt.Errorf("resolve category compat id: %w", err)
		}

		if errors.Is(err, compatids.ErrNotFound) {
			row, insErr := q.InsertTicketTier(ctx, sessionID, name, mode, price, plan.Currency, nil, nil, capacity, nil, plan.SaleWindowEnd, sortOrder)
			if insErr != nil {
				return nil, fmt.Errorf("insert ticket tier: %w", insErr)
			}
			if regErr := registerExternal(ctx, tx, compatids.KindCategoryPrice, row.ID, c.CategoryPriceID); regErr != nil {
				return nil, regErr
			}
			out[externalIDString(c.CategoryPriceID)] = row.ID
			continue
		}

		updated, updErr := q.UpdateTicketTier(ctx, tierID, sessionID, name, mode, &price, plan.Currency, nil, nil, capacity, nil, plan.SaleWindowEnd, &sortOrder)
		if errors.Is(updErr, pgx.ErrNoRows) {
			// The mapping points at a tier of ANOTHER session — the Bil24
			// category id was reused across action events. Re-pointing the
			// mapping would corrupt the other session's outbound ids, so this
			// is a conflict the operator has to resolve upstream.
			return nil, failImport(http.StatusConflict, "import.category_bound_elsewhere",
				"categoryPriceId "+externalIDString(c.CategoryPriceID)+" is already bound to a ticket tier of a different session")
		}
		if updErr != nil {
			return nil, fmt.Errorf("update ticket tier: %w", updErr)
		}
		out[externalIDString(c.CategoryPriceID)] = updated.ID
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8 — publish
// ─────────────────────────────────────────────────────────────────────────────

// applyPublish moves the event to 'published' when the standard publish gate
// (AB-42: at least one session, every session priced) allows it. A refusal is
// a warning, never a failure — the catalog rows imported successfully and the
// operator can publish manually after fixing the cause.
func (h *Handler) applyPublish(ctx context.Context, q *gen.Queries, plan importPlan, eventID, sessionID uuid.UUID, warnings *warningSink) error {
	event, err := q.GetEventRaw(ctx, eventID)
	if err != nil {
		return fmt.Errorf("read event before publish: %w", err)
	}
	if event.Status == "published" {
		return nil
	}
	if !catalogdomain.IsValidEventTransition(event.Status, "published") {
		warnings.add(WarnPublishSkipped,
			"publish requested but the event cannot move from "+event.Status+" to published")
		return nil
	}

	sessions, err := q.ListSessionsByEvent(ctx, eventID)
	if err != nil {
		return fmt.Errorf("list sessions before publish: %w", err)
	}
	for _, s := range sessions {
		tiers, tErr := q.ListTicketTiersBySession(ctx, s.ID)
		if tErr != nil {
			return fmt.Errorf("list tiers before publish: %w", tErr)
		}
		if len(tiers) == 0 {
			warnings.add(WarnPublishSkipped,
				"publish requested but session "+s.ID.String()+" has no ticket tier")
			return nil
		}
	}

	if _, err := q.UpdateEventStatus(ctx, eventID, plan.OrgID, "published"); err != nil {
		return fmt.Errorf("publish event: %w", err)
	}
	_ = sessionID // the session status was already set by resolveSession
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Poster side-load
// ─────────────────────────────────────────────────────────────────────────────

// sideLoadPoster downloads action.bigPosterUrl and records it as an
// event_poster media object. Every failure path degrades to a warning: an
// unreachable poster host must never cost the operator a catalog import.
//
// Runs OUTSIDE the import transaction (mediastore.Insert uses its own pool and
// a third-party download must not hold row locks).
func (h *Handler) sideLoadPoster(ctx context.Context, orgID uuid.UUID, req bil24compat.ImportSessionRequest, warnings *warningSink) *uuid.UUID {
	raw := trimSpace(req.Action.BigPosterURL)
	if raw == "" {
		return nil
	}
	skip := func(reason string) *uuid.UUID {
		warnings.add(WarnPosterSkipped, "poster "+raw+" was not imported: "+reason)
		return nil
	}
	if h.media == nil || h.http == nil {
		return skip("media storage is not configured on this deployment")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return skip("only http(s) poster urls are supported")
	}

	fetchCtx, cancel := context.WithTimeout(ctx, posterFetchTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, raw, nil)
	if err != nil {
		return skip(err.Error())
	}
	resp, err := h.http.Do(httpReq)
	if err != nil {
		return skip(err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return skip("upstream answered HTTP " + strconv.Itoa(resp.StatusCode))
	}

	contentType := trimSpace(resp.Header.Get("Content-Type"))
	if idx := strings.IndexByte(contentType, ';'); idx >= 0 {
		contentType = trimSpace(contentType[:idx])
	}
	if !strings.HasPrefix(contentType, "image/") {
		return skip("upstream content type " + contentType + " is not an image")
	}

	key, err := mediastore.NewStorageKey("event_poster")
	if err != nil {
		return skip(err.Error())
	}
	checksum, size, err := h.media.PutAndStream(fetchCtx, key, contentType, io.LimitReader(resp.Body, maxPosterBytes))
	if err != nil {
		return skip(err.Error())
	}
	if size == 0 {
		return skip("upstream returned an empty body")
	}

	obj, err := h.media.Insert(ctx, mediastore.InsertInput{
		OrgID:          &orgID,
		OwnerType:      "event_poster",
		StorageBackend: h.media.Backend(),
		StorageKey:     key,
		ContentType:    contentType,
		ByteSize:       size,
		ChecksumSHA256: checksum,
	})
	if err != nil {
		return skip(err.Error())
	}
	id := obj.ID
	return &id
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// registerExternal wraps compatids.RegisterExternal, mapping the two mapping
// conflicts onto operator-readable HTTP errors instead of a bare 500.
func registerExternal(ctx context.Context, tx pgx.Tx, kind compatids.Kind, platformID uuid.UUID, systemID int64) error {
	err := compatids.RegisterExternal(ctx, tx, kind, platformID, systemID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, compatids.ErrExternalIDOutOfRange):
		return failImport(http.StatusUnprocessableEntity, "compat.external_id_out_of_range", err.Error())
	case errors.Is(err, compatids.ErrExternalIDCollision), errors.Is(err, compatids.ErrExternalIDConflict):
		return failImport(http.StatusConflict, "compat.external_id_conflict", err.Error())
	default:
		return fmt.Errorf("register %s compat id: %w", kind, err)
	}
}

// externalIDString renders a Bil24 identifier the way both the compat mapping
// mirror columns and the tier_ids response keys spell it.
func externalIDString(id int64) string { return strconv.FormatInt(id, 10) }

func trimSpace(s string) string { return strings.TrimSpace(s) }

// optString converts a possibly-empty payload string into the nullable form
// the gen wrappers expect.
func optString(s string) *string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// normalizeCurrency upper-cases and validates the ISO-4217 alpha-3 code from
// actionEvent.currency. It is required: arena stores prices in minor units and
// cannot guess the denomination.
func normalizeCurrency(raw string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if code == "" {
		return "", errors.New("actionEvent.currency is required (ISO 4217 alpha-3)")
	}
	if len(code) != 3 {
		return "", fmt.Errorf("actionEvent.currency %q is not an ISO 4217 alpha-3 code", raw)
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return "", fmt.Errorf("actionEvent.currency %q is not an ISO 4217 alpha-3 code", raw)
		}
	}
	return code, nil
}

// cyrillicTranslit maps the Cyrillic alphabet onto its conventional Latin
// transliteration so a Russian city or country name still produces a usable
// arena geo slug. Bil24 is a Russian-origin platform; without this table every
// Cyrillic name would slugify to the empty string.
var cyrillicTranslit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "i", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// latinFold maps the accented Latin letters common in the markets arena
// serves (Hungarian, German, Czech) onto their ASCII base letter.
var latinFold = map[rune]rune{
	'á': 'a', 'ä': 'a', 'â': 'a', 'à': 'a', 'å': 'a', 'ã': 'a',
	'é': 'e', 'ë': 'e', 'ê': 'e', 'è': 'e', 'ě': 'e',
	'í': 'i', 'ï': 'i', 'î': 'i', 'ì': 'i',
	'ó': 'o', 'ö': 'o', 'ő': 'o', 'ô': 'o', 'ò': 'o', 'õ': 'o', 'ø': 'o',
	'ú': 'u', 'ü': 'u', 'ű': 'u', 'û': 'u', 'ù': 'u',
	'ý': 'y', 'ç': 'c', 'č': 'c', 'ñ': 'n', 'ß': 's', 'š': 's', 'ž': 'z',
	'ř': 'r', 'ł': 'l',
}

// slugify renders a free-form place name as an arena geo slug:
// lower-case ASCII words joined by single hyphens. Returns "" when nothing
// usable survives, which callers treat as "unresolvable".
func slugify(raw string) string {
	var b strings.Builder
	prevHyphen := true // suppresses a leading hyphen
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if folded, ok := latinFold[r]; ok {
			r = folded
		}
		if tr, ok := cyrillicTranslit[r]; ok {
			if tr != "" {
				b.WriteString(tr)
				prevHyphen = false
			}
			continue
		}
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-' || r == '_':
			// Any other letter/digit is not representable in a slug; treat it
			// (and every separator) as a word boundary.
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		default:
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
