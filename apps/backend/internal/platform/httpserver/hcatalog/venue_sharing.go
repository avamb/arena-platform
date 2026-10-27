// venue_sharing.go: a verified venue is open to every organization
// (migration 0172, spec 24 reduced to its first step).
//
// The owner organization stays the venue's only editor. The platform operator
// verifies a venue (PATCH /v1/admin/venues/{id}/review); from then on other
// organizations list it (GET /v1/organizations/{org_id}/shared-venues) and
// hold their dates there. A hall plan stays with the organization that drew it.
package hcatalog

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

const (
	sharedVenuesDefaultLimit = 100
	sharedVenuesMaxLimit     = 200
)

// HandleListSharedVenues answers GET /v1/organizations/{org_id}/shared-venues:
// the verified venues of other organizations, optionally narrowed by city_id
// and/or country (ISO 3166-1 alpha-2). Contacts of a foreign venue are never
// returned.
func (h *Handler) HandleListSharedVenues(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.venueQueries, orgID) {
		return
	}

	q := r.URL.Query()
	var cityID *uuid.UUID
	if raw := strings.TrimSpace(q.Get("city_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"venue.invalid_city_id", "city_id must be a valid UUID", r,
				map[string]any{"field": "city_id"},
			))
			return
		}
		cityID = &id
	}
	country := strings.ToUpper(strings.TrimSpace(q.Get("country")))
	if country != "" && len(country) != 2 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"venue.invalid_country", "country must be a two-letter ISO 3166-1 code", r,
			map[string]any{"field": "country"},
		))
		return
	}
	limit := sharedVenuesDefaultLimit
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > sharedVenuesMaxLimit {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"venue.invalid_limit", "limit must be a whole number from 1 to 200", r,
				map[string]any{"field": "limit"},
			))
			return
		}
		limit = n
	}

	rows, err := h.venueQueries.ListSharedVenuesForOrg(r.Context(), orgID, cityID, country, int32(limit)) //nolint:gosec // 1..200
	if err != nil {
		h.logger.Error("venue: list shared failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.list_failed", "failed to list venues", r,
		))
		return
	}
	result := make([]venueResponse, 0, len(rows))
	for _, v := range rows {
		resp := venueFromRow(v)
		resp.ContactPhone, resp.ContactEmail = nil, nil
		result = append(result, resp)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"venues": result})
}

type reviewVenueRequest struct {
	ReviewStatus string `json:"review_status"`
}

// HandleReviewVenue answers PATCH /v1/admin/venues/{id}/review: the platform
// operator verifies a venue (every organization may then use it) or takes the
// verification back. A platform superadmin with X-Admin-Reason only.
func (h *Handler) HandleReviewVenue(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	if !auth.HasSuperadminOrgAccess(ctx) {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"venue.curate_superadmin_only", "platform superadmin access is required to verify a venue", r,
		))
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	venueID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var body reviewVenueRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"venue.invalid_body", "request body must be JSON with review_status", r,
		))
		return
	}
	if body.ReviewStatus != gen.VenueReviewVerified && body.ReviewStatus != gen.VenueReviewUnverified {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"venue.invalid_review_status", "review_status must be \"verified\" or \"unverified\"", r,
			map[string]any{"field": "review_status"},
		))
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
	qtx := h.venueQueries.WithTx(tx)

	row, err := qtx.SetVenueReviewStatus(ctx, venueID, body.ReviewStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("venue.not_found", "venue not found", r))
			return
		}
		h.logger.Error("venue: review failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.review_failed", "failed to change the venue verification", r,
		))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.venue.review",
			ResourceType: "venue",
			ResourceID:   venueID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"review_status": row.ReviewStatus,
				"org_id":        row.OrgID.String(),
				"reason":        reason,
			},
		}); err != nil {
			h.logger.Error("venue: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"venue.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.commit_failed", "failed to commit transaction", r,
		))
		return
	}
	var verifiedAt *string
	if row.VerifiedAt != nil {
		s := row.VerifiedAt.UTC().Format(time.RFC3339)
		verifiedAt = &s
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"venue_id":      row.ID.String(),
		"org_id":        row.OrgID.String(),
		"name":          row.Name,
		"review_status": row.ReviewStatus,
		"verified_at":   verifiedAt,
	})
}
