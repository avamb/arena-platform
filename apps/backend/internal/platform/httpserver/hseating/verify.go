// verify.go: a platform superadmin verifies a seating plan (spec
// 08_architecture/64 section 3.4, migration 0216).
//
// An organization decides that a plan fits its hall; the platform superadmin
// decides that the plan fits every organization. A verified plan becomes the
// TEMPLATE of its venue (visibility operator_verified) and its venue becomes
// verified (migration 0182). Another organization lists the templates of a
// venue it may use and takes its own copy through POST
// /v1/seating-plans/{id}/fork; the copy has its own versions, categories and
// annotations, the template never changes through it.
//
//	GET  /v1/admin/seating-plans?verification_state=   the list with filters, page and counts (superadmin)
//	POST /v1/admin/seating-plans/{plan_id}/verify      verify (superadmin + X-Admin-Reason)
//	POST /v1/admin/seating-plans/{plan_id}/needs-fix   send back with a note (superadmin + X-Admin-Reason)
//	GET  /v1/organizations/{org_id}/venues/{venue_id}/plan-templates
//
// operator_verified is set ONLY by the verify route: plan create and PATCH
// refuse it (plans.go).
package hseating

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// Verification states of a seating plan (migration 0216). NULL in the
// database means the plan was never sent for verification.
const (
	VerificationUnverified = "unverified"
	VerificationVerified   = "verified"
	VerificationNeedsFix   = "needs_fix"

	// VisibilityOperatorVerified is the visibility of a venue template.
	VisibilityOperatorVerified = "operator_verified"
	// VisibilityPublicTemplate is the visibility of a plan its owner opened
	// for every organization to copy.
	VisibilityPublicTemplate = "public_template"
	// VisibilityPrivate is the default visibility: the owner only.
	VisibilityPrivate = "private"

	// needsFixNoteMax is the longest note of needs-fix, in characters.
	needsFixNoteMax = 1000

	// VerificationNone is the filter value of the admin list for a plan
	// whose verification_state is NULL (never sent for verification).
	VerificationNone = "none"

	adminPlansDefaultLimit = 50
	adminPlansMaxLimit     = 200
	adminPlansMaxOffset    = 1_000_000
	adminPlansMaxQueryLen  = 100
)

var verificationStates = map[string]bool{
	VerificationUnverified: true,
	VerificationVerified:   true,
	VerificationNeedsFix:   true,
}

var adminPlansIso2Re = regexp.MustCompile(`^[A-Z]{2}$`)

// ParseVerificationStateFilter reads the verification_state query value of
// the admin list: empty means every state, none means a NULL state.
func ParseVerificationStateFilter(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	return raw, raw == VerificationNone || verificationStates[raw]
}

// AdminPlansQuery is the parsed query of GET /v1/admin/seating-plans.
type AdminPlansQuery struct {
	// State is "" (every state), none or a verification state.
	State  string
	Filter gen.AdminSeatingPlanFilter
	Limit  int
	Offset int
}

// AdminPlansParamError is a refused query parameter of the admin list.
type AdminPlansParamError struct {
	Code    string
	Message string
	Field   string
	Extra   map[string]any
}

func adminPlansParamErr(code, message, field string) *AdminPlansParamError {
	return &AdminPlansParamError{Code: code, Message: message, Field: field}
}

