package hbilling

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

const accountKindTools = "tools"

type createTermsBody struct {
	EffectiveFrom    *time.Time `json:"effective_from"`
	PriceListCode    string     `json:"price_list_code"`
	PlanKind         string     `json:"plan_kind"`
	BillingCycle     string     `json:"billing_cycle"`
	ShortfallPolicy  string     `json:"shortfall_policy"`
	InvoiceDelayDays *int32     `json:"invoice_delay_days"`
	DueDays          *int32     `json:"due_days"`
	// FeeMode: organizer_pays (default) or buyer_pays.
	FeeMode string `json:"fee_mode"`
	// ComplimentaryBilling: free or standard (spec 46, owner decision R-7).
	// Absent keeps the value of the terms row in force at effective_from, so
	// a new row written for another reason never flips it back.
	ComplimentaryBilling string `json:"complimentary_billing"`
}

func termsJSON(t gen.BillingAccountTermsRow) map[string]any {
	return map[string]any{
		"id":                    t.ID.String(),
		"effective_from":        t.EffectiveFrom.UTC().Format(time.RFC3339Nano),
		"price_list_code":       t.PriceListCode,
		"plan_kind":             t.PlanKind,
		"billing_cycle":         t.BillingCycle,
		"shortfall_policy":      t.ShortfallPolicy,
		"issuer_id":             t.IssuerID.String(),
		"invoice_delay_days":    t.InvoiceDelayDays,
		"due_days":              t.DueDays,
		"fee_mode":              t.FeeMode,
		"complimentary_billing": t.ComplimentaryBilling,
		"locked":                t.Locked,
		"created_by":            t.CreatedBy,
		"reason":                t.Reason,
		"created_at":            t.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/billing/accounts
// ─────────────────────────────────────────────────────────────────────────────

// HandleListAccounts lists billing accounts with their terms and the three
// flags the operator sets or reads: venue (plan kind of the terms in force),
// tools (account kind) and existing client (the organization has an
// individual rate; the organization-level legacy date column of spec 41
// section 5.5 does not exist yet).
func (h *AdminHandler) HandleListAccounts(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()
	accounts, err := h.queries.ListBillingAccountsAdmin(ctx, orgID)
	if err != nil {
		h.fail(w, r, "list accounts", err)
		return
	}
	ids := make([]uuid.UUID, len(accounts))
	for i, a := range accounts {
		ids[i] = a.ID
	}
	terms, err := h.queries.ListBillingAccountTermsForAccounts(ctx, ids)
	if err != nil {
		h.fail(w, r, "list terms", err)
		return
	}
	rates, err := h.queries.ListOrgRateOverridesAdmin(ctx, orgID, "")
	if err != nil {
		h.fail(w, r, "list rates", err)
		return
	}
	hasRate := map[uuid.UUID]bool{}
	for _, x := range rates {
		hasRate[x.OrgID] = true
	}
	byAccount := map[uuid.UUID][]gen.BillingAccountTermsRow{}
	for _, t := range terms {
		byAccount[t.AccountID] = append(byAccount[t.AccountID], t)
	}
	now := h.now()
	items := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		history := byAccount[a.ID]
		var current *gen.BillingAccountTermsRow
		for i := range history {
			if !history[i].EffectiveFrom.After(now) {
				current = &history[i]
			}
		}
		tj := make([]map[string]any, 0, len(history))
		for _, t := range history {
			tj = append(tj, termsJSON(t))
		}
		item := map[string]any{
			"id":           a.ID.String(),
			"org_id":       a.OrgID.String(),
			"org_name":     a.OrgName,
			"currency":     a.Currency,
			"issuer_id":    a.IssuerID.String(),
			"account_kind": a.AccountKind,
			"terms":        tj,
			"flags": map[string]any{
				"venue":           current != nil && current.PlanKind == billing.PlanVenue,
				"tools":           a.AccountKind == accountKindTools,
				"existing_client": hasRate[a.OrgID],
			},
			"current_terms_id": nil,
		}
		if current != nil {
			item["current_terms_id"] = current.ID.String()
		}
		items = append(items, item)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/billing/accounts/{account_id}/terms
// ─────────────────────────────────────────────────────────────────────────────

var (
	validPlanKinds       = map[string]bool{billing.PlanStandard: true, billing.PlanVenue: true}
	validBillingCycles   = map[string]bool{"prepaid": true, "per_event": true, "monthly": true}
	validShortfallPolicy = map[string]bool{"invoice_postpaid": true, "block_publishing": true}
)

// HandleCreateAccountTerms inserts a new dated terms row for an account: the
// price list, plan kind (the "venue" flag), cycle and shortfall policy. It
// starts in the future after every earlier terms row, and the price list must
// have a version in force at that moment that carries the rule the plan kind
// is rated by.
func (h *AdminHandler) HandleCreateAccountTerms(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	accountID, ok := httputil.UUIDPathParam(w, r, "account_id")
	if !ok {
		return
	}
	var body createTermsBody
	if !decodeBody(w, r, &body) {
		return
	}
	now := h.now()
	switch {
	case body.EffectiveFrom == nil:
		unprocessable(w, r, "billing.invalid_terms", "effective_from is required")
		return
	case !body.EffectiveFrom.After(now):
		unprocessable(w, r, "billing.effective_from_not_future",
			"effective_from must be in the future: new terms never start in the past, so they cannot change a sale that already happened")
		return
	case !priceListCode.MatchString(body.PriceListCode):
		unprocessable(w, r, "billing.invalid_terms", "price_list_code is the code of a price list, for example the one shown on the price lists tab")
		return
	case !validPlanKinds[body.PlanKind]:
		unprocessable(w, r, "billing.invalid_terms", "plan_kind must be standard or venue")
		return
	case !validBillingCycles[body.BillingCycle]:
		unprocessable(w, r, "billing.invalid_terms", "billing_cycle must be prepaid, per_event or monthly")
		return
	case body.FeeMode != "" && body.FeeMode != "organizer_pays" && body.FeeMode != "buyer_pays":
		unprocessable(w, r, "billing.invalid_terms", "fee_mode must be organizer_pays or buyer_pays")
		return
	case body.ComplimentaryBilling != "" && body.ComplimentaryBilling != billing.ComplimentaryBillingFree &&
		body.ComplimentaryBilling != billing.ComplimentaryBillingStandard:
		unprocessable(w, r, "billing.invalid_terms", "complimentary_billing must be free or standard")
		return
	case !validShortfallPolicy[body.ShortfallPolicy]:
		unprocessable(w, r, "billing.invalid_terms", "shortfall_policy must be invoice_postpaid or block_publishing")
		return
	case body.InvoiceDelayDays != nil && *body.InvoiceDelayDays < 0, body.DueDays != nil && *body.DueDays < 0:
		unprocessable(w, r, "billing.invalid_terms", "invoice_delay_days and due_days cannot be negative")
		return
	}
	from := body.EffectiveFrom.UTC()

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.queries.WithTx(tx)
	acc, err := q.GetBillingAccountByID(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r, "billing.account_not_found", "this billing account does not exist")
		return
	}
	if err != nil {
		h.fail(w, r, "get account", err)
		return
	}
	if acc.AccountKind == accountKindTools && body.BillingCycle != "prepaid" {
		unprocessable(w, r, "billing.invalid_terms", "a tools account is prepaid only")
		return
	}
	if err := q.LockBillingAccountTermsSeries(ctx, acc.ID); err != nil {
		h.fail(w, r, "lock terms", err)
		return
	}
	if !from.After(h.now()) { // judged again after the lock wait
		unprocessable(w, r, "billing.effective_from_not_future", "effective_from "+notFutureMessage)
		return
	}
	// The price list's series lock: a concurrent revoke of the version found
	// below either finished (and this read no longer sees it) or waits for
	// this transaction (and then sees these terms). Same order as the revoke.
	if err := q.LockPriceListSeries(ctx, body.PriceListCode, acc.Currency); err != nil {
		h.fail(w, r, "lock price list series", err)
		return
	}
	latest, err := q.GetBillingAccountTermsLatestFrom(ctx, acc.ID)
	if err != nil {
		h.fail(w, r, "latest terms", err)
		return
	}
	if latest != nil && !from.After(*latest) {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"billing.terms_not_after_latest",
			"the new terms must start after this account's latest terms, which start "+latest.UTC().Format(time.RFC3339),
			r, map[string]any{"latest_effective_from": latest.UTC().Format(time.RFC3339Nano)}))
		return
	}
	complimentary := body.ComplimentaryBilling
	if complimentary == "" {
		// Keep the invitation billing of the row in force when the new one
		// starts (the account's latest row, checked above to start earlier).
		prev, perr := q.GetBillingAccountTermsAt(ctx, acc.ID, from)
		switch {
		case perr == nil:
			complimentary = prev.ComplimentaryBilling
		case !errors.Is(perr, pgx.ErrNoRows):
			h.fail(w, r, "terms in force", perr)
			return
		}
	}
	pl, err := q.GetPriceListVersionAt(ctx, body.PriceListCode, acc.Currency, from)
	if errors.Is(err, pgx.ErrNoRows) {
		unprocessable(w, r, "billing.price_list_not_found",
			"no version of the price list "+body.PriceListCode+" in "+acc.Currency+" is in force at that moment")
		return
	}
	if err != nil {
		h.fail(w, r, "price list at date", err)
		return
	}
	rules, err := q.ListPriceListRules(ctx, pl.ID)
	if err != nil {
		h.fail(w, r, "rules", err)
		return
	}
	need := billing.RuleFixedPerTicket
	line := "standard"
	if body.PlanKind == billing.PlanVenue {
		need = billing.RulePercentOfPriceWithCap
		line = "venue"
	}
	found := false
	for _, x := range rules {
		if x.RuleType == string(need) && x.Line == line && x.EventKind == string(billing.KindTicketSold) {
			found = true
		}
	}
	if !found {
		unprocessable(w, r, "billing.price_list_lacks_rule",
			"version "+strconv.Itoa(int(pl.Version))+" of the price list "+pl.Code+" has no "+string(need)+" rule on the "+line+
				" line, so a "+body.PlanKind+" account could not be rated")
		return
	}
	created, err := q.InsertBillingAccountTerms(ctx, gen.InsertBillingAccountTermsParams{
		AccountID: acc.ID, EffectiveFrom: from, PriceListCode: body.PriceListCode, PlanKind: body.PlanKind,
		BillingCycle: body.BillingCycle, ShortfallPolicy: body.ShortfallPolicy, IssuerID: acc.IssuerID,
		InvoiceDelayDays: body.InvoiceDelayDays, DueDays: body.DueDays,
		CreatedBy: ptrString(actorID(r)), Reason: &reason, FeeMode: body.FeeMode,
		ComplimentaryBilling: complimentary,
	})
	if err != nil {
		h.fail(w, r, "insert terms", err)
		return
	}
	if err := h.auditTx(r, tx, AuditAccountTermsCreate, "billing_account", acc.ID, reason, map[string]any{
		"org_id": acc.OrgID.String(), "currency": acc.Currency, "terms_id": created.ID.String(),
		"effective_from":  created.EffectiveFrom.UTC().Format(time.RFC3339Nano),
		"price_list_code": created.PriceListCode, "plan_kind": created.PlanKind,
		"billing_cycle": created.BillingCycle, "shortfall_policy": created.ShortfallPolicy,
		"fee_mode": created.FeeMode, "complimentary_billing": created.ComplimentaryBilling,
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, termsJSON(created))
}
