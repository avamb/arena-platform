// series.go implements an organization's tours (event_series, migration
// 0218, spec 63 section 15.1) and the tour link of an event.
//
// A tour groups several events under one name, public address and set of
// page materials. An event names its tour in events.series_id; NO link means
// the event stands alone. The tour slug shares one address namespace with
// the organization's event slugs (a trigger of the migration refuses the
// collision, SeriesSlugTaken checks it first).
//
// Endpoints (no new permission: read = event.read, write = event.update):
//
//	GET    /v1/organizations/{org_id}/series             — list
//	POST   /v1/organizations/{org_id}/series             — create
//	GET    /v1/organizations/{org_id}/series/{series_id} — one tour with its events
//	PATCH  /v1/organizations/{org_id}/series/{series_id} — partial update
//	DELETE /v1/organizations/{org_id}/series/{series_id} — archive
package hcatalog

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventmaterials"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventslug"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventtext"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// seriesNameMaxLen bounds a tour name (characters).
const seriesNameMaxLen = 200

// seriesSlugConstraint is the trigger constraint that refuses a tour slug
// equal to an active event slug of the organization (and the reverse).
const seriesSlugConstraint = "event_series_slug_namespace"

// seriesOrgSlugConstraint is the unique index on (org_id, lower(slug)).
const seriesOrgSlugConstraint = "event_series_org_slug_uq"

