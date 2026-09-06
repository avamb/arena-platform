// image.go implements GET /compat/bil24/image — the sbt/1.0 seating-plan
// export the WordPress seat picker downloads before it can draw anything
// (feature #501, W1-B5b; spec §8 of
// 08_architecture/18_bil24_compat_wave1_specification_ru.md).
//
// This is the ONLY GET route on the compat gateway. Everything else is
// POST /compat/bil24/json, because Bil24 is an RPC-over-JSON protocol; the
// plan is an exception by necessity — the site drops the URL straight into
// an <img>/fetch and needs plain HTTP caching semantics, which the command
// envelope cannot express (it has no ETag concept, see schema.go's operator
// note). Hence: query-string request, SVG body, real status codes.
//
// Wire contract (spec §8):
//
//	GET /compat/bil24/image?type=seatingPlan&actionEventId=<int64>
//	                       &userId=0&fid=<int64>&locale=<loc>
//
//	200 image/svg+xml   ETag: "<geometry_checksum>:<seat_status_version>"
//	                    Cache-Control: no-cache
//	304                 when If-None-Match selects that ETag
//	404                 for every "you may not see this" case
//
// The 404-for-everything rule is deliberate and is why this file returns so
// few distinct codes. `type` other than seatingPlan, an unknown fid, a
// session in another org, an unpublished session, a general-admission
// session, a malformed actionEventId — all of them answer 404 with no body
// detail. The route is unauthenticated by design (no JWT, and no gateway
// token: the picker runs in a browser and cannot hold a secret), so an
// enumerable error surface would let anyone probe which session ids exist
// and which orgs own them. A GA session in particular MUST look exactly
// like a missing one: it has no seats to render, and leaking "exists but
// wrong admission mode" tells an attacker the id is real.
//
// Auth is fid → channel → org, then org-scope the session. The token is
// NOT checked: it does not travel in an <img> URL. That is acceptable here
// because the route is strictly read-only over data the site publishes
// anyway (a seating plan and its free/taken bitmap) — but it is exactly why
// nothing beyond the plan may ever be added to this response.
package hbil24

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hseating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// imageTypeSeatingPlan is the only value of the `type` query parameter the
// gateway serves. Bil24's original endpoint multiplexed several artefact
// kinds (posters, hall photos) on the same path; arena implements exactly
// one and 404s the rest rather than pretending to support them.
const imageTypeSeatingPlan = "seatingPlan"