// ParseAdminPlansQuery reads the filters and the page of the admin list.
// org_id is a filter for an authorized platform caller, never a grant.
func ParseAdminPlansQuery(q url.Values) (AdminPlansQuery, *AdminPlansParamError) {
	out := AdminPlansQuery{Limit: adminPlansDefaultLimit}
	state, ok := ParseVerificationStateFilter(q.Get("verification_state"))
	if !ok {
		return out, adminPlansParamErr("seating_plan.invalid_verification_state",
			"verification_state must be one of none|unverified|verified|needs_fix", "verification_state")
	}
	out.State = state
	if raw := strings.TrimSpace(q.Get("org_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return out, adminPlansParamErr("seating_plan.invalid_org_id", "Send org_id as a UUID.", "org_id")
		}
		out.Filter.OwnerOrgID = &id
	}
	if raw := strings.ToUpper(strings.TrimSpace(q.Get("country"))); raw != "" {
		if !adminPlansIso2Re.MatchString(raw) {
			return out, adminPlansParamErr("seating_plan.invalid_country",
				"Send country as a 2-letter ISO 3166-1 code, for example ES.", "country")
		}
		out.Filter.Country = raw
	}
	if raw := strings.TrimSpace(q.Get("city_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return out, adminPlansParamErr("seating_plan.invalid_city_id", "Send city_id as a UUID.", "city_id")
		}
		out.Filter.CityID = &id
	}
	text := strings.Join(strings.Fields(q.Get("q")), " ")
	if utf8.RuneCountInString(text) > adminPlansMaxQueryLen {
		e := adminPlansParamErr("seating_plan.invalid_query",
			"The search text is too long. Use at most 100 characters.", "q")
		e.Extra = map[string]any{"max_length": adminPlansMaxQueryLen}
		return out, e
	}
	out.Filter.VenueNamePart = escapeLike(text)
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > adminPlansMaxLimit {
			return out, adminPlansParamErr("seating_plan.invalid_limit", "limit must be a whole number from 1 to 200", "limit")
		}
		out.Limit = n
	}
	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > adminPlansMaxOffset {
			return out, adminPlansParamErr("seating_plan.invalid_offset", "offset must be a whole number from 0 to 1000000", "offset")
		}
		out.Offset = n
	}
	return out, nil
}

// escapeLike escapes the LIKE wildcards of a typed text (backslash escape).
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// adminPlansLocale is the language of city names: lang, else locale, else
// Accept-Language, else English (the rule of the venue directory).
func adminPlansLocale(r *http.Request) string {
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if lang == "" {
		lang = strings.TrimSpace(r.URL.Query().Get("locale"))
	}
	return i18n.NegotiateLocale(r.Header.Get("Accept-Language"), lang, "", "en", nil)
}

// NormalizeNeedsFixNote trims the note of needs-fix and reports whether it
// has 1 to 1000 characters.
func NormalizeNeedsFixNote(raw string) (string, bool) {
	note := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(note)
	return note, n >= 1 && n <= needsFixNoteMax
}

// IsVerifiedTemplate reports whether a plan is a venue template: visibility
// operator_verified AND verification_state verified. A plan that carries
// operator_verified from before the verification routes (state NULL) is not a
// template until the verify route is run on it.
func IsVerifiedTemplate(visibility string, verificationState *string) bool {
	return visibility == VisibilityOperatorVerified && verificationState != nil && *verificationState == VerificationVerified
}

// ForkableByOtherOrganization reports whether a plan of one organization may
// be copied by another one: an active verified template (IsVerifiedTemplate)
// or an active plan its owner opened for copying (public_template).
// shared_read is readable by other organizations but not copied.
func ForkableByOtherOrganization(status, visibility string, verificationState *string) bool {
	return status == "active" && (IsVerifiedTemplate(visibility, verificationState) || visibility == VisibilityPublicTemplate)
}

// VisibilityChange is the decision on a visibility asked for through plan
// create or PATCH.
type VisibilityChange int

const (
	// VisibilityChangeAllowed: write the asked visibility.
	VisibilityChangeAllowed VisibilityChange = iota
	// VisibilityChangeVerifyRequired: operator_verified is set only by the
	// verify route.
	VisibilityChangeVerifyRequired
	// VisibilityChangeTemplateLocked: a verified template goes back to
	// private only.
	VisibilityChangeTemplateLocked
	// VisibilityChangeLeavesTemplate: the template goes back to private and
	// its verification state to unverified.
	VisibilityChangeLeavesTemplate
)

