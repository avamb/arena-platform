package hmodules

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orgmodules"
)

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/organizations/{org_id}/modules
// ─────────────────────────────────────────────────────────────────────────────

// HandleAdminList lists every module period of the organization (all fields)
// and the state of its shop: mode, storefront switch, domain and the monthly
// price that applies today.
func (h *Handler) HandleAdminList(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	ctx := r.Context()
	if !h.orgExists(w, r, h.queries, orgID) {
		return
	}
	now := h.now()
	day := orgmodules.Day(now)
	periods, err := orgmodules.List(ctx, h.queries, orgID)
	if err != nil {
		h.fail(w, r, "list periods", err)
		return
	}
	states, err := orgmodules.States(ctx, h.queries, []uuid.UUID{orgID}, now)
	if err != nil {
		h.fail(w, r, "module state", err)
		return
	}
	st := states[orgID]
	enabled, domain, err := h.storefront(ctx, orgID)
	if err != nil {
		h.fail(w, r, "shop settings", err)
		return
	}
	items := make([]map[string]any, 0, len(periods))
	for _, p := range periods {
		items = append(items, periodJSON(p, day, true))
	}
	price := h.effective(ctx, orgID, periods, now)
	var mode any
	if st.ShopMode != orgmodules.ModeNone {
		mode = string(st.ShopMode)
	}
	modules := st.Modules
	if modules == nil {
		modules = []string{}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"org_id":  orgID.String(),
		"modules": modules,
		"items":   items,
		"shop": map[string]any{
			"mode":                    mode,
			"module_active":           st.ShopMode == orgmodules.ModeFull,
			"open_shop_orders":        st.OpenShopOrders,
			"enabled":                 enabled,
			"shop_domain":             domain,
			"effective_monthly_minor": price.MonthlyMinor,
			"currency":                price.Currency,
			"price_source":            price.source(),
		},
	})
}

// storefront reads the shop's switch and host; an organization that never
// saved its shop settings is closed.
func (h *Handler) storefront(ctx context.Context, orgID uuid.UUID) (bool, *string, error) {
	st, err := h.queries.GetShopSettings(ctx, orgID)
	if err == nil {
		return st.Enabled, st.ShopDomain, nil
	}
	if !isNoRows(err) {
		return false, nil, err
	}
	domain, err := h.queries.GetOrganizationShopDomain(ctx, orgID)
	if err != nil && !isNoRows(err) {
		return false, nil, err
	}
	return false, domain, nil
}

