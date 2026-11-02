package hbilling

// admin_white_label.go - white label periods of a brand (spec
// 08_architecture/53, owner decisions 2026-11-02): a platform superadmin
// turns white label on for an organization (its events without a promoter)
// or for one of its promoters, from a day to a day. The organizer sees it
// read-only (GET /v1/organizations/{org_id}/billing/white-label).
//
// Rules of the screen:
//   - a period never starts in the past (UTC day): it cannot reach a day the
//     platform badge was already shown, nor a month already charged;
//   - two periods of one brand never share a day;
//   - a period is ended, never deleted, and its end only moves earlier and
//     never before yesterday: a day the badge was hidden stays charged;
//   - monthly_price_minor is an individual price; absent means the price
//     list, 0 is free (the existing clients, decision V-7). It is stored with
//     the currency of the organization's billing account on that day; the
//     operator may name the currency they mean, and a different one is
//     refused, so 45 typed as euros is never billed as dollars.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/whitelabel"
)

// Audit actions of the white label routes.
const (
	AuditWhiteLabelCreate = "v1.admin.billing.white_label.create"
	AuditWhiteLabelEnd    = "v1.admin.billing.white_label.end"
)

const dateLayout = "2006-01-02"

type createWhiteLabelBody struct {
	OrgID             string  `json:"org_id"`
	PromoterID        *string `json:"promoter_id"`
	StartsOn          string  `json:"starts_on"`
	EndsOn            *string `json:"ends_on"`
	MonthlyPriceMinor *int64  `json:"monthly_price_minor"`
	Currency          *string `json:"currency"`
}

type endWhiteLabelBody struct {
	EndsOn string `json:"ends_on"`
}

// WhiteLabelStatus is "scheduled", "in_force" or "ended" on day.
func WhiteLabelStatus(p gen.WhiteLabelPeriodRow, day time.Time) string {
	switch {
	case p.StartsOn.After(day):
		return "scheduled"
	case p.EndsOn != nil && p.EndsOn.Before(day):
		return "ended"
	}
	return "in_force"
}

func dateJSON(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(dateLayout)
}

func whiteLabelJSON(p gen.WhiteLabelPeriodRow, day time.Time) map[string]any {
	out := map[string]any{
		"id":                  p.ID.String(),
		"org_id":              p.OrgID.String(),
		"promoter_id":         nil,
		"starts_on":           p.StartsOn.Format(dateLayout),
		"ends_on":             dateJSON(p.EndsOn),
		"monthly_price_minor": p.MonthlyPriceMinor,
		"currency":            p.Currency,
		"reason":              p.Reason,
		"created_at":          p.CreatedAt.UTC().Format(time.RFC3339Nano),
		"ended_at":            timeJSON(p.EndedAt),
		"status":              WhiteLabelStatus(p, day),
	}
	if p.PromoterID != nil {
		out["promoter_id"] = p.PromoterID.String()
	}
	return out
}

