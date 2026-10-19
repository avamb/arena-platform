package hbilling

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

const maxNoteLen = 500

type createRateBody struct {
	OrgID         string     `json:"org_id"`
	Currency      string     `json:"currency"`
	UnseatedMinor *int64     `json:"unseated_minor"`
	SeatedMinor   *int64     `json:"seated_minor"`
	EffectiveFrom *time.Time `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
	Note          string     `json:"note"`
}

type closeRateBody struct {
	EffectiveTo *time.Time `json:"effective_to"`
}

func rateJSON(o gen.OrgRateOverrideRow, orgName string, now time.Time) map[string]any {
	status := "in_force"
	switch {
	case o.EffectiveFrom.After(now):
		status = "scheduled"
	case o.EffectiveTo != nil && !o.EffectiveTo.After(now):
		status = "ended"
	}
	out := map[string]any{
		"id":             o.ID.String(),
		"org_id":         o.OrgID.String(),
		"currency":       o.Currency,
		"unseated_minor": o.UnseatedMinor,
		"seated_minor":   o.SeatedMinor,
		"effective_from": o.EffectiveFrom.UTC().Format(time.RFC3339Nano),
		"effective_to":   timeJSON(o.EffectiveTo),
		"note":           o.Note,
		"created_by":     o.CreatedBy,
		"created_at":     o.CreatedAt.UTC().Format(time.RFC3339Nano),
		"status":         status,
	}
	if orgName != "" {
		out["org_name"] = orgName
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/billing/rate-overrides
// ─────────────────────────────────────────────────────────────────────────────

// HandleListRateOverrides lists individual rates, every period of them.
func (h *AdminHandler) HandleListRateOverrides(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	orgID := uuid.Nil
	if raw := strings.TrimSpace(r.URL.Query().Get("org_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			unprocessable(w, r, "billing.invalid_org_id", "org_id must be a UUID")
			return
		}
		orgID = id
	}
	currency := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("currency")))
	rows, err := h.queries.ListOrgRateOverridesAdmin(r.Context(), orgID, currency)
	if err != nil {
		h.fail(w, r, "list rate overrides", err)
		return
	}
	now := h.now()
	items := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		items = append(items, rateJSON(x.OrgRateOverrideRow, x.OrgName, now))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/billing/rate-overrides
// ─────────────────────────────────────────────────────────────────────────────

// HandleCreateRateOverride inserts an individual rate that starts in the
// future after the organization's earlier rates of that currency, and ends
// the earlier rate that is still open at that moment.
func (h *AdminHandler) HandleCreateRateOverride(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	var body createRateBody
	if !decodeBody(w, r, &body) {
		return
	}
	now := h.now()
	orgID, err := uuid.Parse(body.OrgID)
	switch {
	case err != nil:
		unprocessable(w, r, "billing.invalid_rate", "org_id must be the UUID of an organization")
		return
	case !currencyCode.MatchString(body.Currency):
		unprocessable(w, r, "billing.invalid_rate", "currency must be three capital letters, for example EUR")
		return
	case body.UnseatedMinor == nil || body.SeatedMinor == nil:
		unprocessable(w, r, "billing.invalid_rate", "unseated_minor and seated_minor are both required, in minor units of the currency")
		return
	case *body.UnseatedMinor < 0 || *body.SeatedMinor < 0:
		unprocessable(w, r, "billing.invalid_rate", "a rate cannot be negative")
		return
	case body.EffectiveFrom == nil:
		unprocessable(w, r, "billing.invalid_rate", "effective_from is required")
		return
	case !body.EffectiveFrom.After(now):
		unprocessable(w, r, "billing.effective_from_not_future",
			"effective_from must be in the future: a rate never starts in the past, so it cannot change a sale that already happened")
		return
	case body.EffectiveTo != nil && !body.EffectiveTo.After(*body.EffectiveFrom):
		unprocessable(w, r, "billing.invalid_period", "effective_to must be after effective_from")
		return
	case len([]rune(body.Note)) > maxNoteLen:
		unprocessable(w, r, "billing.invalid_rate", "note is too long (500 characters at most)")
		return
	}
	from := body.EffectiveFrom.UTC()
	var to *time.Time
	if body.EffectiveTo != nil {
		t := body.EffectiveTo.UTC()
		to = &t
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.queries.WithTx(tx)
	exists, err := q.OrganizationExistsForBilling(ctx, orgID)
	if err != nil {
		h.fail(w, r, "check organization", err)
		return
	}
	if !exists {
		notFound(w, r, "billing.organization_not_found", "this organization does not exist")
		return
	}
	if err := q.LockOrgRateSeries(ctx, orgID, body.Currency); err != nil {
		h.fail(w, r, "lock series", err)
		return
	}
	latest, err := q.GetOrgRateSeriesLatestFrom(ctx, orgID, body.Currency)
	if err != nil {
		h.fail(w, r, "series state", err)
		return
	}
	if latest != nil && !from.After(*latest) {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"billing.rate_override_overlap",
			"the new rate must start after this organization's latest rate in "+body.Currency+", which starts "+
				latest.UTC().Format(time.RFC3339)+"; close or remove that rate first or pick a later start",
			r, map[string]any{"latest_effective_from": latest.UTC().Format(time.RFC3339Nano)}))
		return
	}
	closed, err := q.CloseOrgRateOverridesAt(ctx, orgID, body.Currency, from)
	if err != nil {
		h.fail(w, r, "close earlier rate", err)
		return
	}
	created, err := q.InsertOrgRateOverride(ctx, gen.InsertOrgRateOverrideParams{
		OrgID: orgID, Currency: body.Currency, UnseatedMinor: *body.UnseatedMinor, SeatedMinor: *body.SeatedMinor,
		EffectiveFrom: from, EffectiveTo: to, Note: ptrString(strings.TrimSpace(body.Note)), CreatedBy: ptrString(actorID(r)),
	})
	if err != nil {
		h.fail(w, r, "insert rate", err)
		return
	}
	closedIDs := make([]string, len(closed))
	for i, id := range closed {
		closedIDs[i] = id.String()
	}
	if err := h.auditTx(r, tx, AuditRateOverrideCreate, "org_rate_override", created.ID, reason, map[string]any{
		"org_id": orgID.String(), "currency": created.Currency,
		"unseated_minor": created.UnseatedMinor, "seated_minor": created.SeatedMinor,
		"effective_from": created.EffectiveFrom.UTC().Format(time.RFC3339Nano),
		"effective_to":   timeJSON(created.EffectiveTo), "closed_rate_ids": closedIDs,
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	out := rateJSON(created, "", now)
	out["closed_rate_ids"] = closedIDs
	httputil.WriteJSON(w, http.StatusCreated, out)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/billing/rate-overrides/{id}/close
// ─────────────────────────────────────────────────────────────────────────────

// HandleCloseRateOverride sets the end of an individual rate. The end must be
// in the future, after the rate's start, and not past the start of the
// organization's next rate; a rate that has already ended is not touched.
func (h *AdminHandler) HandleCloseRateOverride(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var body closeRateBody
	if !decodeBody(w, r, &body) {
		return
	}
	now := h.now()
	if body.EffectiveTo == nil {
		unprocessable(w, r, "billing.invalid_period", "effective_to is required")
		return
	}
	end := body.EffectiveTo.UTC()
	if !end.After(now) {
		unprocessable(w, r, "billing.invalid_period",
			"effective_to must be in the future: an individual rate is never closed backwards, sales it already priced stay as they are")
		return
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.queries.WithTx(tx)
	cur, err := q.LockOrgRateOverrideByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r, "billing.rate_override_not_found", "this individual rate does not exist")
		return
	}
	if err != nil {
		h.fail(w, r, "lock rate", err)
		return
	}
	if cur.EffectiveTo != nil && !cur.EffectiveTo.After(now) {
		conflict(w, r, "billing.rate_override_ended", "this individual rate has already ended")
		return
	}
	if !end.After(cur.EffectiveFrom) {
		unprocessable(w, r, "billing.invalid_period", "effective_to must be after the start of the rate")
		return
	}
	next, err := q.NextOrgRateOverrideFrom(ctx, cur.OrgID, cur.Currency, cur.EffectiveFrom)
	if err != nil {
		h.fail(w, r, "next rate", err)
		return
	}
	if next != nil && end.After(*next) {
		conflict(w, r, "billing.rate_override_overlap",
			"effective_to is later than the start of this organization's next rate ("+next.UTC().Format(time.RFC3339)+")")
		return
	}
	updated, err := q.SetOrgRateOverrideEnd(ctx, id, end, now)
	if errors.Is(err, pgx.ErrNoRows) {
		conflict(w, r, "billing.rate_override_ended", "this individual rate has just ended")
		return
	}
	if err != nil {
		h.fail(w, r, "close rate", err)
		return
	}
	if err := h.auditTx(r, tx, AuditRateOverrideClose, "org_rate_override", updated.ID, reason, map[string]any{
		"org_id": updated.OrgID.String(), "currency": updated.Currency,
		"effective_from":   updated.EffectiveFrom.UTC().Format(time.RFC3339Nano),
		"old_effective_to": timeJSON(cur.EffectiveTo), "effective_to": timeJSON(updated.EffectiveTo),
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, rateJSON(updated, "", now))
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/admin/billing/rate-overrides/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleDeleteRateOverride removes an individual rate that has not started.
// One that started stays: it is the record of how sales were priced.
func (h *AdminHandler) HandleDeleteRateOverride(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.queries.WithTx(tx)
	cur, err := q.LockOrgRateOverrideByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r, "billing.rate_override_not_found", "this individual rate does not exist")
		return
	}
	if err != nil {
		h.fail(w, r, "lock rate", err)
		return
	}
	n, err := q.DeleteNotStartedOrgRateOverride(ctx, id, h.now())
	if err != nil {
		h.fail(w, r, "delete rate", err)
		return
	}
	if n == 0 {
		conflict(w, r, "billing.rate_override_started",
			"this individual rate has already started and cannot be removed: close it with an end date instead")
		return
	}
	if err := h.auditTx(r, tx, AuditRateOverrideDelete, "org_rate_override", cur.ID, reason, map[string]any{
		"org_id": cur.OrgID.String(), "currency": cur.Currency,
		"unseated_minor": cur.UnseatedMinor, "seated_minor": cur.SeatedMinor,
		"effective_from": cur.EffectiveFrom.UTC().Format(time.RFC3339Nano),
		"effective_to":   timeJSON(cur.EffectiveTo),
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