// HandleBil24Image serves the sbt/1.0 seating plan for one session.
//
// Ordering of the checks is load-bearing: the cheap syntactic rejection
// (`type`) runs before any DB work, and the ETag comparison runs before the
// geometry / seat / tier reads, so a revalidating picker costs exactly one
// indexed row fetch (GetPublicSessionSchema) instead of a full plan render.
func (h *Handler) HandleBil24Image(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	// Wrong artefact kind: not "not allowed", just "does not exist here".
	if strings.TrimSpace(q.Get("type")) != imageTypeSeatingPlan {
		h.imageNotFound(w, r)
		return
	}

	// Dependency self-gate. Distinct from 404 on purpose: a missing query
	// surface is an operator problem, and answering 404 would silently
	// teach the site that every plan disappeared.
	if h.schemaQ == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	// Spec §4: actionEventId is int64 on the wire; resolveActionEventID
	// reverse-maps it through compatibility_id_map (and falls back to the
	// UUID passthrough for unit tests that build a Handler without a pool).
	sessionID, err := h.resolveActionEventID(ctx, q.Get("actionEventId"))
	if err != nil {
		h.imageNotFound(w, r)
		return
	}

	if !h.authorizeImageRequest(ctx, q.Get("fid"), sessionID) {
		h.imageNotFound(w, r)
		return
	}

	// GetPublicSessionSchema already filters to published, non-deleted,
	// non-general_admission sessions with a bound plan version, so the GA
	// and unpublished 404s of spec §8 fall out of this one query.
	schemaRow, err := h.schemaQ.GetPublicSessionSchema(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.imageNotFound(w, r)
			return
		}
		h.logger.Error("bil24_compat: image: schema lookup failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		h.imageFailed(w, r)
		return
	}

	// Composite validator + revalidation. Set before the 304 branch so the
	// not-modified response still carries the headers a cache needs to
	// refresh its stored entry.
	etag := hseating.SBT10ETag(schemaRow.GeometryChecksum, schemaRow.SeatStatusVersion)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", hseating.SBT10CacheControl)
	if hseating.SBT10MatchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	var geom seating.Geometry
	if err := json.Unmarshal(schemaRow.Geometry, &geom); err != nil {
		h.logger.Error("bil24_compat: image: geometry decode failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		h.imageFailed(w, r)
		return
	}

	// session_seats is the source of sbt:id (system_seat_id) and sbt:state;
	// a geometry seat without a row here is skipped by the encoder.
	seats, err := h.schemaQ.ListSessionSeats(ctx, sessionID)
	if err != nil {
		h.logger.Error("bil24_compat: image: list session seats failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		h.imageFailed(w, r)
		return
	}

	// Tiers feed sbt:price. Their absence is not fatal — the plan still
	// renders, categories just advertise 0 — so a nil TierQ (unit tests) or
	// a read failure degrades instead of 500-ing the whole picker.
	var tiers []gen.TicketTierRow
	if h.resDeps.TierQ != nil {
		tiers, err = h.resDeps.TierQ.ListTicketTiersBySession(ctx, sessionID)
		if err != nil {
			h.logger.Error("bil24_compat: image: list ticket tiers failed",
				slog.String("session_id", sessionID.String()),
				slog.String("error", err.Error()),
			)
			tiers = nil
		}
	}

	// The encoder XML-escapes every attribute it interpolates (see
	// hseating.xmlAttrString) and emits no request-derived text at all — the
	// document is built purely from stored geometry, seat and tier rows — so
	// there is no reflected-input path into the body. nosniff plus the exact
	// image/svg+xml type keeps a browser from re-interpreting it as HTML.
	body := hseating.RenderSBT10SVG(
		geom, seats, tiers,
		h.imageCategoryIDs(ctx, geom, seats, tiers),
		schemaRow.SeatStatusVersion,
	)
	w.Header().Set("Content-Type", hseating.SBT10ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: gosec's taint analysis flags this only because the
	// session id entered through r.URL.Query(). The query value is parsed into
	// a uuid.UUID / int64 before it is used and never reaches the body; every
	// byte written here is XML-escaped output built from stored rows.
	_, _ = w.Write(body)
}

// authorizeImageRequest runs the spec-§8 fid → channel → org gate.
//
// Two nil-surface escapes keep the pre-W1 unit-test harness (Handlers built
// without a pool) working; both are safe because production wiring in
// bil24_shims.go always populates channelQ and resDeps.CtxQ, and a Server
// without them cannot serve real tenant data in the first place.
func (h *Handler) authorizeImageRequest(ctx context.Context, fid string, sessionID uuid.UUID) bool {
	if h.channelQ == nil {
		return true
	}
	channel, ok := h.resolveChannelByFID(ctx, bil24Request{Command: "IMAGE", FID: fid})
	if !ok {
		return false
	}
	if h.resDeps.CtxQ == nil {
		return true
	}
	row, err := h.resDeps.CtxQ.GetSessionOrgContext(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("bil24_compat: image: session org lookup failed",
				slog.String("session_id", sessionID.String()),
				slog.String("error", err.Error()),
			)
		}
		return false
	}
	if row.OrgID != channel.OrgID {
		h.logger.Warn("bil24_compat: image: cross-tenant plan access rejected",
			slog.String("session_id", sessionID.String()),
			slog.String("channel_org", channel.OrgID.String()),
			slog.String("session_org", row.OrgID.String()),
		)
		return false
	}
	return true
}

// imageCategoryIDs maps every seated category index to the int64 wire id of
// the ticket tier bound to it (compatibility_id_map kind = category_price),
// which is what the picker sends back as categoryPriceId when it RESERVEs.
//
// Categories with no resolvable tier are omitted; RenderSBT10SVG emits
// sbt:id="0" for those so the attribute is always present. A non-int64
// value can only come from the nil-compatDB fallback (a UUID string), which
// has no place in a wire that spec §4 defines as int64 — it is dropped to
// the same 0 rather than serialised.
func (h *Handler) imageCategoryIDs(
	ctx context.Context,
	geom seating.Geometry,
	seats []gen.SessionSeatRow,
	tiers []gen.TicketTierRow,
) map[int]int64 {
	tierByCat := hseating.SBT10CategoryTierIDs(geom, seats, tiers)
	out := make(map[int]int64, len(tierByCat))
	for idx, tierID := range tierByCat {
		if id, ok := h.compatCategoryPriceID(ctx, tierID).(int64); ok {
			out[idx] = id
		}
	}
	return out
}

// imageNotFound is the single "you may not have this" exit. Centralised so
// every rejection reason produces a byte-identical response and the route
// stays non-enumerable (see the file docstring): one code, one message, no
// hint about which of the six possible reasons actually fired.
//
// The body is the platform ErrorEnvelope rather than the Bil24 command
// envelope: this is a plain HTTP resource, so it answers in the platform's
// documented error shape (and the OpenAPI error guardrail requires it).
func (h *Handler) imageNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"bil24_compat.image_not_found",
		"seating plan is not available for this request",
		r,
	))
}

// imageFailed is the "we broke, not you" exit: an unreadable geometry blob,
// a dead pool. Kept separate from imageNotFound so a monitoring alert can
// tell a genuine outage from the routine 404 traffic an open GET endpoint
// always attracts.
func (h *Handler) imageFailed(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"bil24_compat.image_failed", "failed to render the seating plan", r,
	))
}
