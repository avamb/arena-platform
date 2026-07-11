// layout_svg.go implements the SEAT-D3 BSS-compatible SVG export
// (feature #314).
//
//	GET /v1/event-sessions/{id}/layout.svg
//
// Renders a Bil24-style seating scheme (BSS) SVG for a published seated
// session so legacy Bil24 widgets can consume the plan verbatim. The
// wire attribute names — every one namespaced with the `sbt:` prefix —
// mirror the legacy Bil24 authoring/consumer convention documented in
// 09_autoforge/seating_backlog.md §6:
//
//	seats                sbt:seat  (seat number)
//	                     sbt:id    (platform seat id, session_seats.id string)
//	                     sbt:cat   (1-based category index)
//	                     sbt:state (BSS status code — see below)
//	row groups           sbt:row   (row name)
//	                     sbt:sect  (sector/section name)
//	category swatches    sbt:index sbt:name sbt:color
//	                     sbt:price sbt:currency
//	                     sbt:sold  sbt:used
//
// Status wire codes (§6):
//
//	0 INACCESSIBLE   ← internal status="blocked"
//	1 AVAILABLE      ← internal status="available"
//	2 PRE_RESERVED   (reserved for future flows; never emitted in SEAT-D3)
//	3 RESERVED       ← internal status="held"
//	4 OCCUPIED       ← internal status="sold"
//	5 REFUND         (reserved for future flows; never emitted in SEAT-D3)
//
// Visibility gate mirrors the sibling /schema + /seat-status endpoints:
// the parent event MUST be `published` and the session admission mode
// MUST NOT be `general_admission` — otherwise a 404 is returned.
//
// The response is content-addressed by the geometry checksum joined with
// the live seat_status_version so caches can short-circuit repeat renders
// via `If-None-Match`. Cache-Control is set to `no-cache` because the
// live seat map turns over on every reservation.
package hseating

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// layoutSVGCacheControl is the Cache-Control header for the /layout.svg
// endpoint. The response body embeds the live seat_status_version so it
// mutates on every reservation touching the session; edge caches MUST NOT
// serve a stale render. Callers that want to skip re-download costs can
// still use If-None-Match against the composite ETag below.
const layoutSVGCacheControl = "no-cache"

// bssStateCode enumerates the BSS wire codes per §6 of the seating
// backlog. Kept as named constants so the renderer and tests share one
// spelling; the values are stable public wire codes.
const (
	bssStateInaccessible = 0
	bssStateAvailable    = 1
	// bssStatePreReserved = 2 — reserved for future flows.
	bssStateReserved = 3
	bssStateOccupied = 4
	// bssStateRefund      = 5 — reserved for future flows.
)

// sbtNamespaceURI is the URI advertised for the sbt: attribute namespace.
// The specific URI is not required to be resolvable — legacy Bil24
// consumers key off the "sbt:" prefix only — but a stable non-empty URI
// keeps the SVG XML-well-formed for downstream tools that validate
// namespace declarations.
const sbtNamespaceURI = "http://bil24.pro/sbt"

// svgNamespaceURI is the standard SVG default namespace.
const svgNamespaceURI = "http://www.w3.org/2000/svg"

// HandleGetPublicSessionLayoutSVG serves the SEAT-D3 BSS-compatible SVG
// export for a published seated session. See the file-level docstring
// for the full contract.
func (h *Handler) HandleGetPublicSessionLayoutSVG(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()

	schemaRow, err := h.queries.GetPublicSessionSchema(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"event_session.layout_not_found",
				"session is not published, is general_admission, or does not exist",
				r,
			))
			return
		}
		h.logger.Error("seating_public: layout.svg schema lookup failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.layout_failed", "failed to load session schema", r,
		))
		return
	}

	// Composite ETag: geometry_checksum pins the plan geometry (so a plan
	// rebind produces a new tag), the seat_status_version pins the live
	// seat states. Caches that store the pair can serve fast 304s while
	// still reflecting every reservation flip.
	etag := layoutSVGETag(schemaRow.GeometryChecksum, schemaRow.SeatStatusVersion)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", layoutSVGCacheControl)
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Decode the canonical geometry once. This gives us the coordinate +
	// category-index index the renderer needs; the raw jsonb payload
	// itself is not written to the SVG output.
	var geom seating.Geometry
	if err := json.Unmarshal(schemaRow.Geometry, &geom); err != nil {
		h.logger.Error("seating_public: layout.svg geometry decode failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.layout_failed", "failed to decode session geometry", r,
		))
		return
	}

	seats, err := h.queries.ListSessionSeats(ctx, sessionID)
	if err != nil {
		h.logger.Error("seating_public: layout.svg list seats failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.layout_failed", "failed to load session seats", r,
		))
		return
	}

	tiers, err := h.queries.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		h.logger.Error("seating_public: layout.svg list tiers failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.layout_failed", "failed to load ticket tiers", r,
		))
		return
	}

	body := RenderBSSLayoutSVG(geom, seats, tiers, schemaRow.SeatStatusVersion)

	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// layoutSVGETag composes the strong ETag exposed on the response. The