// DecideVisibilityChange decides a visibility change from current to asked.
// current is "" for a new plan.
func DecideVisibilityChange(current, asked string) VisibilityChange {
	if asked == current {
		return VisibilityChangeAllowed
	}
	if asked == VisibilityOperatorVerified {
		return VisibilityChangeVerifyRequired
	}
	if current == VisibilityOperatorVerified {
		if asked == VisibilityPrivate {
			return VisibilityChangeLeavesTemplate
		}
		return VisibilityChangeTemplateLocked
	}
	return VisibilityChangeAllowed
}

func writeVerifyRequired(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelopeWithDetails(
		"seating_plan.verify_required",
		"operator_verified is set only when a platform superadmin verifies the plan", r,
		map[string]any{"field": "visibility"},
	))
}

func writeTemplateLocked(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
		"seating_plan.template_visibility_locked",
		"a verified venue template can change its visibility only back to private", r,
		map[string]any{"field": "visibility"},
	))
}

// ─────────────────────────────────────────────────────────────────────────────
// Response shapes
// ─────────────────────────────────────────────────────────────────────────────

// AdminSeatingPlanItem is one plan of the superadmin's verification list.
type AdminSeatingPlanItem struct {
	PlanID            string  `json:"plan_id"`
	Name              string  `json:"name"`
	PlanType          string  `json:"plan_type"`
	Visibility        string  `json:"visibility"`
	Status            string  `json:"status"`
	VerificationState *string `json:"verification_state"`
	VerificationNote  *string `json:"verification_note"`
	OwnerOrgID        string  `json:"owner_org_id"`
	OwnerOrgName      string  `json:"owner_org_name"`
	VenueID           string  `json:"venue_id"`
	VenueName         string  `json:"venue_name"`
	CityID            *string `json:"city_id"`
	CityName          *string `json:"city_name"`
	CountryIso2       *string `json:"country_iso2"`
	CurrentVersionID  *string `json:"current_version_id"`
	VersionNumber     *int32  `json:"version_number"`
	CapacitySeated    int32   `json:"capacity_seated"`
	CapacityStanding  int32   `json:"capacity_standing"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	VerifiedAt        *string `json:"verified_at"`
}

// AdminSeatingPlanCounts is the number of plans in each verification state
// under the filters of the request except verification_state.
type AdminSeatingPlanCounts struct {
	None       int64 `json:"none"`
	Unverified int64 `json:"unverified"`
	NeedsFix   int64 `json:"needs_fix"`
	Verified   int64 `json:"verified"`
}

// Total is the count of one state filter value ("" is every state).
func (c AdminSeatingPlanCounts) Total(state string) int64 {
	switch state {
	case VerificationNone:
		return c.None
	case VerificationUnverified:
		return c.Unverified
	case VerificationNeedsFix:
		return c.NeedsFix
	case VerificationVerified:
		return c.Verified
	}
	return c.None + c.Unverified + c.NeedsFix + c.Verified
}

// AdminSeatingPlanListResponse is the answer of GET /v1/admin/seating-plans.
type AdminSeatingPlanListResponse struct {
	SeatingPlans []AdminSeatingPlanItem `json:"seating_plans"`
	Total        int64                  `json:"total"`
	Limit        int                    `json:"limit"`
	Offset       int                    `json:"offset"`
	Counts       AdminSeatingPlanCounts `json:"counts"`
}

// PlanTemplateItem is one verified template of a venue.
type PlanTemplateItem struct {
	PlanID           string  `json:"plan_id"`
	Name             string  `json:"name"`
	PlanType         string  `json:"plan_type"`
	VenueID          string  `json:"venue_id"`
	CurrentVersionID string  `json:"current_version_id"`
	VersionNumber    *int32  `json:"version_number"`
	CapacitySeated   *int32  `json:"capacity_seated"`
	CapacityStanding *int32  `json:"capacity_standing"`
	VerifiedAt       *string `json:"verified_at"`
	Own              bool    `json:"own"`
}

// SeatingPlanVerificationResponse is the answer of verify and needs-fix.
type SeatingPlanVerificationResponse struct {
	PlanID            string  `json:"plan_id"`
	VenueID           string  `json:"venue_id"`
	OwnerOrgID        string  `json:"owner_org_id"`
	Status            string  `json:"status"`
	Visibility        string  `json:"visibility"`
	VerificationState string  `json:"verification_state"`
	VerificationNote  *string `json:"verification_note"`
	VerifiedAt        *string `json:"verified_at"`
	VenueReviewStatus string  `json:"venue_review_status"`
	Changed           bool    `json:"changed"`
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func uuidStrPtr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// ─────────────────────────────────────────────────────────────────────────────
// Gate
// ─────────────────────────────────────────────────────────────────────────────

// requireSuperadmin answers 403 unless the caller is a platform superadmin.
// The route's permission (seating_plan.verify) is enforced by applyAuth
// before the handler runs.
func requireSuperadmin(w http.ResponseWriter, r *http.Request) bool {
	if auth.HasSuperadminOrgAccess(r.Context()) {
		return true
	}
	httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
		"seating_plan.verify_superadmin_only", "platform superadmin access is required to verify a seating plan", r,
	))
	return false
}

func (h *Handler) dbReady(w http.ResponseWriter, r *http.Request) bool {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/seating-plans
// ─────────────────────────────────────────────────────────────────────────────

// HandleAdminListSeatingPlans lists the live seating plans for the
// superadmin, newest first, in one request: filters, one page, the total and
// the count per verification state. Archived and deleted plans and plans of
// archived or deleted venues are never listed or counted; draft plans are.
func (h *Handler) HandleAdminListSeatingPlans(w http.ResponseWriter, r *http.Request) {
	if !h.dbReady(w, r) || !requireSuperadmin(w, r) {
		return
	}
	pq, perr := ParseAdminPlansQuery(r.URL.Query())
	if perr != nil {
		details := map[string]any{"field": perr.Field}
		for k, v := range perr.Extra {
			details[k] = v
		}
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(perr.Code, perr.Message, r, details))
		return
	}
	ctx := r.Context()
	counts, err := h.queries.CountAdminSeatingPlans(ctx, pq.Filter)
	if err != nil {
		h.adminListFailed(w, r, "counts", err)
		return
	}
	rows, err := h.queries.ListAdminSeatingPlans(ctx, pq.Filter, pq.State, adminPlansLocale(r),
		int32(pq.Limit), int32(pq.Offset)) //nolint:gosec // limit 1..200, offset 0..1000000
	if err != nil {
		h.adminListFailed(w, r, "list", err)
		return
	}
	out := make([]AdminSeatingPlanItem, 0, len(rows))
	for _, p := range rows {
		item := AdminSeatingPlanItem{
			PlanID:            p.ID.String(),
			Name:              p.Name,
			PlanType:          p.PlanType,
			Visibility:        p.Visibility,
			Status:            p.Status,
			VerificationState: p.VerificationState,
			VerificationNote:  p.VerificationNote,
			OwnerOrgID:        p.OwnerOrgID.String(),
			OwnerOrgName:      p.OrgName,
			VenueID:           p.VenueID.String(),
			VenueName:         p.VenueName,
			CityID:            uuidStrPtr(p.CityID),
			CityName:          p.CityName,
			CountryIso2:       p.CountryIso2,
			CurrentVersionID:  uuidStrPtr(p.CurrentVersionID),
			VersionNumber:     p.VersionNumber,
			CreatedAt:         p.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:         p.UpdatedAt.UTC().Format(time.RFC3339),
			VerifiedAt:        rfc3339Ptr(p.VerifiedAt),
		}
		if p.CapacitySeated != nil {
			item.CapacitySeated = *p.CapacitySeated
		}
		if p.CapacityStanding != nil {
			item.CapacityStanding = *p.CapacityStanding
		}
		out = append(out, item)
	}
	c := AdminSeatingPlanCounts{
		None: counts.None, Unverified: counts.Unverified, NeedsFix: counts.NeedsFix, Verified: counts.Verified,
	}
	httputil.WriteJSON(w, http.StatusOK, AdminSeatingPlanListResponse{
		SeatingPlans: out,
		Total:        c.Total(pq.State),
		Limit:        pq.Limit,
		Offset:       pq.Offset,
		Counts:       c,
	})
}

func (h *Handler) adminListFailed(w http.ResponseWriter, r *http.Request, block string, err error) {
	h.logger.Error("seating_plan: verification list failed", slog.String("block", block), slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"seating_plan.list_failed", "failed to list seating plans", r,
	))
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/seating-plans/{plan_id}/verify
// ─────────────────────────────────────────────────────────────────────────────

// HandleVerifySeatingPlan verifies an active plan whose current version has
// valid geometry: in one transaction the plan becomes verified and the
// template of its venue (operator_verified), the venue becomes verified if it
// is not, and the audit row is written. A plan that was never sent for
// verification (state NULL) is verified the same way. Verifying a verified
// template again changes nothing and answers 200.
func (h *Handler) HandleVerifySeatingPlan(w http.ResponseWriter, r *http.Request) {
	if !h.dbReady(w, r) || !requireSuperadmin(w, r) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	planID, ok := httputil.UUIDPathParam(w, r, "plan_id")
	if !ok {
		return
	}
	ctx := r.Context()
	actor, _ := auth.ActorFromContext(ctx)
	var verifiedBy *uuid.UUID
	if id, err := uuid.Parse(actor.ID); err == nil {
		verifiedBy = &id
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.queries.WithTx(tx)

	plan, ok := h.lockPlan(ctx, w, r, qtx, planID, "seating_plan.verify_failed")
	if !ok {
		return
	}
	if plan.Status != "active" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"seating_plan.not_active", "only an active seating plan can be verified", r,
		))
		return
	}
	if plan.CurrentVersionID == nil {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"seating_plan.no_current_version", "the seating plan has no current version to verify", r,
		))
		return
	}
	version, err := qtx.GetSeatingPlanVersionByID(ctx, *plan.CurrentVersionID)
	if err != nil {
		h.logger.Error("seating_plan: verify version read failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.verify_failed", "failed to verify seating plan", r,
		))
		return
	}
	var geometry seating.Geometry
	if err := json.Unmarshal(version.Geometry, &geometry); err != nil {
		writeVerifyGeometryInvalid(w, r, seating.ValidationErrors{{
			Code: "geometry_unreadable", Element: "geometry", Detail: err.Error(),
		}})
		return
	}
	if verrs := seating.ValidateForPlanType(geometry, plan.PlanType); len(verrs) > 0 {
		writeVerifyGeometryInvalid(w, r, verrs)
		return
	}

	alreadyTemplate := plan.VerificationState != nil && *plan.VerificationState == VerificationVerified &&
		plan.Visibility == VisibilityOperatorVerified
	changed := false
	if !alreadyTemplate {
		vis := VisibilityOperatorVerified
		n, err := qtx.SetSeatingPlanVerification(ctx, gen.SetSeatingPlanVerificationParams{
			PlanID: plan.ID, State: VerificationVerified, VerifiedBy: verifiedBy, Visibility: &vis,
		})
		if err != nil || n == 0 {
			h.logVerifyErr("seating_plan: verify update failed", err)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seating_plan.verify_failed", "failed to verify seating plan", r,
			))
			return
		}
		changed = true
	}
	venueChanged, err := qtx.MarkVenueVerifiedIfNot(ctx, plan.VenueID)
	if err != nil {
		h.logger.Error("seating_plan: verify venue update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.verify_failed", "failed to verify seating plan", r,
		))
		return
	}
	if venueChanged > 0 {
		changed = true
	}
	if changed {
		prev := ""
		if plan.VerificationState != nil {
			prev = *plan.VerificationState
		}
		if err := h.writeAdminAuditTx(ctx, tx, r, "v1.seating_plan.verify", plan.ID.String(), map[string]any{
			"owner_org_id":             plan.OwnerOrgID.String(),
			"venue_id":                 plan.VenueID.String(),
			"seating_plan_version_id":  version.ID.String(),
			"geometry_checksum":        version.GeometryChecksum,
			"previous_state":           prev,
			"previous_visibility":      plan.Visibility,
			"venue_verified_with_plan": venueChanged > 0,
			"reason":                   reason,
		}); err != nil {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seating_plan.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}
	h.finishVerification(ctx, w, r, tx, qtx, plan.ID, changed)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/seating-plans/{plan_id}/needs-fix
// ─────────────────────────────────────────────────────────────────────────────

type needsFixRequest struct {
	Note string `json:"note"`
}

// HandleNeedsFixSeatingPlan marks a plan needs_fix with the superadmin's note.
// A plan that was a venue template (operator_verified) goes back to private,
// so no organization can copy it any more; copies made before keep working.
// The venue keeps its review status.
func (h *Handler) HandleNeedsFixSeatingPlan(w http.ResponseWriter, r *http.Request) {
	if !h.dbReady(w, r) || !requireSuperadmin(w, r) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	planID, ok := httputil.UUIDPathParam(w, r, "plan_id")
	if !ok {
		return
	}
	var body needsFixRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"seating_plan.invalid_body", "request body must be JSON with note", r,
		))
		return
	}
	note, ok := NormalizeNeedsFixNote(body.Note)
	if !ok {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating_plan.invalid_note", "note must have 1 to 1000 characters", r,
			map[string]any{"field": "note"},
		))
		return
	}
	ctx := r.Context()

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.queries.WithTx(tx)

	plan, ok := h.lockPlan(ctx, w, r, qtx, planID, "seating_plan.needs_fix_failed")
	if !ok {
		return
	}
	var vis *string
	if plan.Visibility == VisibilityOperatorVerified {
		p := VisibilityPrivate
		vis = &p
	}
	n, err := qtx.SetSeatingPlanVerification(ctx, gen.SetSeatingPlanVerificationParams{
		PlanID: plan.ID, State: VerificationNeedsFix, Note: &note, Visibility: vis,
	})
	if err != nil || n == 0 {
		h.logVerifyErr("seating_plan: needs-fix update failed", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.needs_fix_failed", "failed to mark the seating plan", r,
		))
		return
	}
	prev := ""
	if plan.VerificationState != nil {
		prev = *plan.VerificationState
	}
	if err := h.writeAdminAuditTx(ctx, tx, r, "v1.seating_plan.needs_fix", plan.ID.String(), map[string]any{
		"owner_org_id":        plan.OwnerOrgID.String(),
		"venue_id":            plan.VenueID.String(),
		"previous_state":      prev,
		"previous_visibility": plan.Visibility,
		"note":                note,
		"reason":              reason,
	}); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.audit_failed", "failed to write audit event", r,
		))
		return
	}
	h.finishVerification(ctx, w, r, tx, qtx, plan.ID, true)
}

// lockPlan reads and locks the plan; a missing or deleted plan is 404.
func (h *Handler) lockPlan(ctx context.Context, w http.ResponseWriter, r *http.Request, qtx *gen.Queries, planID uuid.UUID, failCode string) (gen.SeatingPlanVerificationLock, bool) {
	plan, err := qtx.LockSeatingPlanForVerification(ctx, planID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"seating_plan.not_found", "seating plan not found", r,
			))
			return plan, false
		}
		h.logger.Error("seating_plan: verification lock failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			failCode, "failed to read the seating plan", r,
		))
		return plan, false
	}
	return plan, true
}

// finishVerification re-reads the plan, commits (or leaves an unchanged
// transaction to the deferred rollback) and writes the answer.
func (h *Handler) finishVerification(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, qtx *gen.Queries, planID uuid.UUID, changed bool) {
	after, err := qtx.LockSeatingPlanForVerification(ctx, planID)
	if err != nil {
		h.logger.Error("seating_plan: verification re-read failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.verify_failed", "failed to read the seating plan", r,
		))
		return
	}
	states, err := qtx.ListVenueReviewStates(ctx, []uuid.UUID{after.VenueID})
	if err != nil {
		h.logger.Error("seating_plan: venue review read failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.verify_failed", "failed to read the venue", r,
		))
		return
	}
	if changed {
		if err := tx.Commit(ctx); err != nil {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seating_plan.commit_failed", "failed to commit transaction", r,
			))
			return
		}
	}
	state := ""
	if after.VerificationState != nil {
		state = *after.VerificationState
	}
	httputil.WriteJSON(w, http.StatusOK, SeatingPlanVerificationResponse{
		PlanID:            after.ID.String(),
		VenueID:           after.VenueID.String(),
		OwnerOrgID:        after.OwnerOrgID.String(),
		Status:            after.Status,
		Visibility:        after.Visibility,
		VerificationState: state,
		VerificationNote:  after.VerificationNote,
		VerifiedAt:        rfc3339Ptr(after.VerifiedAt),
		VenueReviewStatus: states[after.VenueID].ReviewStatus,
		Changed:           changed,
	})
}

func (h *Handler) logVerifyErr(msg string, err error) {
	if err == nil {
		err = errors.New("no row changed")
	}
	h.logger.Error(msg, slog.String("error", err.Error()))
}

func writeVerifyGeometryInvalid(w http.ResponseWriter, r *http.Request, errs seating.ValidationErrors) {
	details := make([]map[string]any, 0, len(errs))
	for _, e := range errs {
		details = append(details, map[string]any{
			"code":    e.Code,
			"element": e.Element,
			"detail":  e.Detail,
		})
	}
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		"seating_plan.geometry_invalid",
		"the current version of the seating plan fails geometry validation", r,
		map[string]any{"errors": details},
	))
}

// writeAdminAuditTx writes a durable audit row of a superadmin action inside
// the transaction. Unlike writeAuditTx it fails when no audit writer is wired:
// a verification without its audit row must not commit.
func (h *Handler) writeAdminAuditTx(ctx context.Context, tx pgx.Tx, r *http.Request, action, resourceID string, metadata map[string]any) error {
	if h.audit == nil {
		h.logger.Error("seating_plan: audit writer is not wired", slog.String("action", action))
		return errors.New("audit writer is not wired")
	}
	actor, _ := auth.ActorFromContext(ctx)
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "seating_plan",
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
		h.logger.Error("seating_plan: audit write failed", slog.String("error", err.Error()))
		return err
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/venues/{venue_id}/plan-templates
// ─────────────────────────────────────────────────────────────────────────────

// HandleListPlanTemplates lists the verified templates of a venue the path
// organization may use: its own venue, or a venue of another organization
// that the platform verified (migration 0182). Any other venue is 404
// venue.not_found. Membership of {org_id} is checked by the shim.
func (h *Handler) HandleListPlanTemplates(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	venueID, ok := httputil.UUIDPathParam(w, r, "venue_id")
	if !ok {
		return
	}
	ctx := r.Context()
	venue, err := h.queries.GetVenueSessionContext(ctx, venueID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.logger.Error("seating_plan: template venue read failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.list_failed", "failed to list plan templates", r,
		))
		return
	}
	if err != nil || (venue.OrgID != orgID && !venue.Verified) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"venue.not_found", "venue not found", r,
		))
		return
	}
	rows, err := h.queries.ListVerifiedSeatingPlansOfVenue(ctx, venueID)
	if err != nil {
		h.logger.Error("seating_plan: template list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.list_failed", "failed to list plan templates", r,
		))
		return
	}
	out := make([]PlanTemplateItem, 0, len(rows))
	for _, p := range rows {
		if p.CurrentVersionID == nil {
			continue
		}
		out = append(out, PlanTemplateItem{
			PlanID:           p.ID.String(),
			Name:             p.Name,
			PlanType:         p.PlanType,
			VenueID:          p.VenueID.String(),
			CurrentVersionID: p.CurrentVersionID.String(),
			VersionNumber:    p.VersionNumber,
			CapacitySeated:   p.CapacitySeated,
			CapacityStanding: p.CapacityStanding,
			VerifiedAt:       rfc3339Ptr(p.VerifiedAt),
			Own:              p.OwnerOrgID == orgID,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"plan_templates": out})
}
