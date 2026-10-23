package hbilling

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Status of a price-list version at one instant.
const (
	StatusRevoked    = "revoked"
	StatusScheduled  = "scheduled"
	StatusInForce    = "in_force"
	StatusSuperseded = "superseded"
)

var (
	priceListCode = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)
	currencyCode  = regexp.MustCompile(`^[A-Z]{3}$`)
)

// ─────────────────────────────────────────────────────────────────────────────
// Request and response shapes
// ─────────────────────────────────────────────────────────────────────────────

type ruleBody struct {
	RuleType     string          `json:"rule_type"`
	Line         string          `json:"line"`
	EventKind    string          `json:"event_kind"`
	SaleCurrency string          `json:"sale_currency"`
	Params       json.RawMessage `json:"params"`
}

type packBody struct {
	Tier                 *int16 `json:"tier"`
	FaceMinor            *int64 `json:"face_minor"`
	UnseatedRateMinor    *int64 `json:"unseated_rate_minor"`
	SeatedSurchargeMinor *int64 `json:"seated_surcharge_minor"`
	Active               *bool  `json:"active"`
}

type createPriceListBody struct {
	Code                   string     `json:"code"`
	Currency               string     `json:"currency"`
	EffectiveFrom          *time.Time `json:"effective_from"`
	Locked                 bool       `json:"locked"`
	FreeTicketsAnnualLimit *int32     `json:"free_tickets_annual_limit"`
	FloorPerTicketMinor    *int64     `json:"floor_per_ticket_minor"`
	BonusShareCapBP        *int32     `json:"bonus_share_cap_bp"`
	BonusCoversSurcharge   bool       `json:"bonus_covers_surcharge"`
	Rules                  []ruleBody `json:"rules"`
	Packs                  []packBody `json:"packs"`
}

func priceListJSON(p gen.PriceListRow, status string) map[string]any {
	return map[string]any{
		"id":                        p.ID.String(),
		"code":                      p.Code,
		"version":                   p.Version,
		"currency":                  p.Currency,
		"effective_from":            p.EffectiveFrom.UTC().Format(time.RFC3339Nano),
		"revoked_at":                timeJSON(p.RevokedAt),
		"locked":                    p.Locked,
		"free_tickets_annual_limit": p.FreeTicketsAnnualLimit,
		"floor_per_ticket_minor":    p.FloorPerTicketMinor,
		"bonus_share_cap_bp":        p.BonusShareCapBP,
		"bonus_covers_surcharge":    p.BonusCoversSurcharge,
		"created_by":                p.CreatedBy,
		"reason":                    p.Reason,
		"created_at":                p.CreatedAt.UTC().Format(time.RFC3339Nano),
		"status":                    status,
	}
}

func ruleJSON(r gen.PriceListRuleRow) map[string]any {
	params := json.RawMessage(r.Params)
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	return map[string]any{
		"id":            r.ID.String(),
		"rule_type":     r.RuleType,
		"line":          r.Line,
		"event_kind":    r.EventKind,
		"sale_currency": r.SaleCurrency,
		"params":        params,
		"sort_order":    r.SortOrder,
	}
}

func packJSON(p gen.PriceListPackRow) map[string]any {
	return map[string]any{
		"id":                     p.ID.String(),
		"tier":                   p.Tier,
		"currency":               p.Currency,
		"face_minor":             p.FaceMinor,
		"unseated_rate_minor":    p.UnseatedRateMinor,
		"seated_surcharge_minor": p.SeatedSurchargeMinor,
		"active":                 p.Active,
		"sort_order":             p.SortOrder,
	}
}