// format `"<geometry_checksum>:<seat_status_version>"` is opaque to the
// caller and only needs to compare byte-for-byte against If-None-Match
// on the next request; internal composition is documented so operators
// debugging cache misses can reason about it.
func layoutSVGETag(geometryChecksum string, seatStatusVersion int64) string {
	return `"` + geometryChecksum + `:` + strconv.FormatInt(seatStatusVersion, 10) + `"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Pure renderer
// ─────────────────────────────────────────────────────────────────────────────

// RenderBSSLayoutSVG renders the BSS-compatible SVG payload for a seated
// session. Pure function so the HTTP handler and tests can share the
// exact wire projection.
//
//	g                    canonical geometry (canvas + categories + seats)
//	seats                live session_seats snapshot (source of sbt:id + sbt:state)
//	tiers                ticket_tiers snapshot (source of sbt:price + sbt:currency)
//	seatStatusVersion    monotonic cursor written into the root <svg> as
//	                     sbt:statusVersion so consumers can correlate a
//	                     downloaded map with the SEAT-B3 delta stream.
//
// Determinism: every collection is walked in canonical order (categories
// by index; seats by seat_key thanks to ListSessionSeats). The output is
// byte-stable across runs for identical inputs.
func RenderBSSLayoutSVG(
	g seating.Geometry,
	seats []gen.SessionSeatRow,
	tiers []gen.TicketTierRow,
	seatStatusVersion int64,
) []byte {
	// Pre-index geometry lookups. seat_key → geometry Seat for
	// coordinates + category index; category_index → Category for the
	// PriceCategory swatches.
	seatGeomByKey := seatKeyIndex(g)
	categoryByIndex := make(map[int]seating.Category, len(g.Categories))
	for _, c := range g.Categories {
		categoryByIndex[c.Index] = c
	}

	// Resolve category → tier by inspecting the live session_seats
	// snapshot: any seat carrying a resolvable tier_id anchors its
	// category bucket. First-seat-wins keeps resolution stable across
	// renames (shared helper in types.go, also used by /schema).
	categoryToTier := resolveCategoryTiers(g, seats, tiers, seatGeomByKey)

	// Roll up per-category live counters. sold = seats currently in
	// status="sold"; used = seats not in status="available" (i.e. any of
	// blocked / held / sold — the operator-visible "unavailable"
	// aggregate). Blocked-seat counters are exposed to consumers that
	// prefer to render "closed" ticks separately from "reserved" / "sold".
	type catCounts struct{ sold, used int }
	counts := make(map[int]catCounts, len(g.Categories))
	for _, s := range seats {
		cat, ok := seatGeomByKey[s.SeatKey]
		if !ok {
			continue
		}
		c := counts[cat.CategoryIndex]
		switch s.Status {
		case "sold":
			c.sold++
			c.used++
		case "held", "blocked":
			c.used++
		}
		counts[cat.CategoryIndex] = c
	}

	// Build a stable seats-by-(sector,row) index so the row groups render
	// in canonical geometry order (sector Key ASC, row Key ASC). The
	// geometry payload is already canonicalised on import, so we simply
	// walk it — session_seats rows are attached by seat_key lookup.
	seatBySessionKey := make(map[string]gen.SessionSeatRow, len(seats))
	for _, s := range seats {
		seatBySessionKey[s.SeatKey] = s
	}

	var buf bytes.Buffer
	// XML declaration keeps the response well-formed against strict
	// consumers (Bil24 legacy widget uses libxml2 under the hood).
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")

	// Root <svg> declares the default SVG namespace and the sbt: prefix.
	// viewBox mirrors the imported canvas so downstream renderers scale
	// deterministically. width / height are emitted as pixel-space values
	// to match the Bil24 authoring convention.
	fmt.Fprintf(&buf,
		`<svg xmlns="%s" xmlns:sbt="%s" width="%s" height="%s" viewBox="0 0 %s %s" sbt:statusVersion="%d">`+"\n",
		svgNamespaceURI, sbtNamespaceURI,
		formatFloat(g.Canvas.Width), formatFloat(g.Canvas.Height),
		formatFloat(g.Canvas.Width), formatFloat(g.Canvas.Height),
		seatStatusVersion,
	)

	// Decor first so it renders under the seats. The importer already
	// canonicalised the fragment, so we can splice it in as-is inside a
	// <g id="Decor"> wrapper.
	if strings.TrimSpace(g.DecorSVG) != "" {
		buf.WriteString(`  <g id="Decor">`)
		buf.WriteString(g.DecorSVG)
		buf.WriteString(`</g>` + "\n")
	}

	// PriceCategory group — one swatch per Category with the full sbt:*
	// metadata block. Category swatches are 1-based; we walk in Index
	// ASC order (already canonicalised).
	buf.WriteString(`  <g id="PriceCategory">` + "\n")
	sortedCats := append([]seating.Category(nil), g.Categories...)
	sort.SliceStable(sortedCats, func(i, j int) bool {
		return sortedCats[i].Index < sortedCats[j].Index
	})
	for _, cat := range sortedCats {
		cnt := counts[cat.Index]
		price := int64(0)
		currency := ""
		if tier, ok := categoryToTier[cat.Index]; ok {
			price = tier.PriceAmount
			currency = tier.Currency
		}
		fmt.Fprintf(&buf,
			`    <circle sbt:index="%d" sbt:name=%s sbt:color=%s sbt:price="%d" sbt:currency=%s sbt:sold="%d" sbt:used="%d" fill=%s/>`+"\n",
			cat.Index,
			xmlAttrString(cat.Name),
			xmlAttrString(cat.Color),
			price,
			xmlAttrString(currency),
			cnt.sold,
			cnt.used,
			xmlAttrString(cat.Color),
		)
	}
	buf.WriteString(`  </g>` + "\n")

	// Seats grouped by (Section, Row). Every row group carries sbt:sect
	// + sbt:row so legacy widgets can render the row label without
	// touching seat metadata. Row group ordering is stable via the
	// canonicalised geometry walk.
	buf.WriteString(`  <g id="Seats">` + "\n")
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			fmt.Fprintf(&buf,
				`    <g sbt:sect=%s sbt:row=%s>`+"\n",
				xmlAttrString(sec.Name),
				xmlAttrString(row.Name),
			)
			for _, seat := range row.Seats {
				key := seat.Key
				if key == "" {
					key = seating.SeatKey(sec.Key, row.Key, seat.Number)
				}
				sessionSeat, hasLive := seatBySessionKey[key]
				state := bssStateAvailable
				sbtID := ""
				if hasLive {
					state = statusToBSS(sessionSeat.Status)
					sbtID = sessionSeat.ID.String()
				}
				cat := categoryByIndex[seat.CategoryIndex]
				fmt.Fprintf(&buf,
					`      <circle sbt:seat=%s sbt:id=%s sbt:cat="%d" sbt:state="%d" cx="%s" cy="%s" r="%s" fill=%s/>`+"\n",
					xmlAttrString(seat.Number),
					xmlAttrString(sbtID),
					seat.CategoryIndex,
					state,
					formatFloat(seat.X),
					formatFloat(seat.Y),
					formatFloat(seat.Radius),
					xmlAttrString(cat.Color),
				)
			}
			buf.WriteString(`    </g>` + "\n")
		}
	}
	buf.WriteString(`  </g>` + "\n")
	buf.WriteString(`</svg>` + "\n")
	return buf.Bytes()
}

// statusToBSS translates the internal session_seats.status string to the
// §6 BSS wire code. Any unknown status maps to INACCESSIBLE so legacy
// consumers never see a hole in the enum surface.
func statusToBSS(status string) int {
	switch status {
	case "available":
		return bssStateAvailable
	case "held":
		return bssStateReserved
	case "sold":
		return bssStateOccupied
	case "blocked":
		return bssStateInaccessible
	default:
		return bssStateInaccessible
	}
}

// xmlAttrString returns the value formatted as a quoted, XML-escaped
// attribute (including the surrounding double quotes). Uses xml.EscapeText
// so &, <, >, ', " are all encoded correctly. An empty value is emitted
// as `""` — legacy Bil24 widgets accept empty attribute strings.
func xmlAttrString(v string) string {
	var buf bytes.Buffer
	buf.WriteByte('"')
	if v != "" {
		_ = xml.EscapeText(&buf, []byte(v))
	}
	buf.WriteByte('"')
	return buf.String()
}

// formatFloat renders a canvas / seat coordinate with a compact, stable
// decimal representation. strconv.FormatFloat with 'f'/-1 emits the
// shortest round-trippable form and never uses scientific notation.
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
