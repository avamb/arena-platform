// promoters.go implements an organization's promoter list and the promoter
// link of an event (migration 0113).
//
// The organization selling an event is often, but not always, its promoter:
// a partner can be. org_promoters is the organization's list of partners and
// event_promoters links an event to one of them; NO link means the
// organization itself is the promoter. The linked promoter's name is what the
// ticket PDF prints as "Organizer" and what the order export carries as the
// organizer display name.
//
// Endpoints:
//
//	GET   /v1/organizations/{org_id}/promoters            — list (promoter.read)
//	POST  /v1/organizations/{org_id}/promoters            — create (promoter.manage)
//	PATCH /v1/organizations/{org_id}/promoters/{id}       — update / archive (promoter.manage)
//	PUT   /v1/organizations/{org_id}/events/{id}/promoter — set / clear the event's promoter (event.update)
package hcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// PromoterResponse is the JSON shape of one promoter.
type PromoterResponse struct {
	ID         string  `json:"id"`
	OrgID      string  `json:"org_id"`
	Name       string  `json:"name"`
	LegalID    *string `json:"legal_id"`
	Phone      *string `json:"phone"`
	Email      *string `json:"email"`
	Slug       *string `json:"slug"`
	Archived   bool    `json:"archived"`
	ArchivedAt *string `json:"archived_at"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// PromoterFromRow renders an org_promoters row.
func PromoterFromRow(p gen.OrgPromoterRow) PromoterResponse {
	resp := PromoterResponse{
		ID:        p.ID.String(),
		OrgID:     p.OrgID.String(),
		Name:      p.Name,
		LegalID:   p.LegalID,
		Phone:     p.Phone,
		Email:     p.Email,
		Slug:      p.Slug,
		Archived:  p.ArchivedAt != nil,
		CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if p.ArchivedAt != nil {
		s := p.ArchivedAt.UTC().Format(time.RFC3339)
		resp.ArchivedAt = &s
	}
	return resp
}

// NormalizePromoterName trims a promoter name and collapses inner runs of
// whitespace to one space. Case is kept: it is printed on tickets as typed.
// The uniqueness index compares lower(btrim(name)), so two names that differ
// only in case or surrounding whitespace collide.
func NormalizePromoterName(raw string) string {
	return strings.Join(strings.Fields(raw), " ")
}

// normalizeOptionalText trims an optional contact field; blank means "not
// set" and is stored as NULL.
func normalizeOptionalText(v *string) *string {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		return nil
	}
	return &s
}

// isPromoterNameConflict reports the active-name unique index violation.
func isPromoterNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName != promoterSlugConstraint
}

// promoterSlugConstraint is the platform-wide unique index on
// org_promoters.slug (migration 0117).
const promoterSlugConstraint = "org_promoters_slug_uq"

func isPromoterSlugConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == promoterSlugConstraint
}

// promoterSlugMaxLen bounds a promoter page slug.
const promoterSlugMaxLen = 64

// NormalizePromoterSlug lower-cases and trims a slug typed by a person and
// reports whether it is a valid page address: 2–64 characters of a-z, 0-9
// and single inner hyphens (the same alphabet organization slugs use, since
// the two share the public page namespace).
func NormalizePromoterSlug(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if len(s) < 2 || len(s) > promoterSlugMaxLen {
		return "", false
	}
	prevHyphen := true
	for i, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			prevHyphen = false
		case r == '-':
			if prevHyphen || i == len(s)-1 {
				return "", false
			}
			prevHyphen = true
		default:
			return "", false
		}
	}
	return s, true
}

// promoterSlugChecker is the one query the slug helpers need.
type promoterSlugChecker interface {
	PromoterSlugTaken(ctx context.Context, slug string) (bool, error)
}

// autoPromoterSlug derives a free page slug from the promoter's name:
// geoslug.Slugify (Latin folding + Cyrillic transliteration), "promoter"
// when nothing survives, then "-2", "-3", … until one is free of both
// promoter and organization slugs.
func autoPromoterSlug(ctx context.Context, q promoterSlugChecker, name string) (string, error) {
	base := geoslug.Slugify(name)
	if base == "" {
		base = "promoter"
	}
	if len(base) > promoterSlugMaxLen-4 {
		base = strings.TrimRight(base[:promoterSlugMaxLen-4], "-")
	}
	if len(base) < 2 {
		base = "promoter"
	}
	for n := 1; n <= 200; n++ {
		candidate := base
		if n > 1 {
			candidate = base + "-" + strconv.Itoa(n)
		}
		taken, err := q.PromoterSlugTaken(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", errors.New("promoter: no free slug")
}

func (h *Handler) promoterAudit(ctx context.Context, tx pgx.Tx, r *http.Request, action, resourceType, resourceID string, meta map[string]any) error {
	if h.audit == nil {
		return nil
	}
	actor, _ := auth.ActorFromContext(ctx)
	return h.audit.WriteTx(ctx, tx, audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user", // rewritten to api_key by audit.WithServiceActor
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     meta,
	})
}

func (h *Handler) promoterDepsReady(w http.ResponseWriter, r *http.Request, needTx bool) bool {
	if h.eventQueries == nil || (needTx && h.pool == nil) {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	return true
}

func readJSONBody(w http.ResponseWriter, r *http.Request, prefix string, dst any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil || len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(prefix+".empty_body", "request body is required", r))
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(prefix+".invalid_json", "request body is not valid JSON", r))
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/promoters
// ─────────────────────────────────────────────────────────────────────────────

// HandleListPromoters lists the organization's active promoters by name;
// ?include_archived=true adds the archived ones.
func (h *Handler) HandleListPromoters(w http.ResponseWriter, r *http.Request) {
	if !h.promoterDepsReady(w, r, false) {
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
				"promoter.invalid_query", "include_archived must be true or false", r,
				map[string]any{"param": "include_archived"},
			))
			return
		}
	}
	rows, err := h.eventQueries.ListOrgPromoters(r.Context(), orgID, includeArchived)
	if err != nil {
		h.logger.Error("promoter: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promoter.list_failed", "failed to list promoters", r))
		return
	}
	out := make([]PromoterResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, PromoterFromRow(p))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"promoters": out})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/promoters
// ─────────────────────────────────────────────────────────────────────────────

type createPromoterRequest struct {
	Name    string  `json:"name"`
	LegalID *string `json:"legal_id"`
	Phone   *string `json:"phone"`
	Email   *string `json:"email"`
	// Slug is the promoter's public page address (migration 0117); absent
	// or empty = derived from the name.
	Slug *string `json:"slug"`
}

// HandleCreatePromoter creates a promoter of the organization.
func (h *Handler) HandleCreatePromoter(w http.ResponseWriter, r *http.Request) {
	if !h.promoterDepsReady(w, r, true) {
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
	var req createPromoterRequest
	if !readJSONBody(w, r, "promoter", &req) {
		return
	}
	name := NormalizePromoterName(req.Name)
	if name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"promoter.invalid_name", "name is required", r, map[string]any{"field": "name"},
		))
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to begin transaction", r))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.eventQueries.WithTx(tx)

	var slug string
	if req.Slug != nil && strings.TrimSpace(*req.Slug) != "" {
		normalized, ok := NormalizePromoterSlug(*req.Slug)
		if !ok {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"promoter.invalid_slug", "slug must be 2-64 characters of a-z, 0-9 and single hyphens", r, map[string]any{"field": "slug"},
			))
			return
		}
		taken, err := qtx.PromoterSlugTaken(ctx, normalized)
		if err != nil {
			h.logger.Error("promoter: slug check failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promoter.insert_failed", "failed to create promoter", r))
			return
		}
		if taken {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"promoter.duplicate_slug", "this page address is already taken", r, map[string]any{"field": "slug"},
			))
			return
		}
		slug = normalized
	} else {
		auto, err := autoPromoterSlug(ctx, qtx, name)
		if err != nil {
			h.logger.Error("promoter: slug derivation failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promoter.insert_failed", "failed to create promoter", r))
			return
		}
		slug = auto
	}

	p, err := qtx.InsertOrgPromoter(ctx, orgID, name,
		normalizeOptionalText(req.LegalID), normalizeOptionalText(req.Phone), normalizeOptionalText(req.Email), &slug)
	if err != nil {
		if isPromoterSlugConflict(err) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"promoter.duplicate_slug", "this page address is already taken", r, map[string]any{"field": "slug"},
			))
			return
		}
		if isPromoterNameConflict(err) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"promoter.duplicate_name", "an active promoter with this name already exists", r,
				map[string]any{"field": "name"},
			))
			return
		}
		h.logger.Error("promoter: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promoter.insert_failed", "failed to create promoter", r))
		return
	}
	if err := h.promoterAudit(ctx, tx, r, "v1.promoter.create", "promoter", p.ID.String(), map[string]any{
		"org_id": orgID.String(), "promoter_name": p.Name,
	}); err != nil {
		h.logger.Error("promoter: audit write failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promoter.audit_failed", "failed to write audit event", r))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promoter.commit_failed", "failed to commit transaction", r))
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"promoter": PromoterFromRow(p)})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/promoters/{id}
// ─────────────────────────────────────────────────────────────────────────────

// updatePromoterRequest: absent = keep, null = clear (name cannot be
// cleared), value = set. archived=true archives, false restores.
type updatePromoterRequest struct {
	Name     optionalString `json:"name"`
	LegalID  optionalString `json:"legal_id"`
	Phone    optionalString `json:"phone"`
	Email    optionalString `json:"email"`
	Archived *bool          `json:"archived"`
	// Slug: absent = keep, null = no page, value = the new page address.
	Slug optionalString `json:"slug"`
}

// HandleUpdatePromoter edits, archives or restores a promoter.
func (h *Handler) HandleUpdatePromoter(w http.ResponseWriter, r *http.Request) {
	if !h.promoterDepsReady(w, r, true) {
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	promoterID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	var req updatePromoterRequest
	if !readJSONBody(w, r, "promoter", &req) {
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to begin transaction", r))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.eventQueries.WithTx(tx)

	current, err := qtx.GetOrgPromoter(ctx, promoterID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("promoter.not_found", "promoter not found", r))
			return
		}
		h.logger.Error("promoter: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promoter.get_failed", "failed to read promoter", r))
		return
	}

	name := current.Name
	if req.Name.Present {
		if req.Name.Value == nil || NormalizePromoterName(*req.Name.Value) == "" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"promoter.invalid_name", "name cannot be empty", r, map[string]any{"field": "name"},
			))
			return
		}
		name = NormalizePromoterName(*req.Name.Value)
	}
	legalID := normalizeOptionalText(resolveStr(req.LegalID, current.LegalID))
	phone := normalizeOptionalText(resolveStr(req.Phone, current.Phone))
	email := normalizeOptionalText(resolveStr(req.Email, current.Email))
	archived := current.ArchivedAt != nil
	if req.Archived != nil {
		archived = *req.Archived
	}
	slug := current.Slug
	if req.Slug.Present {
		if req.Slug.Value == nil || strings.TrimSpace(*req.Slug.Value) == "" {
			slug = nil
		} else {
			normalized, ok := NormalizePromoterSlug(*req.Slug.Value)
			if !ok {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"promoter.invalid_slug", "slug must be 2-64 characters of a-z, 0-9 and single hyphens", r, map[string]any{"field": "slug"},
				))
				return
			}
			if current.Slug == nil || !strings.EqualFold(*current.Slug, normalized) {
				taken, err := qtx.PromoterSlugTaken(ctx, normalized)
				if err != nil {
					h.logger.Error("promoter: slug check failed", slog.String("error", err.Error()))
					httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promoter.update_failed", "failed to update promoter", r))
					return
				}
				if taken {
					httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
						"promoter.duplicate_slug", "this page address is already taken", r, map[string]any{"field": "slug"},
					))
					return
				}
			}
			slug = &normalized
		}
	}

	updated, err := qtx.UpdateOrgPromoter(ctx, promoterID, orgID, name, legalID, phone, email, archived, slug)
	if err != nil {
		if isPromoterSlugConflict(err) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"promoter.duplicate_slug", "this page address is already taken", r, map[string]any{"field": "slug"},
			))
			return
		}
		if isPromoterNameConflict(err) {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"promoter.duplicate_name", "an active promoter with this name already exists", r,
				map[string]any{"field": "name"},
			))
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("promoter.not_found", "promoter not found", r))
			return
		}
		h.logger.Error("promoter: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promoter.update_failed", "failed to update promoter", r))
		return
	}
	if err := h.promoterAudit(ctx, tx, r, "v1.promoter.update", "promoter", updated.ID.String(), map[string]any{
		"org_id":        orgID.String(),
		"promoter_name": updated.Name,
		"archived":      updated.ArchivedAt != nil,
	}); err != nil {
		h.logger.Error("promoter: audit write failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promoter.audit_failed", "failed to write audit event", r))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("promoter.commit_failed", "failed to commit transaction", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"promoter": PromoterFromRow(updated)})
}

// ─────────────────────────────────────────────────────────────────────────────
// PUT /v1/organizations/{org_id}/events/{id}/promoter
// ─────────────────────────────────────────────────────────────────────────────

// setEventPromoterRequest: promoter_id null (or absent) makes the
// organization itself the promoter again.
type setEventPromoterRequest struct {
	PromoterID *string `json:"promoter_id"`
}

// ErrInvalidPromoter is returned by SetEventPromoterTx when the promoter is
// unknown, belongs to another organization or is archived.
var ErrInvalidPromoter = errors.New("promoter is not an active promoter of this organization")

// SetEventPromoterTx links eventID to promoterID inside q's transaction, or
// removes the link when promoterID is nil. It refuses a promoter that is not
// an active promoter of orgID with ErrInvalidPromoter, and returns the name
// of the linked promoter ("" when the link was removed). The caller has
// already proved the event belongs to orgID. Shared with the event-bundle
// import so both paths apply the same rule.
func SetEventPromoterTx(ctx context.Context, q *gen.Queries, eventID, orgID uuid.UUID, promoterID *uuid.UUID) (string, error) {
	if promoterID == nil {
		return "", q.DeleteEventPromoter(ctx, eventID, orgID)
	}
	p, err := q.GetOrgPromoter(ctx, *promoterID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrInvalidPromoter
		}
		return "", err
	}
	if p.ArchivedAt != nil {
		return "", ErrInvalidPromoter
	}
	if err := q.SetEventPromoter(ctx, eventID, orgID, p.ID); err != nil {
		return "", err
	}
	return p.Name, nil
}

// HandleSetEventPromoter sets or clears the promoter of an event.
func (h *Handler) HandleSetEventPromoter(w http.ResponseWriter, r *http.Request) {
	if !h.promoterDepsReady(w, r, true) {
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	if !h.requireEventInOrg(w, r, eventID, orgID) {
		return
	}
	var req setEventPromoterRequest
	if !readJSONBody(w, r, "event", &req) {
		return
	}
	var promoterID *uuid.UUID
	if req.PromoterID != nil {
		parsed, err := uuid.Parse(strings.TrimSpace(*req.PromoterID))
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"event.invalid_promoter_id", "promoter_id must be a UUID or null", r,
				map[string]any{"field": "promoter_id"},
			))
			return
		}
		promoterID = &parsed
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to begin transaction", r))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	name, err := SetEventPromoterTx(ctx, h.eventQueries.WithTx(tx), eventID, orgID, promoterID)
	if err != nil {
		if errors.Is(err, ErrInvalidPromoter) {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"event.invalid_promoter", "promoter_id is not an active promoter of this organization", r,
				map[string]any{"field": "promoter_id"},
			))
			return
		}
		h.logger.Error("event: set promoter failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("event.set_promoter_failed", "failed to set the event promoter", r))
		return
	}

	resp := map[string]any{"event_id": eventID.String(), "promoter_id": nil, "promoter_name": nil}
	meta := map[string]any{"org_id": orgID.String(), "promoter_id": nil}
	if promoterID != nil {
		resp["promoter_id"] = promoterID.String()
		resp["promoter_name"] = name
		meta["promoter_id"] = promoterID.String()
		meta["promoter_name"] = name
	}
	if err := h.promoterAudit(ctx, tx, r, "v1.event.promoter_set", "event", eventID.String(), meta); err != nil {
		h.logger.Error("event: promoter audit write failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("event.audit_failed", "failed to write audit event", r))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("event.commit_failed", "failed to commit transaction", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}