// SeriesResponse is the JSON shape of one tour.
type SeriesResponse struct {
	ID                  string  `json:"id"`
	OrgID               string  `json:"org_id"`
	Slug                string  `json:"slug"`
	Name                string  `json:"name"`
	DescriptionHTML     *string `json:"description_html"`
	PosterMediaID       *string `json:"poster_media_id"`
	PosterURL           *string `json:"poster_url"`
	HeroMediaID         *string `json:"hero_media_id"`
	HeroURL             *string `json:"hero_url"`
	HeroVideoURL        *string `json:"hero_video_url"`
	PartnersTextHTML    *string `json:"partners_text_html"`
	HideChildrenInLists bool    `json:"hide_children_in_lists"`
	Position            int32   `json:"position"`
	EventCount          int     `json:"event_count"`
	ArchivedAt          *string `json:"archived_at"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
	// Translations is reserved for the translated texts of a tour (spec 63
	// Л-1): locale -> {description_html, partners_text_html}. Always {} now;
	// ignored on write.
	Translations map[string]any `json:"translations"`
}

// SeriesEventCardResponse is the short card of an event inside a tour.
type SeriesEventCardResponse struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Slug           *string `json:"slug"`
	Status         string  `json:"status"`
	FirstSessionAt *string `json:"first_session_at"`
}

// SeriesDetailResponse is a tour plus its events.
type SeriesDetailResponse struct {
	SeriesResponse
	Events []SeriesEventCardResponse `json:"events"`
}

func uuidPtrString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// seriesFromRow renders an event_series row (event_count left 0).
func (h *Handler) seriesFromRow(ctx context.Context, s gen.EventSeriesRow) SeriesResponse {
	resp := SeriesResponse{
		ID:                  s.ID.String(),
		OrgID:               s.OrgID.String(),
		Slug:                s.Slug,
		Name:                s.Name,
		DescriptionHTML:     s.DescriptionHTML,
		PosterMediaID:       uuidPtrString(s.PosterMediaID),
		HeroMediaID:         uuidPtrString(s.HeroMediaID),
		HeroVideoURL:        s.HeroVideoURL,
		PartnersTextHTML:    s.PartnersTextHTML,
		HideChildrenInLists: s.HideChildrenInLists,
		Position:            s.Position,
		CreatedAt:           s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:           s.UpdatedAt.UTC().Format(time.RFC3339),
		Translations:        map[string]any{},
	}
	if s.PosterMediaID != nil {
		u := h.mediaFileURL(ctx, *s.PosterMediaID)
		resp.PosterURL = &u
	}
	if s.HeroMediaID != nil {
		u := h.mediaFileURL(ctx, *s.HeroMediaID)
		resp.HeroURL = &u
	}
	if s.ArchivedAt != nil {
		a := s.ArchivedAt.UTC().Format(time.RFC3339)
		resp.ArchivedAt = &a
	}
	return resp
}

// mediaFileURL is the URL of a media object: absolute and signed when a
// signer is wired, else the host-relative /v1/media-files/{id}.
func (h *Handler) mediaFileURL(ctx context.Context, id uuid.UUID) string {
	if h.mediaSigner != nil {
		if signed := h.mediaSigner(ctx, id); signed != "" {
			return signed
		}
	}
	return "/v1/media-files/" + id.String()
}

// NormalizeSeriesName trims a tour name and collapses inner whitespace.
func NormalizeSeriesName(raw string) (string, bool) {
	s := strings.Join(strings.Fields(raw), " ")
	if s == "" || utf8.RuneCountInString(s) > seriesNameMaxLen {
		return "", false
	}
	return s, true
}

// NormalizeSeriesVideoURL validates a tour's hero video: an https YouTube or
// Vimeo link. Blank means "not set" (nil, true).
func NormalizeSeriesVideoURL(raw string) (*string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, true
	}
	ref, err := eventmaterials.ParseVideoURL(s)
	if err != nil {
		return nil, false
	}
	if ref.Provider != eventmaterials.ProviderYouTube && ref.Provider != eventmaterials.ProviderVimeo {
		return nil, false
	}
	return &ref.URL, true
}

// normalizeSeriesHTML runs a rich text of a tour through the same sanitizer
// as event descriptions. An empty result is "not set".
func normalizeSeriesHTML(raw string) (*string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	clean, err := eventtext.NormalizeDescription(s, richtext.FormatART)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(clean) == "" {
		return nil, nil
	}
	return &clean, nil
}

// seriesSlugChecker is the one query the slug helper needs.
type seriesSlugChecker interface {
	SeriesSlugTaken(ctx context.Context, orgID uuid.UUID, slug string, excludeSeriesID *uuid.UUID) (bool, error)
}

// autoSeriesSlug derives a free address from a tour name: geoslug.Slugify,
// "tour" when nothing survives, then "-2", "-3", … until one is free of
// every tour and every active event of the organization.
func autoSeriesSlug(ctx context.Context, q seriesSlugChecker, orgID uuid.UUID, name string) (string, error) {
	base := geoslug.Slugify(name)
	if base == "" {
		base = "tour"
	}
	if len(base) > eventslug.MaxLen-6 {
		base = strings.Trim(base[:eventslug.MaxLen-6], "-")
	}
	if base == "" {
		base = "tour"
	}
	for n := 1; n <= 200; n++ {
		candidate := base
		if n > 1 {
			candidate = base + "-" + strconv.Itoa(n)
		}
		if _, ok := eventslug.Normalize(candidate); !ok {
			continue
		}
		taken, err := q.SeriesSlugTaken(ctx, orgID, candidate, nil)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", errors.New("series: no free slug")
}

func isSeriesSlugConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgUniqueViolation {
		return false
	}
	return pgErr.ConstraintName == seriesSlugConstraint || pgErr.ConstraintName == seriesOrgSlugConstraint
}

func writeSeriesSlugTaken(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
		"series.slug_taken", "this address is already used by a tour or an event of the organization", r,
		map[string]any{"field": "slug"},
	))
}

func writeSeriesInvalid(w http.ResponseWriter, r *http.Request, field, msg string) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		"series.invalid", msg, r, map[string]any{"field": field},
	))
}

func writeSeriesNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("series.not_found", "tour not found", r))
}

// seriesMediaInOrg checks that a media object is a picture uploaded for a tour
// (owner_type event_series) of the organization.
// It answers 422 series.invalid itself and reports false on a refusal.
func (h *Handler) seriesMediaInOrg(w http.ResponseWriter, r *http.Request, q *gen.Queries, field string, mediaID, orgID uuid.UUID) bool {
	var owner *uuid.UUID
	var ownerType string
	err := q.DB().QueryRow(r.Context(),
		`SELECT org_id, owner_type FROM media_objects WHERE id = $1 AND deleted_at IS NULL`, mediaID).Scan(&owner, &ownerType)
	if err == nil && (owner == nil || *owner != orgID || ownerType != "event_series") {
		err = pgx.ErrNoRows
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSeriesInvalid(w, r, field, field+" does not name a media object of this organization")
			return false
		}
		h.logger.Error("series: media lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.media_lookup_failed", "failed to verify media", r))
		return false
	}
	return true
}

// parseSeriesMediaID turns an optional string into a media id. ok=false means
// the 422 has been written.
func parseSeriesMediaID(w http.ResponseWriter, r *http.Request, field string, raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		writeSeriesInvalid(w, r, field, field+" must be a UUID or null")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) seriesDepsReady(w http.ResponseWriter, r *http.Request, needTx bool) bool {
	return h.promoterDepsReady(w, r, needTx)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/series
// ─────────────────────────────────────────────────────────────────────────────

// HandleListSeries lists the organization's tours; ?include_archived=true adds
// the archived ones.
func (h *Handler) HandleListSeries(w http.ResponseWriter, r *http.Request) {
	if !h.seriesDepsReady(w, r, false) {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	includeArchived := false
	if raw := strings.TrimSpace(r.URL.Query().Get("include_archived")); raw != "" {
		switch strings.ToLower(raw) {
		case "true", "1":
			includeArchived = true
		case "false", "0":
		default:
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"series.invalid_query", "include_archived must be true or false", r,
				map[string]any{"param": "include_archived"},
			))
			return
		}
	}
	ctx := r.Context()
	rows, err := h.eventQueries.ListEventSeries(ctx, orgID, includeArchived)
	if err != nil {
		h.logger.Error("series: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.list_failed", "failed to list tours", r))
		return
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, s := range rows {
		ids = append(ids, s.ID)
	}
	counts, err := h.eventQueries.CountEventsPerSeries(ctx, ids)
	if err != nil {
		h.logger.Error("series: count failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.list_failed", "failed to list tours", r))
		return
	}
	out := make([]SeriesResponse, 0, len(rows))
	for _, s := range rows {
		resp := h.seriesFromRow(ctx, s)
		resp.EventCount = counts[s.ID]
		out = append(out, resp)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/series/{series_id}
// ─────────────────────────────────────────────────────────────────────────────

// seriesDetail renders a tour with its event count and event cards.
func (h *Handler) seriesDetail(ctx context.Context, q *gen.Queries, s gen.EventSeriesRow) (SeriesDetailResponse, error) {
	cards, err := q.ListEventCardsForSeries(ctx, s.ID, s.OrgID)
	if err != nil {
		return SeriesDetailResponse{}, err
	}
	resp := h.seriesFromRow(ctx, s)
	resp.EventCount = len(cards)
	events := make([]SeriesEventCardResponse, 0, len(cards))
	for _, c := range cards {
		card := SeriesEventCardResponse{ID: c.ID.String(), Name: c.Name, Slug: c.Slug, Status: c.Status}
		if c.FirstSessionAt != nil {
			f := c.FirstSessionAt.UTC().Format(time.RFC3339)
			card.FirstSessionAt = &f
		}
		events = append(events, card)
	}
	return SeriesDetailResponse{SeriesResponse: resp, Events: events}, nil
}

// HandleGetSeries returns one tour with its events.
func (h *Handler) HandleGetSeries(w http.ResponseWriter, r *http.Request) {
	if !h.seriesDepsReady(w, r, false) {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	seriesID, ok := httputil.UUIDPathParam(w, r, "series_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	ctx := r.Context()
	s, err := h.eventQueries.GetEventSeries(ctx, seriesID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSeriesNotFound(w, r)
			return
		}
		h.logger.Error("series: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.get_failed", "failed to read tour", r))
		return
	}
	resp, err := h.seriesDetail(ctx, h.eventQueries, s)
	if err != nil {
		h.logger.Error("series: events read failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.get_failed", "failed to read tour", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/series
// ─────────────────────────────────────────────────────────────────────────────

type seriesWriteRequest struct {
	Name                *string        `json:"name"`
	Slug                optionalString `json:"slug"`
	DescriptionHTML     optionalString `json:"description_html"`
	PosterMediaID       optionalString `json:"poster_media_id"`
	HeroMediaID         optionalString `json:"hero_media_id"`
	HeroVideoURL        optionalString `json:"hero_video_url"`
	PartnersTextHTML    optionalString `json:"partners_text_html"`
	HideChildrenInLists *bool          `json:"hide_children_in_lists"`
	Position            *int32         `json:"position"`
}

// applySeriesRequest merges a request into params starting from base (a
// zero value for create, the current row for update). ok=false means the
// answer is written. The slug is returned separately: "" means "derive".
func (h *Handler) applySeriesRequest(w http.ResponseWriter, r *http.Request, q *gen.Queries, orgID uuid.UUID, req seriesWriteRequest, p *gen.EventSeriesParams, creating bool) (slugTyped bool, ok bool) {
	if req.Name != nil {
		name, valid := NormalizeSeriesName(*req.Name)
		if !valid {
			writeSeriesInvalid(w, r, "name", "name must be 1-200 characters")
			return false, false
		}
		p.Name = name
	} else if creating {
		writeSeriesInvalid(w, r, "name", "name is required")
		return false, false
	}
	if req.Slug.Present {
		if req.Slug.Value == nil || strings.TrimSpace(*req.Slug.Value) == "" {
			if !creating {
				writeSeriesInvalid(w, r, "slug", "slug cannot be cleared")
				return false, false
			}
		} else {
			norm, valid := eventslug.Normalize(*req.Slug.Value)
			if !valid {
				writeSeriesInvalid(w, r, "slug", "slug must be 1-120 characters: Latin letters, digits and single hyphens")
				return false, false
			}
			if !strings.EqualFold(p.Slug, norm) {
				p.Slug = norm
			}
			slugTyped = true
		}
	}
	if req.DescriptionHTML.Present {
		p.DescriptionHTML = nil
		if req.DescriptionHTML.Value != nil {
			clean, err := normalizeSeriesHTML(*req.DescriptionHTML.Value)
			if err != nil {
				writeSeriesRichtextInvalid(w, r, "description_html", err)
				return false, false
			}
			p.DescriptionHTML = clean
		}
	}
	if req.PartnersTextHTML.Present {
		p.PartnersTextHTML = nil
		if req.PartnersTextHTML.Value != nil {
			clean, err := normalizeSeriesHTML(*req.PartnersTextHTML.Value)
			if err != nil {
				writeSeriesRichtextInvalid(w, r, "partners_text_html", err)
				return false, false
			}
			p.PartnersTextHTML = clean
		}
	}
	if req.HeroVideoURL.Present {
		p.HeroVideoURL = nil
		if req.HeroVideoURL.Value != nil {
			v, valid := NormalizeSeriesVideoURL(*req.HeroVideoURL.Value)
			if !valid {
				writeSeriesInvalid(w, r, "hero_video_url", "hero_video_url must be an https YouTube or Vimeo link")
				return false, false
			}
			p.HeroVideoURL = v
		}
	}
	for _, f := range []struct {
		field string
		opt   optionalString
		dst   **uuid.UUID
	}{
		{"poster_media_id", req.PosterMediaID, &p.PosterMediaID},
		{"hero_media_id", req.HeroMediaID, &p.HeroMediaID},
	} {
		if !f.opt.Present {
			continue
		}
		*f.dst = nil
		if f.opt.Value == nil || strings.TrimSpace(*f.opt.Value) == "" {
			continue
		}
		id, valid := parseSeriesMediaID(w, r, f.field, *f.opt.Value)
		if !valid {
			return false, false
		}
		if !h.seriesMediaInOrg(w, r, q, f.field, id, orgID) {
			return false, false
		}
		*f.dst = &id
	}
	if req.HideChildrenInLists != nil {
		p.HideChildrenInLists = *req.HideChildrenInLists
	}
	if req.Position != nil {
		p.Position = *req.Position
	}
	return slugTyped, true
}

func writeSeriesRichtextInvalid(w http.ResponseWriter, r *http.Request, field string, err error) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		"series.invalid", field+" is not allowed: "+err.Error(), r,
		map[string]any{"field": field, "reason": eventtext.Reason(err)},
	))
}

// HandleCreateSeries creates a tour.
func (h *Handler) HandleCreateSeries(w http.ResponseWriter, r *http.Request) {
	if !h.seriesDepsReady(w, r, true) {
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	var req seriesWriteRequest
	if !readJSONBody(w, r, "series", &req) {
		return
	}
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to begin transaction", r))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.eventQueries.WithTx(tx)

	p := gen.EventSeriesParams{HideChildrenInLists: true} // spec 15.1: DEFAULT true
	typed, ok := h.applySeriesRequest(w, r, qtx, orgID, req, &p, true)
	if !ok {
		return
	}
	if typed {
		taken, err := qtx.SeriesSlugTaken(ctx, orgID, p.Slug, nil)
		if err != nil {
			h.logger.Error("series: slug check failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.create_failed", "failed to create tour", r))
			return
		}
		if taken {
			writeSeriesSlugTaken(w, r)
			return
		}
	} else {
		auto, err := autoSeriesSlug(ctx, qtx, orgID, p.Name)
		if err != nil {
			h.logger.Error("series: slug derivation failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.create_failed", "failed to create tour", r))
			return
		}
		p.Slug = auto
	}
	row, err := qtx.CreateEventSeries(ctx, orgID, p)
	if err != nil {
		if isSeriesSlugConflict(err) {
			writeSeriesSlugTaken(w, r)
			return
		}
		h.logger.Error("series: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.create_failed", "failed to create tour", r))
		return
	}
	if err := h.promoterAudit(ctx, tx, r, "v1.series.create", "event_series", row.ID.String(), map[string]any{
		"org_id": orgID.String(), "series_name": row.Name, "slug": row.Slug,
	}); err != nil {
		h.logger.Error("series: audit write failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.audit_failed", "failed to write audit event", r))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.commit_failed", "failed to commit transaction", r))
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, h.seriesFromRow(ctx, row))
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/series/{series_id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleUpdateSeries edits a tour: absent = keep, null = clear a nullable
// field.
func (h *Handler) HandleUpdateSeries(w http.ResponseWriter, r *http.Request) {
	if !h.seriesDepsReady(w, r, true) {
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	seriesID, ok := httputil.UUIDPathParam(w, r, "series_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	var req seriesWriteRequest
	if !readJSONBody(w, r, "series", &req) {
		return
	}
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to begin transaction", r))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.eventQueries.WithTx(tx)

	current, err := qtx.GetEventSeries(ctx, seriesID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSeriesNotFound(w, r)
			return
		}
		h.logger.Error("series: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.get_failed", "failed to read tour", r))
		return
	}
	p := gen.EventSeriesParams{
		Slug: current.Slug, Name: current.Name, DescriptionHTML: current.DescriptionHTML,
		PosterMediaID: current.PosterMediaID, HeroMediaID: current.HeroMediaID,
		HeroVideoURL: current.HeroVideoURL, PartnersTextHTML: current.PartnersTextHTML,
		HideChildrenInLists: current.HideChildrenInLists, Position: current.Position,
	}
	if _, ok := h.applySeriesRequest(w, r, qtx, orgID, req, &p, false); !ok {
		return
	}
	if !strings.EqualFold(p.Slug, current.Slug) {
		taken, err := qtx.SeriesSlugTaken(ctx, orgID, p.Slug, &seriesID)
		if err != nil {
			h.logger.Error("series: slug check failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.update_failed", "failed to update tour", r))
			return
		}
		if taken {
			writeSeriesSlugTaken(w, r)
			return
		}
	}
	row, err := qtx.UpdateEventSeries(ctx, seriesID, orgID, p)
	if err != nil {
		if isSeriesSlugConflict(err) {
			writeSeriesSlugTaken(w, r)
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeSeriesNotFound(w, r)
			return
		}
		h.logger.Error("series: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.update_failed", "failed to update tour", r))
		return
	}
	if err := h.promoterAudit(ctx, tx, r, "v1.series.update", "event_series", row.ID.String(), map[string]any{
		"org_id": orgID.String(), "series_name": row.Name, "slug": row.Slug,
	}); err != nil {
		h.logger.Error("series: audit write failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.audit_failed", "failed to write audit event", r))
		return
	}
	resp, err := h.seriesDetail(ctx, qtx, row)
	if err != nil {
		h.logger.Error("series: events read failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.update_failed", "failed to update tour", r))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.commit_failed", "failed to commit transaction", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, resp.SeriesResponse)
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/series/{series_id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleArchiveSeries archives a tour. Its events keep their link.
func (h *Handler) HandleArchiveSeries(w http.ResponseWriter, r *http.Request) {
	if !h.seriesDepsReady(w, r, true) {
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	seriesID, ok := httputil.UUIDPathParam(w, r, "series_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to begin transaction", r))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := h.eventQueries.WithTx(tx).ArchiveEventSeries(ctx, seriesID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSeriesNotFound(w, r)
			return
		}
		h.logger.Error("series: archive failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.archive_failed", "failed to archive tour", r))
		return
	}
	if err := h.promoterAudit(ctx, tx, r, "v1.series.archive", "event_series", row.ID.String(), map[string]any{
		"org_id": orgID.String(), "series_name": row.Name,
	}); err != nil {
		h.logger.Error("series: audit write failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.audit_failed", "failed to write audit event", r))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("series.commit_failed", "failed to commit transaction", r))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────────────────────────────────────────────────────────────────────────
// The tour link of an event
// ─────────────────────────────────────────────────────────────────────────────

// ErrInvalidSeries is returned by SetEventSeriesTx when the tour is unknown,
// belongs to another organization or is archived.
var ErrInvalidSeries = errors.New("series is not an active tour of this organization")

// SetEventSeriesTx links eventID to seriesID inside q's transaction, or
// removes the link when seriesID is nil. A tour that is not an active tour of
// orgID is refused with ErrInvalidSeries. The caller has already proved the
// event belongs to orgID. Shared with the event-bundle import so both paths
// apply the same rule.
func SetEventSeriesTx(ctx context.Context, q *gen.Queries, eventID, orgID uuid.UUID, seriesID *uuid.UUID) error {
	if seriesID != nil {
		s, err := q.GetEventSeries(ctx, *seriesID, orgID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidSeries
			}
			return err
		}
		if s.ArchivedAt != nil {
			return ErrInvalidSeries
		}
	}
	_, err := q.SetEventSeries(ctx, eventID, orgID, seriesID)
	return err
}

// hydrateSeries fills SeriesID / SeriesName of event responses from two
// batched queries. Presentational: a failure logs and leaves both nil.
func (h *Handler) hydrateSeries(ctx context.Context, responses []eventResponse) {
	if h.eventQueries == nil || len(responses) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(responses))
	for _, r := range responses {
		if id, err := uuid.Parse(r.ID); err == nil {
			ids = append(ids, id)
		}
	}
	links, err := h.eventQueries.ListSeriesIDsForEvents(ctx, ids)
	if err != nil {
		h.logger.Warn("event: series hydration failed", slog.String("error", err.Error()))
		return
	}
	if len(links) == 0 {
		return
	}
	seen := make(map[uuid.UUID]struct{}, len(links))
	seriesIDs := make([]uuid.UUID, 0, len(links))
	for _, sid := range links {
		if _, dup := seen[sid]; !dup {
			seen[sid] = struct{}{}
			seriesIDs = append(seriesIDs, sid)
		}
	}
	names, err := h.eventQueries.ListEventSeriesNames(ctx, seriesIDs)
	if err != nil {
		h.logger.Warn("event: series name hydration failed", slog.String("error", err.Error()))
		return
	}
	for i := range responses {
		id, err := uuid.Parse(responses[i].ID)
		if err != nil {
			continue
		}
		if sid, ok := links[id]; ok {
			s := sid.String()
			responses[i].SeriesID = &s
			if n, ok := names[sid]; ok {
				name := n
				responses[i].SeriesName = &name
			}
		}
	}
}

// writeInvalidSeries answers 422 for a series_id that is not an active tour of
// the organization.
func writeInvalidSeries(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		"event.invalid_series", "series_id is not an active tour of this organization", r,
		map[string]any{"field": "series_id"},
	))
}

// seriesSlugValid reports whether a slug is a valid tour address.
func seriesSlugValid(slug string) (string, bool) { return eventslug.Normalize(slug) }