func parseDay(s string) (time.Time, bool) {
	t, err := time.Parse(dateLayout, strings.TrimSpace(s))
	return t, err == nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/billing/white-label
// ─────────────────────────────────────────────────────────────────────────────

// HandleListWhiteLabel lists white label periods, of one organization when
// org_id is given.
func (h *AdminHandler) HandleListWhiteLabel(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	var orgID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("org_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			unprocessable(w, r, "billing.invalid_org_id", "org_id must be a UUID")
			return
		}
		orgID = &id
	}
	rows, err := h.queries.ListWhiteLabelPeriods(r.Context(), orgID)
	if err != nil {
		h.fail(w, r, "list white label", err)
		return
	}
	day := whitelabel.Today(h.now())
	items := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		item := whiteLabelJSON(x.WhiteLabelPeriodRow, day)
		item["org_name"] = x.OrgName
		item["promoter_name"] = x.PromoterName
		items = append(items, item)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/billing/white-label
// ─────────────────────────────────────────────────────────────────────────────

// HandleCreateWhiteLabel turns white label on for a brand from starts_on
// (today or later) to ends_on (open when absent).
func (h *AdminHandler) HandleCreateWhiteLabel(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	var body createWhiteLabelBody
	if !decodeBody(w, r, &body) {
		return
	}
	orgID, err := uuid.Parse(strings.TrimSpace(body.OrgID))
	if err != nil {
		unprocessable(w, r, "billing.invalid_white_label", "org_id must be the UUID of an organization")
		return
	}
	var promoterID *uuid.UUID
	if body.PromoterID != nil && strings.TrimSpace(*body.PromoterID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(*body.PromoterID))
		if err != nil {
			unprocessable(w, r, "billing.invalid_white_label", "promoter_id must be the UUID of a promoter of the organization")
			return
		}
		promoterID = &id
	}
	startsOn, ok := parseDay(body.StartsOn)
	if !ok {
		unprocessable(w, r, "billing.invalid_white_label", "starts_on must be a date, for example 2026-12-01")
		return
	}
	var endsOn *time.Time
	if body.EndsOn != nil && strings.TrimSpace(*body.EndsOn) != "" {
		e, ok := parseDay(*body.EndsOn)
		if !ok {
			unprocessable(w, r, "billing.invalid_white_label", "ends_on must be a date, for example 2027-11-30")
			return
		}
		if e.Before(startsOn) {
			unprocessable(w, r, "billing.invalid_period", "ends_on must be on or after starts_on")
			return
		}
		endsOn = &e
	}
	if body.MonthlyPriceMinor != nil && (*body.MonthlyPriceMinor < 0 || *body.MonthlyPriceMinor > 1_000_000_000) {
		unprocessable(w, r, "billing.invalid_white_label", "monthly_price_minor must be 0 or more, in minor units of the account currency")
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
	exists, err := q.OrganizationExistsForBilling(ctx, orgID)
	if err != nil {
		h.fail(w, r, "check organization", err)
		return
	}
	if !exists {
		notFound(w, r, "billing.organization_not_found", "this organization does not exist")
		return
	}
	var currency *string
	if body.MonthlyPriceMinor != nil {
		country, err := q.GetOrgBillingCountry(ctx, orgID)
		if err != nil {
			h.fail(w, r, "billing country", err)
			return
		}
		cur, _, err := billing.RouteCurrency(ctx, q, country)
		if err != nil {
			h.fail(w, r, "billing currency", err)
			return
		}
		if body.Currency != nil && strings.TrimSpace(*body.Currency) != "" &&
			!strings.EqualFold(strings.TrimSpace(*body.Currency), cur) {
			unprocessable(w, r, "billing.white_label_currency_mismatch",
				"this organization is billed in "+cur+": type the individual price in "+cur)
			return
		}
		currency = &cur
	}
	if promoterID != nil {
		if _, err := q.GetOrgPromoter(ctx, *promoterID, orgID); errors.Is(err, pgx.ErrNoRows) {
			notFound(w, r, "billing.promoter_not_found", "this promoter does not belong to the organization")
			return
		} else if err != nil {
			h.fail(w, r, "check promoter", err)
			return
		}
	}
	if err := q.LockWhiteLabelBrand(ctx, orgID, promoterID); err != nil {
		h.fail(w, r, "lock brand", err)
		return
	}
	today := whitelabel.Today(h.now()) // judged after the lock wait
	if startsOn.Before(today) {
		unprocessable(w, r, "billing.starts_on_in_past",
			"starts_on must be today or later: white label never starts in the past, so it cannot change a day or a month already charged")
		return
	}
	n, err := q.CountOverlappingWhiteLabelPeriods(ctx, orgID, promoterID, startsOn, endsOn)
	if err != nil {
		h.fail(w, r, "overlap", err)
		return
	}
	if n > 0 {
		conflict(w, r, "billing.white_label_overlap",
			"this brand already has white label on some of these days; end that period first or pick other dates")
		return
	}
	created, err := q.InsertWhiteLabelPeriod(ctx, gen.InsertWhiteLabelPeriodParams{
		OrgID: orgID, PromoterID: promoterID, StartsOn: startsOn, EndsOn: endsOn,
		MonthlyPriceMinor: body.MonthlyPriceMinor, Currency: currency, Reason: reason, CreatedBy: actorUUID(r),
	})
	if err != nil {
		h.fail(w, r, "insert white label", err)
		return
	}
	meta := map[string]any{
		"org_id": orgID.String(), "starts_on": created.StartsOn.Format(dateLayout),
		"ends_on": dateJSON(created.EndsOn), "monthly_price_minor": created.MonthlyPriceMinor,
		"currency": created.Currency,
	}
	if promoterID != nil {
		meta["promoter_id"] = promoterID.String()
	}
	if err := h.auditTx(r, tx, AuditWhiteLabelCreate, "white_label_period", created.ID, reason, meta); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, whiteLabelJSON(created, today))
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/billing/white-label/{id}/end
// ─────────────────────────────────────────────────────────────────────────────

// HandleEndWhiteLabel sets the last day of a period. The last day is
// yesterday or later (yesterday ends white label now), never later than the
// current end, and may be the day before a period that has not started (the
// period then never takes effect).
func (h *AdminHandler) HandleEndWhiteLabel(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var body endWhiteLabelBody
	if !decodeBody(w, r, &body) {
		return
	}
	endsOn, ok := parseDay(body.EndsOn)
	if !ok {
		unprocessable(w, r, "billing.invalid_period", "ends_on must be a date, for example 2026-12-31")
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
	cur, err := q.GetWhiteLabelPeriod(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r, "billing.white_label_not_found", "this white label period does not exist")
		return
	}
	if err != nil {
		h.fail(w, r, "get white label", err)
		return
	}
	if err := q.LockWhiteLabelBrand(ctx, cur.OrgID, cur.PromoterID); err != nil {
		h.fail(w, r, "lock brand", err)
		return
	}
	today := whitelabel.Today(h.now())
	yesterday := today.AddDate(0, 0, -1)
	switch {
	case endsOn.Before(yesterday):
		unprocessable(w, r, "billing.invalid_period",
			"ends_on must be yesterday or later: days the badge was already hidden stay charged")
		return
	case endsOn.Before(cur.StartsOn.AddDate(0, 0, -1)):
		unprocessable(w, r, "billing.invalid_period", "ends_on cannot be earlier than the day before the period starts")
		return
	case cur.EndsOn != nil && !endsOn.Before(*cur.EndsOn):
		unprocessable(w, r, "billing.invalid_period",
			"ends_on must be earlier than the current end ("+cur.EndsOn.Format(dateLayout)+"): ending a period never extends it")
		return
	}
	updated, err := q.EndWhiteLabelPeriod(ctx, id, endsOn, actorUUID(r))
	if errors.Is(err, pgx.ErrNoRows) {
		conflict(w, r, "billing.white_label_ended", "this period already ends on or before that day")
		return
	}
	if err != nil {
		h.fail(w, r, "end white label", err)
		return
	}
	if err := h.auditTx(r, tx, AuditWhiteLabelEnd, "white_label_period", updated.ID, reason, map[string]any{
		"org_id": updated.OrgID.String(), "old_ends_on": dateJSON(cur.EndsOn), "ends_on": dateJSON(updated.EndsOn),
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, whiteLabelJSON(updated, today))
}

// actorUUID is the caller's id as a UUID for created_by columns, nil when it
// is not one.
func actorUUID(r *http.Request) *uuid.UUID {
	id, err := uuid.Parse(actorID(r))
	if err != nil {
		return nil
	}
	return &id
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/billing/white-label (organizer, read-only)
// ─────────────────────────────────────────────────────────────────────────────

// HandleOrgWhiteLabel lists the organization's white label periods with the
// monthly price that applies: the period's individual price, else the price
// list's for the organization's billing account.
func (h *ChargesHandler) HandleOrgWhiteLabel(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		h.unavailable(w, r)
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	ctx := r.Context()
	rows, err := h.queries.ListWhiteLabelPeriods(ctx, &orgID)
	if err != nil {
		h.internal(w, r, "list white label", err)
		return
	}
	now := time.Now().UTC()
	day := whitelabel.Today(now)
	listMonthly, currency := h.listWhiteLabelPrice(r, orgID, now)
	items := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		item := whiteLabelJSON(x.WhiteLabelPeriodRow, day)
		delete(item, "reason")
		item["promoter_name"] = x.PromoterName
		item["brand_name"] = x.OrgName
		if x.PromoterName != nil {
			item["brand_name"] = *x.PromoterName
		}
		monthly, cur := listMonthly, currency
		if x.MonthlyPriceMinor != nil {
			v := *x.MonthlyPriceMinor
			monthly = &v
			if x.Currency != nil {
				cur = *x.Currency
			}
		}
		item["effective_monthly_minor"] = monthly
		item["currency"] = cur
		items = append(items, item)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "currency": currency,
		"price_list_monthly_minor": listMonthly})
}

// listWhiteLabelPrice reads the price list's monthly white label price for
// the organization's billing account today. It never creates an account: an
// organization without one, or a price list without the rule, answers nil.
func (h *ChargesHandler) listWhiteLabelPrice(r *http.Request, orgID uuid.UUID, now time.Time) (*int64, string) {
	ctx := r.Context()
	country, err := h.queries.GetOrgBillingCountry(ctx, orgID)
	if err != nil {
		return nil, ""
	}
	currency, _, err := billing.RouteCurrency(ctx, h.queries, country)
	if err != nil {
		return nil, ""
	}
	acc, err := h.queries.GetBillingAccount(ctx, orgID, currency, billing.AccountKindOrganizer)
	if err != nil {
		return nil, currency
	}
	terms, err := billing.TermsAt(ctx, h.queries, acc.ID, now)
	if err != nil {
		return nil, currency
	}
	if v, ok := terms.WhiteLabelMonthlyMinor(); ok {
		return &v, currency
	}
	return nil, currency
}