// versionStatuses labels every version of the list with its status at now.
// Within a (code, currency) series the version in force is the unrevoked one
// with the latest effective_from not after now (highest version on a tie),
// the same choice GetPriceListVersionAt makes.
func versionStatuses(rows []gen.PriceListRow, now time.Time) map[string]string {
	type series struct{ code, currency string }
	best := map[series]gen.PriceListRow{}
	for _, p := range rows {
		if p.RevokedAt != nil || p.EffectiveFrom.After(now) {
			continue
		}
		k := series{p.Code, p.Currency}
		cur, ok := best[k]
		if !ok || p.EffectiveFrom.After(cur.EffectiveFrom) ||
			(p.EffectiveFrom.Equal(cur.EffectiveFrom) && p.Version > cur.Version) {
			best[k] = p
		}
	}
	out := make(map[string]string, len(rows))
	for _, p := range rows {
		switch {
		case p.RevokedAt != nil:
			out[p.ID.String()] = StatusRevoked
		case p.EffectiveFrom.After(now):
			out[p.ID.String()] = StatusScheduled
		case best[series{p.Code, p.Currency}].ID == p.ID:
			out[p.ID.String()] = StatusInForce
		default:
			out[p.ID.String()] = StatusSuperseded
		}
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/billing/price-lists
// ─────────────────────────────────────────────────────────────────────────────

// HandleListPriceLists lists every version of every price list.
func (h *AdminHandler) HandleListPriceLists(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	currency := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("currency")))
	rows, err := h.queries.ListPriceListVersions(r.Context(), code, currency)
	if err != nil {
		h.fail(w, r, "list price lists", err)
		return
	}
	plain := make([]gen.PriceListRow, len(rows))
	for i, p := range rows {
		plain[i] = p.PriceListRow
	}
	statuses := versionStatuses(plain, h.now())
	items := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		it := priceListJSON(p.PriceListRow, statuses[p.ID.String()])
		it["rules_count"] = p.RulesCount
		it["packs_count"] = p.PacksCount
		items = append(items, it)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/billing/price-lists/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetPriceList returns one version with its rules and packs.
func (h *AdminHandler) HandleGetPriceList(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	p, err := h.queries.GetPriceListByID(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r, "billing.price_list_not_found", "this price list version does not exist")
		return
	}
	if err != nil {
		h.fail(w, r, "get price list", err)
		return
	}
	body, err := h.priceListDetail(r, p)
	if err != nil {
		h.fail(w, r, "price list detail", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, body)
}

// priceListDetail builds the full answer for one version: rows, rules, packs
// and the status among its series.
func (h *AdminHandler) priceListDetail(r *http.Request, p gen.PriceListRow) (map[string]any, error) {
	return h.priceListDetailOn(r, h.queries, p)
}

func (h *AdminHandler) priceListDetailOn(r *http.Request, q *gen.Queries, p gen.PriceListRow) (map[string]any, error) {
	series, err := q.ListPriceListVersions(r.Context(), p.Code, p.Currency)
	if err != nil {
		return nil, err
	}
	plain := make([]gen.PriceListRow, len(series))
	for i, s := range series {
		plain[i] = s.PriceListRow
	}
	status := versionStatuses(plain, h.now())[p.ID.String()]
	rules, err := q.ListPriceListRules(r.Context(), p.ID)
	if err != nil {
		return nil, err
	}
	packs, err := q.ListPriceListPacks(r.Context(), p.ID)
	if err != nil {
		return nil, err
	}
	out := priceListJSON(p, status)
	rj := make([]map[string]any, 0, len(rules))
	for _, x := range rules {
		rj = append(rj, ruleJSON(x))
	}
	pj := make([]map[string]any, 0, len(packs))
	for _, x := range packs {
		pj = append(pj, packJSON(x))
	}
	out["rules"] = rj
	out["packs"] = pj
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/billing/price-lists
// ─────────────────────────────────────────────────────────────────────────────

// HandleCreatePriceList inserts a NEW version of a price list. The version
// number is the next one of its (code, currency) series, the start must be in
// the future and after the start of every earlier version that was not
// revoked, and every rule must be one the rating engine reads.
func (h *AdminHandler) HandleCreatePriceList(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	var body createPriceListBody
	if !decodeBody(w, r, &body) {
		return
	}
	now := h.now()
	if msg := validateCreatePriceList(body, now); msg != "" {
		unprocessable(w, r, "billing.invalid_price_list", msg)
		return
	}
	ruleInputs := make([]billing.RuleInput, len(body.Rules))
	for i, x := range body.Rules {
		ruleInputs[i] = billing.RuleInput{RuleType: x.RuleType, Line: x.Line, EventKind: x.EventKind,
			SaleCurrency: x.SaleCurrency, Params: x.Params}
	}
	if err := billing.CheckRuleSet(ruleInputs, body.Currency); err != nil {
		unprocessable(w, r, "billing.invalid_price_list", err.Error())
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
	if err := q.LockPriceListSeries(ctx, body.Code, body.Currency); err != nil {
		h.fail(w, r, "lock series", err)
		return
	}
	// The wait for the lock may have taken long: judge "in the future" again
	// on a clock read now, not on the one read before the transaction.
	if !from.After(h.now()) {
		unprocessable(w, r, "billing.effective_from_not_future", "effective_from "+notFutureMessage)
		return
	}
	state, err := q.GetPriceListSeriesState(ctx, body.Code, body.Currency)
	if err != nil {
		h.fail(w, r, "series state", err)
		return
	}
	if state.LatestEffectiveFrom != nil && !from.After(*state.LatestEffectiveFrom) {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"billing.effective_from_not_after_latest",
			"the new version must start after the latest version of this price list, which starts "+
				state.LatestEffectiveFrom.UTC().Format(time.RFC3339)+"; revoke that version first or pick a later start",
			r, map[string]any{"latest_effective_from": state.LatestEffectiveFrom.UTC().Format(time.RFC3339Nano)}))
		return
	}
	// The rules the previous version carried and this one drops are reported
	// back, not refused: removing a rule can be deliberate, forgetting one
	// breaks rating of the accounts that rely on it.
	warnings, err := h.droppedRuleWarnings(r, q, body, ruleInputs)
	if err != nil {
		h.fail(w, r, "compare with previous version", err)
		return
	}

	created, err := q.InsertPriceListVersion(ctx, gen.InsertPriceListVersionParams{
		Code: body.Code, Version: state.NextVersion, Currency: body.Currency, EffectiveFrom: from,
		Locked: body.Locked, FreeTicketsAnnualLimit: body.FreeTicketsAnnualLimit,
		FloorPerTicketMinor: body.FloorPerTicketMinor, BonusShareCapBP: *body.BonusShareCapBP,
		BonusCoversSurcharge: body.BonusCoversSurcharge, CreatedBy: ptrString(actorID(r)), Reason: &reason,
	})
	if err != nil {
		h.fail(w, r, "insert price list", err)
		return
	}
	for i, x := range body.Rules {
		var sale *string
		if x.SaleCurrency != "" {
			sc := x.SaleCurrency
			sale = &sc
		}
		if _, err := q.InsertPriceListRule(ctx, created.ID, x.RuleType, x.Line, x.EventKind, sale,
			[]byte(x.Params), int32((i+1)*10)); err != nil { //nolint:gosec // bounded by the rule count cap
			h.fail(w, r, "insert rule", err)
			return
		}
	}
	for _, x := range body.Packs {
		active := true
		if x.Active != nil {
			active = *x.Active
		}
		if _, err := q.InsertPriceListPack(ctx, created.ID, created.Currency, *x.Tier, *x.FaceMinor,
			*x.UnseatedRateMinor, *x.SeatedSurchargeMinor, active, int32(*x.Tier)); err != nil {
			h.fail(w, r, "insert pack", err)
			return
		}
	}
	if err := h.auditTx(r, tx, AuditPriceListCreate, "price_list", created.ID, reason, map[string]any{
		"code": created.Code, "version": created.Version, "currency": created.Currency,
		"effective_from": created.EffectiveFrom.UTC().Format(time.RFC3339Nano),
		"rules":          len(body.Rules), "packs": len(body.Packs), "locked": created.Locked,
		"bonus_share_cap_bp": created.BonusShareCapBP, "dropped_rules": len(warnings),
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	out, err := h.priceListDetailOn(r, q, created)
	if err != nil {
		h.fail(w, r, "detail", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	out["warnings"] = warnings
	httputil.WriteJSON(w, http.StatusCreated, out)
}

const (
	maxRulesPerList = 50
	maxPacksPerList = 30
)

// validateCreatePriceList checks everything that needs no database. It
// returns a message for the operator, "" when the body is fine.
func validateCreatePriceList(b createPriceListBody, now time.Time) string {
	switch {
	case !priceListCode.MatchString(b.Code):
		return "code must be 2-64 characters: lower-case letters, digits, dot, dash or underscore, starting with a letter or digit"
	case !currencyCode.MatchString(b.Currency):
		return "currency must be three capital letters, for example EUR"
	case b.EffectiveFrom == nil:
		return "effective_from is required"
	case !b.EffectiveFrom.After(now):
		return "effective_from must be in the future: a version never starts in the past, so it cannot reach an event that already happened"
	case b.BonusShareCapBP == nil:
		return "bonus_share_cap_bp is required (basis points, 0-10000, a multiple of 10)"
	case *b.BonusShareCapBP < 0 || *b.BonusShareCapBP > 10000 || *b.BonusShareCapBP%10 != 0:
		return "bonus_share_cap_bp must be between 0 and 10000 and a multiple of 10"
	case b.FreeTicketsAnnualLimit != nil && *b.FreeTicketsAnnualLimit < 0:
		return "free_tickets_annual_limit cannot be negative"
	case b.FloorPerTicketMinor != nil && *b.FloorPerTicketMinor < 0:
		return "floor_per_ticket_minor cannot be negative"
	case len(b.Rules) == 0:
		return "a version needs at least one rule"
	case len(b.Rules) > maxRulesPerList:
		return fmt.Sprintf("a version holds at most %d rules", maxRulesPerList)
	case len(b.Packs) > maxPacksPerList:
		return fmt.Sprintf("a version holds at most %d packs", maxPacksPerList)
	}
	tiers := map[int16]bool{}
	for i, p := range b.Packs {
		n := i + 1
		switch {
		case p.Tier == nil || p.FaceMinor == nil || p.UnseatedRateMinor == nil || p.SeatedSurchargeMinor == nil:
			return fmt.Sprintf("pack %d needs tier, face_minor, unseated_rate_minor and seated_surcharge_minor", n)
		case *p.Tier <= 0:
			return fmt.Sprintf("pack %d: tier must be 1 or more", n)
		case *p.FaceMinor <= 0:
			return fmt.Sprintf("pack %d: face_minor must be more than 0", n)
		case *p.UnseatedRateMinor < 0 || *p.SeatedSurchargeMinor < 0:
			return fmt.Sprintf("pack %d: rates cannot be negative", n)
		case tiers[*p.Tier]:
			return fmt.Sprintf("pack %d: tier %d is used twice", n, *p.Tier)
		}
		tiers[*p.Tier] = true
	}
	return ""
}

type ruleKey struct{ typ, line, kind, sale string }

// droppedRuleWarnings lists the rule slots the latest unrevoked version of the
// series has and the new one lacks.
func (h *AdminHandler) droppedRuleWarnings(r *http.Request, q *gen.Queries, b createPriceListBody, inputs []billing.RuleInput) ([]string, error) {
	versions, err := q.ListPriceListVersions(r.Context(), b.Code, b.Currency)
	if err != nil {
		return nil, err
	}
	var prev *gen.PriceListRow
	for i := range versions {
		v := versions[i].PriceListRow
		if v.RevokedAt != nil {
			continue
		}
		if prev == nil || v.EffectiveFrom.After(prev.EffectiveFrom) {
			cp := v
			prev = &cp
		}
	}
	warnings := []string{}
	if prev == nil {
		return warnings, nil
	}
	have := map[ruleKey]bool{}
	for _, in := range inputs {
		have[ruleKey{in.RuleType, in.Line, in.EventKind, in.SaleCurrency}] = true
	}
	old, err := q.ListPriceListRules(r.Context(), prev.ID)
	if err != nil {
		return nil, err
	}
	for _, o := range old {
		sale := ""
		if o.SaleCurrency != nil {
			sale = *o.SaleCurrency
		}
		if !have[ruleKey{o.RuleType, o.Line, o.EventKind, sale}] {
			warnings = append(warnings, fmt.Sprintf("version %d has the rule %s (line %s, %s) and this version does not",
				prev.Version, o.RuleType, o.Line, o.EventKind))
		}
	}
	sort.Strings(warnings)
	return warnings, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/admin/billing/price-lists/{id}/revoke
// ─────────────────────────────────────────────────────────────────────────────

// HandleRevokePriceList revokes a version that has not started. After its
// effective_from the answer is 409: a version in force or in the past is
// history that charges already point at.
func (h *AdminHandler) HandleRevokePriceList(w http.ResponseWriter, r *http.Request) {
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
	// Lock order is the one HandleCreatePriceList and HandleCreateAccountTerms
	// use: the series advisory lock first, the version row second. The series
	// is learned from an unlocked read, and the version is read AGAIN under
	// the locks, so the coverage check below sees what concurrent revokes and
	// terms have committed.
	probe, err := q.GetPriceListByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r, "billing.price_list_not_found", "this price list version does not exist")
		return
	}
	if err != nil {
		h.fail(w, r, "read price list", err)
		return
	}
	if err := q.LockPriceListSeries(ctx, probe.Code, probe.Currency); err != nil {
		h.fail(w, r, "lock series", err)
		return
	}
	cur, err := q.LockPriceListByID(ctx, id)
	if err != nil { // the row cannot disappear: nothing deletes a version
		h.fail(w, r, "lock price list", err)
		return
	}
	now := h.now() // after the locks
	switch {
	case cur.RevokedAt != nil:
		conflict(w, r, "billing.price_list_already_revoked", "this version is already revoked")
		return
	case !cur.EffectiveFrom.After(now):
		conflict(w, r, "billing.price_list_in_force",
			"this version has already started on "+cur.EffectiveFrom.UTC().Format(time.RFC3339)+
				" and can no longer be revoked: publish a new version instead")
		return
	}
	uncovered, err := q.PriceListRevokeLeavesAccountsUncovered(ctx, id)
	if err != nil {
		h.fail(w, r, "check coverage", err)
		return
	}
	if uncovered {
		conflict(w, r, "billing.price_list_in_use",
			"accounts use this price list and this is its first version: revoking it would leave them without prices")
		return
	}
	revoked, err := q.RevokePriceListVersion(ctx, id, now)
	if errors.Is(err, pgx.ErrNoRows) {
		conflict(w, r, "billing.price_list_in_force", "this version has just started and can no longer be revoked")
		return
	}
	if err != nil {
		h.fail(w, r, "revoke", err)
		return
	}
	if err := h.auditTx(r, tx, AuditPriceListRevoke, "price_list", revoked.ID, reason, map[string]any{
		"code": revoked.Code, "version": revoked.Version, "currency": revoked.Currency,
		"effective_from": revoked.EffectiveFrom.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	out, err := h.priceListDetailOn(r, q, revoked)
	if err != nil {
		h.fail(w, r, "detail", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}
