// schema.go implements the Bil24-compatible GET_SCHEMA command
// (feature #313, Wave SEAT-D2).
//
// GET_SCHEMA returns per-seat coordinates for a seated session, derived
// from the seating_plan_versions.geometry payload bound to the session.
// It mirrors the legacy Bil24 API split: GET_SCHEMA carries the pure
// (x, y) geometry, while GET_SEAT_LIST (SEAT-D1) carries the mutable
// per-seat status / price. The two responses are joinable on seatId,
// which in both commands is the session_seats.id serialised as a UUID
// string (ADR-005).
//
// Wire response:
//
//	{
//	  "resultCode": 0,
//	  "command": "GET_SCHEMA",
//	  "admissionMode": "assigned_seats" | "hybrid",
//	  "geometryChecksum": "<sha256 hex>",
//	  "seatStatusVersion": <int>,
//	  "canvas": { "width": <float>, "height": <float> },
//	  "seatSchema": [
//	    { "seatId": "<uuid>", "seatKey": "<key>",
//	      "x": <float>, "y": <float>, "radius": <float>,
//	      "categoryIndex": <int> }
//	  ]
//	}
//
// seatSchema is ordered by session_seats.seat_key ASC so a client can
// zip it against GET_SEAT_LIST (also seat_key ordered) without a second
// hash-join. Seats whose seat_key does not resolve to a geometry entry
// (should not happen, but tolerated during rebinds) are still emitted
// with x=0/y=0 so the response cardinality always matches the session's
// seat count.
package hbil24

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// handleBil24GetSchema resolves the seat-coordinate map for a session and
// projects it into the Bil24 GET_SCHEMA wire envelope.
//
// Bil24 request fields used:
//   - actionEventId: platform session UUID (Bil24 event instance)
//
// Result codes:
//   - ResultCodeOK           on success
//   - ResultCodeInternalError when the schema dependency is not wired
//     (resultCode=-99 "schema service unavailable")
//   - ResultCodeInvalidRequest when actionEventId is missing / not a UUID
//   - ResultCodeNotFound when the session is unpublished, general_admission,
//     soft-deleted, or missing a bound seating_plan_version
//
// Operator note: like GET_SEAT_LIST, the seatSchema payload can be large
// for stadium-scale plans. The response is content-addressed by
// geometryChecksum, which is deterministic per seating_plan_version, so
// operators MAY layer HTTP caching on the reverse proxy — but the Bil24
// envelope itself has no ETag mechanism.
func (h *Handler) handleBil24GetSchema(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if h.schemaQ == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "schema service unavailable",
		))
		return
	}

	sessionID, err := TranslateLegacyID(req.ActionEventID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId must be a valid session identifier",
		))
		return
	}

	ctx := r.Context()

	schemaRow, err := h.schemaQ.GetPublicSessionSchema(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound,
				"session is not published, is general_admission, or has no seating plan bound",
			))
			return
		}
		h.logger.Error("bil24_compat: GET_SCHEMA: schema lookup failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to retrieve seat schema",
		))
		return
	}

	// Decode geometry into the canonical domain shape so we can derive
	// coordinates by seat_key without re-parsing per seat.
	var geom seating.Geometry
	if err := json.Unmarshal(schemaRow.Geometry, &geom); err != nil {
		h.logger.Error("bil24_compat: GET_SCHEMA: geometry decode failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to decode seat schema",
		))
		return
	}

	seatCoordsByKey := buildSeatKeyCoordinateIndex(geom)

	// Load session_seats so the response emits one entry per real seat
	// keyed by its session_seats.id (ADR-005 seatId). ListSessionSeats
	// returns rows in seat_key ASC order (see queries/session_seats.sql)
	// so GET_SCHEMA and GET_SEAT_LIST land in the same order.
	seats, err := h.schemaQ.ListSessionSeats(ctx, sessionID)
	if err != nil {
		h.logger.Error("bil24_compat: GET_SCHEMA: list session seats failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to load session seats",
		))
		return
	}

	seatSchema := make([]map[string]any, 0, len(seats))
	for _, s := range seats {
		entry := map[string]any{
			// ADR-005: seatId on the wire is the platform session_seats.id
			// serialised as a plain UUID string. Matches GET_SEAT_LIST.
			"seatId":  s.ID.String(),
			"seatKey": s.SeatKey,
		}
		if coord, ok := seatCoordsByKey[s.SeatKey]; ok {
			entry["x"] = coord.X
			entry["y"] = coord.Y
			entry["radius"] = coord.Radius
			entry["categoryIndex"] = coord.CategoryIndex
		} else {
			// Seat exists in session_seats but not (yet) in geometry —
			// emit a stable zero coordinate so the response cardinality
			// still matches session_seats. This defends against mid-
			// rebind races where the seating_plan_version is being
			// swapped and rows lag briefly.
			entry["x"] = float64(0)
			entry["y"] = float64(0)
			entry["radius"] = float64(0)
			entry["categoryIndex"] = 0
			h.logger.Warn("bil24_compat: GET_SCHEMA: seat_key missing in geometry",
				slog.String("session_id", sessionID.String()),
				slog.String("seat_key", s.SeatKey),
			)
		}
		seatSchema = append(seatSchema, entry)
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"admissionMode":     schemaRow.AdmissionMode,
		"geometryChecksum":  schemaRow.GeometryChecksum,
		"seatStatusVersion": schemaRow.SeatStatusVersion,
		"canvas": map[string]any{
			"width":  geom.Canvas.Width,
			"height": geom.Canvas.Height,
		},
		"seatSchema": seatSchema,
	}))
}

// seatCoordinate is the minimal (x, y, radius, category) tuple GET_SCHEMA
// projects per seat. Kept package-private because it is a transport-only
// intermediate — the wire format flattens it into the response map.
type seatCoordinate struct {
	X             float64
	Y             float64
	Radius        float64
	CategoryIndex int
}

// buildSeatKeyCoordinateIndex walks the geometry once and returns a
// seat_key → (x, y, radius, category_index) map. Uses the seat.Key field
// verbatim when present (SVG-imported geometries always populate it) and
// falls back to seating.SeatKey(section, row, number) for hand-authored
// payloads that predate the field.
func buildSeatKeyCoordinateIndex(g seating.Geometry) map[string]seatCoordinate {
	estimate := 0
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			estimate += len(row.Seats)
		}
	}
	out := make(map[string]seatCoordinate, estimate)
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			for _, seat := range row.Seats {
				key := seat.Key
				if key == "" {
					key = seating.SeatKey(sec.Key, row.Key, seat.Number)
				}
				out[key] = seatCoordinate{
					X:             seat.X,
					Y:             seat.Y,
					Radius:        seat.Radius,
					CategoryIndex: seat.CategoryIndex,
				}
			}
		}
	}
	return out
}