// orgExists answers 404 org_module.organization_not_found for an unknown
// organization.
func (h *Handler) orgExists(w http.ResponseWriter, r *http.Request, q *gen.Queries, orgID uuid.UUID) bool {
	exists, err := q.OrganizationExistsForBilling(r.Context(), orgID)
	if err != nil {
		h.fail(w, r, "check organization", err)
		return false
	}
	if !exists {
		writeErr(w, r, http.StatusNotFound, "org_module.organization_not_found", "this organization does not exist")
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/organizations/{org_id}/modules/shop
// ─────────────────────────────────────────────────────────────────────────────

type connectBody struct {
	StartsOn          *string `json:"starts_on"`
	EndsOn            *string `json:"ends_on"`
	MonthlyPriceMinor *int64  `json:"monthly_price_minor"`
	Currency          *string `json:"currency"`
	Reason            *string `json:"reason"`
}

// periodReason is the reason stored on a period: the body's, else the
// operator's X-Admin-Reason.
func periodReason(body *string, header string) string {
	if body != nil && strings.TrimSpace(*body) != "" {
		return strings.TrimSpace(*body)
	}
	return header
}

// HandleConnectShop connects the shop module from starts_on (today, UTC, when
// absent) to ends_on (no end when absent). No price is the price list; a
// price without a currency is in the organization's billing currency.
func (h *Handler) HandleConnectShop(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, true) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	var body connectBody
	if !decodeBody(w, r, &body) {
		return
	}
	now := h.now()
	startsOn := orgmodules.Day(now)
	if body.StartsOn != nil && strings.TrimSpace(*body.StartsOn) != "" {
		d, ok := parseDay(*body.StartsOn)
		if !ok {
			writeErr(w, r, http.StatusUnprocessableEntity, "org_module.invalid", "starts_on must be a date, for example 2026-12-01")
			return
		}
		startsOn = d
	}
	if startsOn.Before(orgmodules.Day(now)) {
		writeErr(w, r, http.StatusUnprocessableEntity, "org_module.starts_on_in_past",
			"starts_on must be today or later: a month already charged is never rewritten")
		return
	}
	var endsOn *time.Time
	if body.EndsOn != nil && strings.TrimSpace(*body.EndsOn) != "" {
		d, ok := parseDay(*body.EndsOn)
		if !ok {
			writeErr(w, r, http.StatusUnprocessableEntity, "org_module.invalid", "ends_on must be a date, for example 2027-11-30")
			return
		}
		endsOn = &d
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.queries.WithTx(tx)
	if !h.orgExists(w, r, q, orgID) {
		return
	}
	price, currency, err := h.priceInput(ctx, q, orgID, body.MonthlyPriceMinor, body.Currency)
	if err != nil {
		writeErr(w, r, http.StatusUnprocessableEntity, "org_module.invalid",
			"the organization's billing currency is not known: send the currency with monthly_price_minor")
		return
	}
	created, err := orgmodules.Connect(ctx, tx, orgmodules.ConnectInput{
		OrgID: orgID, Module: orgmodules.ModuleShop, StartsOn: startsOn, EndsOn: endsOn,
		MonthlyPriceMinor: price, Currency: currency,
		Reason: periodReason(body.Reason, reason), ActorID: actorUUID(r),
	})
	if err != nil {
		h.moduleErr(w, r, "connect", err)
		return
	}
	contiguous, err := h.continuesPeriod(ctx, q, orgID, startsOn)
	if err != nil {
		h.fail(w, r, "check previous period", err)
		return
	}
	if !contiguous {
		// A fresh connection or a reconnection after a gap: the storefront
		// stays closed until the organization opens it itself through the
		// normal opening checks (spec 61 section 4.2).
		if _, err := q.SetShopSettingsEnabled(ctx, orgID, false); err != nil {
			h.fail(w, r, "close storefront", err)
			return
		}
	}
	if err := h.auditTx(r, tx, AuditConnect, created.ID, reason, map[string]any{
		"org_id": orgID.String(), "module": created.Module,
		"starts_on": created.StartsOn.Format(dateLayout), "ends_on": dateJSON(created.EndsOn),
		"monthly_price_minor": created.MonthlyPriceMinor, "currency": created.Currency,
		"period_reason": created.Reason,
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, periodJSON(created, orgmodules.Day(now), true))
}

// continuesPeriod reports whether a period of the shop module covers the day
// before day, i.e. a period starting on day continues an existing connection. It
// runs after Connect, which holds the (organization, module) lock, and the
// period just created starts on day, so it never matches itself.
func (h *Handler) continuesPeriod(ctx context.Context, q *gen.Queries, orgID uuid.UUID, day time.Time) (bool, error) {
	periods, err := orgmodules.List(ctx, q, orgID)
	if err != nil {
		return false, err
	}
	prev := day.AddDate(0, 0, -1)
	for _, p := range periods {
		if p.Module == orgmodules.ModuleShop && p.Covers(prev) {
			return true, nil
		}
	}
	return false, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/org-modules/{id}/end
// ─────────────────────────────────────────────────────────────────────────────

type endBody struct {
	EndsOn string  `json:"ends_on"`
	Reason *string `json:"reason"`
}

// HandleEnd sets the last day of a period (never later than its current
// end). From the next UTC day the storefront is closed and the shop falls to
// orders_only for good once the organization has a paid or an open order.
func (h *Handler) HandleEnd(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, true) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var body endBody
	if !decodeBody(w, r, &body) {
		return
	}
	endsOn, ok := parseDay(body.EndsOn)
	if !ok {
		writeErr(w, r, http.StatusUnprocessableEntity, "org_module.invalid", "ends_on must be a date, for example 2026-12-31")
		return
	}
	if endsOn.Before(orgmodules.Day(h.now()).AddDate(0, 0, -1)) {
		writeErr(w, r, http.StatusUnprocessableEntity, "org_module.ends_on_in_past",
			"ends_on must be yesterday or later: a month already charged is never rewritten")
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	before, err := h.queries.WithTx(tx).GetOrgModulePeriod(ctx, id)
	if err != nil && !isNoRows(err) {
		h.fail(w, r, "get period", err)
		return
	}
	ended, err := orgmodules.End(ctx, tx, id, endsOn, periodReason(body.Reason, reason), actorUUID(r))
	if err != nil {
		h.moduleErr(w, r, "end", err)
		return
	}
	if err := h.auditTx(r, tx, AuditEnd, ended.ID, reason, map[string]any{
		"org_id": ended.OrgID.String(), "module": ended.Module,
		"old_ends_on": dateJSON(before.EndsOn), "ends_on": dateJSON(ended.EndsOn),
		"end_reason": periodReason(body.Reason, reason),
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, periodJSON(ended, orgmodules.Day(h.now()), true))
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/org-modules/{id}/reprice
// ─────────────────────────────────────────────────────────────────────────────

type repriceBody struct {
	From              string  `json:"from"`
	MonthlyPriceMinor *int64  `json:"monthly_price_minor"`
	Currency          *string `json:"currency"`
	Reason            *string `json:"reason"`
}

// HandleReprice changes the price from the day `from` on: the period ends on
// from-1 and a new one opens from `from` with the new price (none = the price
// list) and the old end. It answers the NEW period.
func (h *Handler) HandleReprice(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, true) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var body repriceBody
	if !decodeBody(w, r, &body) {
		return
	}
	from, ok := parseDay(body.From)
	if !ok {
		writeErr(w, r, http.StatusUnprocessableEntity, "org_module.invalid", "from must be a date, for example 2026-12-01")
		return
	}
	if from.Before(orgmodules.Day(h.now())) {
		writeErr(w, r, http.StatusUnprocessableEntity, "org_module.from_in_past",
			"from must be today or later: a month already charged is never rewritten")
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
	old, err := q.GetOrgModulePeriod(ctx, id)
	if isNoRows(err) {
		h.moduleErr(w, r, "reprice", orgmodules.ErrNotFound)
		return
	}
	if err != nil {
		h.fail(w, r, "get period", err)
		return
	}
	price, currency, err := h.priceInput(ctx, q, old.OrgID, body.MonthlyPriceMinor, body.Currency)
	if err != nil {
		writeErr(w, r, http.StatusUnprocessableEntity, "org_module.invalid",
			"the organization's billing currency is not known: send the currency with monthly_price_minor")
		return
	}
	created, err := orgmodules.Reprice(ctx, tx, id, from, price, currency, periodReason(body.Reason, reason), actorUUID(r))
	if err != nil {
		h.moduleErr(w, r, "reprice", err)
		return
	}
	if err := h.auditTx(r, tx, AuditReprice, created.ID, reason, map[string]any{
		"org_id": created.OrgID.String(), "module": created.Module, "old_period_id": id.String(),
		"from":                    created.StartsOn.Format(dateLayout),
		"old_monthly_price_minor": old.MonthlyPriceMinor, "old_currency": old.Currency,
		"monthly_price_minor": created.MonthlyPriceMinor, "currency": created.Currency,
		"period_reason": created.Reason,
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, periodJSON(created, orgmodules.Day(h.now()), true))
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/billing/modules (organizer, read-only)
// ─────────────────────────────────────────────────────────────────────────────

// HandleOrgModules lists the organization's module periods without the
// operator-only fields, each with the monthly price that applies to it (its
// individual price, else the price list's), and the price that applies today.
func (h *Handler) HandleOrgModules(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	ctx := r.Context()
	periods, err := orgmodules.List(ctx, h.queries, orgID)
	if err != nil {
		h.fail(w, r, "list periods", err)
		return
	}
	now := h.now()
	day := orgmodules.Day(now)
	listMonthly, listCurrency := h.listPrice(ctx, orgID, now)
	items := make([]map[string]any, 0, len(periods))
	for _, p := range periods {
		item := periodJSON(p, day, false)
		monthly, cur := listMonthly, any(nil)
		if listCurrency != "" {
			cur = listCurrency
		}
		if p.MonthlyPriceMinor != nil {
			v := *p.MonthlyPriceMinor
			monthly, cur = &v, p.Currency
		}
		item["effective_monthly_minor"] = monthly
		item["effective_currency"] = cur
		items = append(items, item)
	}
	price := h.effective(ctx, orgID, periods, now)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"items":                    items,
		"price_list_monthly_minor": listMonthly,
		"effective_monthly_minor":  price.MonthlyMinor,
		"currency":                 price.Currency,
		"price_source":             price.source(),
	})
}
