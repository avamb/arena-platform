// Package hconversions serves the owner's server-side conversion settings of
// an organization (spec 62 section 8.3, item П-9):
//
//	GET   /v1/organizations/{org_id}/conversion-settings
//	PATCH /v1/organizations/{org_id}/conversion-settings
//
// Meta pixel id + Conversions API token and GA4 measurement id + Measurement
// Protocol secret. The two secrets are write-only: no answer carries them,
// only whether one is set. Both routes need org.update in the path
// organization (the owner) and refuse an organization API key.
package hconversions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// Handler serves the conversion-settings routes. The caller has already
// passed the org.update gate and the membership check of {org_id}.
type Handler struct {
	pool  *pgxpool.Pool
	audit audit.Writer
}

// New builds a Handler; a nil pool makes every route answer 503.
func New(pool *pgxpool.Pool, auditWriter audit.Writer) *Handler {
	return &Handler{pool: pool, audit: auditWriter}
}

// Settings is the answer: secrets are reported only as set or not.
type Settings struct {
	MetaPixelID       string `json:"meta_pixel_id"`
	MetaCAPITokenSet  bool   `json:"meta_capi_token_set"`
	MetaTestEventCode string `json:"meta_test_event_code"`
	GA4MeasurementID  string `json:"ga4_measurement_id"`
	GA4APISecretSet   bool   `json:"ga4_api_secret_set"`
}

var (
	pixelRe       = regexp.MustCompile(`^[0-9]{5,32}$`)
	measurementRe = regexp.MustCompile(`^G-[A-Z0-9]{4,20}$`)
	testCodeRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// column maps a body key to its column and validator.
var fields = []struct {
	key, column string
	ok          func(string) bool
	msg         string
}{
	{"meta_pixel_id", "meta_pixel_id", pixelRe.MatchString, "meta_pixel_id is the numeric pixel id"},
	{"meta_capi_token", "meta_capi_token", secretOK, "meta_capi_token is 8-512 characters without spaces"},
	{"meta_test_event_code", "meta_test_event_code", testCodeRe.MatchString, "meta_test_event_code is up to 64 letters, digits, - or _"},
	{"ga4_measurement_id", "ga4_measurement_id", measurementRe.MatchString, "ga4_measurement_id looks like G-XXXXXXXXXX"},
	{"ga4_api_secret", "ga4_api_secret", secretOK, "ga4_api_secret is 8-512 characters without spaces"},
}

func secretOK(s string) bool {
	if n := utf8.RuneCountInString(s); n < 8 || n > 512 {
		return false
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func guard(w http.ResponseWriter, r *http.Request, h *Handler) (uuid.UUID, bool) {
	if actor, ok := auth.ActorFromContext(r.Context()); ok && actor.IsService() {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"conversions.api_key_forbidden", "an API key cannot read or change the conversion credentials", r))
		return uuid.Nil, false
	}
	if h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return uuid.Nil, false
	}
	return httputil.UUIDPathParam(w, r, "org_id")
}

func (h *Handler) load(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, orgID uuid.UUID) (Settings, error) {
	var s Settings
	err := q.QueryRow(ctx, `
		SELECT COALESCE(meta_pixel_id, ''), COALESCE(meta_capi_token, '') <> '', COALESCE(meta_test_event_code, ''),
		       COALESCE(ga4_measurement_id, ''), COALESCE(ga4_api_secret, '') <> ''
		  FROM org_conversion_settings WHERE org_id = $1`, orgID).
		Scan(&s.MetaPixelID, &s.MetaCAPITokenSet, &s.MetaTestEventCode, &s.GA4MeasurementID, &s.GA4APISecretSet)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, nil
	}
	return s, err
}

func fail(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
		"conversions.unavailable", "the conversion settings are temporarily unavailable, try again in a minute", r))
}

// HandleGet answers the stored settings (no secrets).
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	orgID, ok := guard(w, r, h)
	if !ok {
		return
	}
	s, err := h.load(r.Context(), h.pool, orgID)
	if err != nil {
		fail(w, r)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"conversion_settings": s})
}

// HandlePatch changes the settings. An absent key keeps the stored value;
// null or "" clears it; anything else is validated and replaces it.
func (h *Handler) HandlePatch(w http.ResponseWriter, r *http.Request) {
	orgID, ok := guard(w, r, h)
	if !ok {
		return
	}
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	var req map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	if err != nil || dec.Decode(&req) != nil || dec.More() || len(req) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("conversions.invalid_body",
			"send at least one of meta_pixel_id, meta_capi_token, meta_test_event_code, ga4_measurement_id, ga4_api_secret", r))
		return
	}
	known := map[string]bool{}
	for _, f := range fields {
		known[f.key] = true
	}
	for k := range req {
		if !known[k] {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("conversions.invalid_body", "unknown field "+k, r))
			return
		}
	}
	// Collect (column, value) pairs; value nil clears.
	cols := make([]string, 0, len(fields))
	vals := make([]any, 0, len(fields))
	changed := make([]string, 0, len(fields))
	for _, f := range fields {
		raw, present := req[f.key]
		if !present {
			continue
		}
		var v *string
		if err := json.Unmarshal(raw, &v); err != nil {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope("conversions.invalid_field", f.msg, r))
			return
		}
		var val any
		if v != nil && strings.TrimSpace(*v) != "" {
			t := strings.TrimSpace(*v)
			if !f.ok(t) {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope("conversions.invalid_field", f.msg, r))
				return
			}
			val = t
		}
		cols = append(cols, f.column)
		vals = append(vals, val)
		changed = append(changed, f.key) // names only, never values
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		fail(w, r)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Upsert: first make sure the row exists, then set the named columns.
	if _, err := tx.Exec(ctx, `
		INSERT INTO org_conversion_settings (org_id)
		SELECT id FROM organizations WHERE id = $1
		ON CONFLICT (org_id) DO NOTHING`, orgID); err != nil {
		fail(w, r)
		return
	}
	set := make([]string, 0, len(cols)+1)
	args := []any{orgID}
	for i, c := range cols {
		args = append(args, vals[i])
		set = append(set, c+" = $"+strconv.Itoa(len(args)))
	}
	set = append(set, "updated_at = now()")
	tag, err := tx.Exec(ctx, "UPDATE org_conversion_settings SET "+strings.Join(set, ", ")+" WHERE org_id = $1", args...)
	if err != nil {
		fail(w, r)
		return
	}
	if tag.RowsAffected() == 0 {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("org.not_found", "organization not found", r))
		return
	}
	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.organization.conversion_settings_update",
			ResourceType: "organization",
			ResourceID:   orgID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata:     map[string]any{"org_id": orgID.String(), "changed": changed},
		}); err != nil {
			fail(w, r)
			return
		}
	}
	s, err := h.load(ctx, tx, orgID)
	if err != nil || tx.Commit(ctx) != nil {
		fail(w, r)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"conversion_settings": s})
}
