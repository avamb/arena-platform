// smart_price.go serves the "smart price" presets (spec
// 08_architecture/51_4_2_smart_price_ru.md):
//
//	GET  .../sessions/{session_id}/smart-price                 rules of the session, with the price now
//	POST .../sessions/{session_id}/smart-price/preview         what a preset would write (writes nothing)
//	POST .../sessions/{session_id}/smart-price                 enable a preset
//	POST .../sessions/{session_id}/smart-price/{rule_id}/stop  freeze the price where it is
//
// A preset is NOT a price engine. Every write reads the session back as an
// event-bundle (the same code as GET .../event-bundle), changes the one
// category the preset applies to — a chain of steps with sell limits, or price
// windows — and posts the bundle through the import's own preparation and
// executor. The chain sweep and priceresolve then decide every price exactly
// as for a chain or a schedule typed by hand.
package himports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/pricing"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/pricing/smartprice"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/txretry"
)

// SmartPriceRequest is the body of the preview and enable routes.
type SmartPriceRequest struct {
	// TierIDs are the categories the preset applies to: exactly one for
	// by_sales, one or more for by_days and last_days.
	TierIDs []uuid.UUID `json:"tier_ids"`
	smartprice.Params
}

// SmartPriceStep is one row of a preset's table: a chain step (places) or a
// price window (from).
type SmartPriceStep struct {
	Places int32      `json:"places,omitempty"`
	From   *time.Time `json:"from,omitempty"`
	Price  int64      `json:"price"`
}

// SmartPricePreviewCategory is what a preset would write for one category.
type SmartPricePreviewCategory struct {
	TierID    uuid.UUID        `json:"tier_id"`
	Name      string           `json:"name"`
	Currency  string           `json:"currency"`
	BasePrice int64            `json:"base_price"`
	CapPrice  int64            `json:"cap_price"`
	Steps     []SmartPriceStep `json:"steps"`
}

// SmartPricePreviewResponse answers the preview route.
type SmartPricePreviewResponse struct {
	Kind       string                      `json:"kind"`
	Categories []SmartPricePreviewCategory `json:"categories"`
}

// SmartPriceNow is where a rule stands at the moment of the read.
type SmartPriceNow struct {
	Price     int64  `json:"price"`
	Step      int    `json:"step"`
	Steps     int    `json:"steps"`
	NextPrice *int64 `json:"next_price"`
	// RemainingAtPrice is how many tickets are left before the next step
	// (by_sales); NextAt is when the next window starts (by_days, last_days).
	RemainingAtPrice *int32     `json:"remaining_at_price"`
	NextAt           *time.Time `json:"next_at"`
}

// SmartPriceRule is one preset of a session.
type SmartPriceRule struct {
	ID       uuid.UUID `json:"id"`
	TierID   uuid.UUID `json:"tier_id"`
	TierName string    `json:"tier_name"`
	// StepTierIDs are the later steps the preset created (by_sales).
	StepTierIDs []uuid.UUID       `json:"step_tier_ids"`
	Kind        string            `json:"kind"`
	Params      smartprice.Params `json:"params"`
	BasePrice   int64             `json:"base_price"`
	Currency    string            `json:"currency"`
	Status      string            `json:"status"`
	CreatedAt   time.Time         `json:"created_at"`
	StoppedAt   *time.Time        `json:"stopped_at"`
	// Now is filled for an active rule whose categories still exist.
	Now *SmartPriceNow `json:"now"`
}

// SmartPriceRulesResponse answers the list, enable and stop routes.
type SmartPriceRulesResponse struct {
	Rules    []SmartPriceRule `json:"rules"`
	Warnings []Warning        `json:"warnings"`
}

// smartScope is a session read back as its event-bundle.
type smartScope struct {
	src    bundleSource
	ss     *bundleSessionSource
	bundle bil24compat.ImportSessionRequest
	loc    *time.Location
	tiers  map[uuid.UUID]gen.TicketTierRow
	// next is the chain link of a category; incoming marks a successor.
	next     map[uuid.UUID]gen.TierChainRow
	incoming map[uuid.UUID]bool
	rules    []gen.SmartPriceRuleRow
	// fingerprint digests what the plan is built from (gen.SmartSessionFingerprint).
	fingerprint string
}

var errSmartSessionNotFound = errors.New("session not found")

type smartSessionUnsupportedError struct{ code, msg string }

func (e *smartSessionUnsupportedError) Error() string { return e.msg }

// loadSmartScope reads the session of orgID in one snapshot.
func (h *Handler) loadSmartScope(ctx context.Context, orgID, sessionID uuid.UUID) (smartScope, error) {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return smartScope{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	eventID, err := q.GetSessionEventID(ctx, sessionID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return smartScope{}, errSmartSessionNotFound
	case err != nil:
		return smartScope{}, err
	}
	src, err := loadBundleSource(ctx, q, orgID, eventID)
	switch {
	case errors.Is(err, errBundleEventNotFound):
		return smartScope{}, errSmartSessionNotFound
	case err != nil:
		return smartScope{}, err
	}
	sc := smartScope{src: src, tiers: map[uuid.UUID]gen.TicketTierRow{},
		next: map[uuid.UUID]gen.TierChainRow{}, incoming: map[uuid.UUID]bool{}}
	for i := range src.Sessions {
		if src.Sessions[i].Session.ID == sessionID {
			sc.ss = &src.Sessions[i]
		}
	}
	if sc.ss == nil {
		return smartScope{}, errSmartSessionNotFound
	}
	if sc.rules, err = q.ListSmartPriceRulesBySession(ctx, sessionID, orgID); err != nil {
		return smartScope{}, err
	}
	if sc.fingerprint, err = q.SmartSessionFingerprint(ctx, sessionID); err != nil {
		return smartScope{}, err
	}
	for _, t := range sc.ss.Tiers {
		sc.tiers[t.ID] = t
	}
	for _, l := range sc.ss.Chain {
		sc.next[l.TierID] = l
		sc.incoming[l.NextTierID] = true
	}
	read := buildEventBundles(src)
	for _, sk := range read.Skipped {
		if sk.SessionID == sessionID {
			return sc, &smartSessionUnsupportedError{code: sk.Code, msg: sk.Message}
		}
	}
	for _, b := range read.Bundles {
		if b.ActionEvent.ArenaSessionID == sessionID.String() {
			sc.bundle = b
		}
	}
	sc.loc = time.UTC
	if sc.ss.Venue != nil {
		if l, err := time.LoadLocation(derefString(sc.ss.Venue.Timezone)); err == nil && derefString(sc.ss.Venue.Timezone) != "" {
			sc.loc = l
		}
	}
	return sc, nil
}

func (sc smartScope) seated() bool {
	s := sc.ss.Session
	return s.SeatingPlanVersionID != nil || s.AdmissionMode != "general_admission"
}

func (sc smartScope) activeRuleFor(tierID uuid.UUID) bool {
	for _, r := range sc.rules {
		if r.Status != "active" {
			continue
		}
		if r.TierID == tierID {
			return true
		}
		for _, id := range r.StepTierIDs {
			if id == tierID {
				return true
			}
		}
	}
	return false
}

// categoryIndex is the bundle entry of a tier, -1 when the bundle has none.
func (sc smartScope) categoryIndex(tierID uuid.UUID) int {
	for i, c := range sc.bundle.CategoryList {
		if c.ArenaTierID == tierID.String() {
			return i
		}
	}
	return -1
}

// smartPlan is a bundle a preset changed, with its preview.
type smartPlan struct {
	bundle  bil24compat.ImportSessionRequest
	preview SmartPricePreviewResponse
	// stepNames are the names of the categories a by_sales preset creates.
	stepNames []string
}

func smartFail(code, msg string) error { return &smartprice.Error{Code: code, Message: msg} }

// planSmart applies a preset to a copy of the session's bundle.
func planSmart(sc smartScope, req SmartPriceRequest, now time.Time) (smartPlan, error) {
	if err := req.Validate(); err != nil {
		return smartPlan{}, err
	}
	switch {
	case len(req.TierIDs) == 0:
		return smartPlan{}, smartFail("smart_price.no_category", "name the category the preset applies to")
	case req.Kind == smartprice.KindBySales && len(req.TierIDs) != 1:
		return smartPlan{}, smartFail("smart_price.one_category", "a rise by tickets sold applies to one category")
	case req.Kind == smartprice.KindBySales && sc.seated():
		return smartPlan{}, smartFail("smart_price.seated_by_sales", "a session with seats can only rise by days")
	}
	out := smartPlan{bundle: sc.bundle, preview: SmartPricePreviewResponse{Kind: req.Kind}}
	out.bundle.CategoryList = append([]bil24compat.ImportSessionCategory(nil), sc.bundle.CategoryList...)
	seen := map[uuid.UUID]bool{}
	for _, tierID := range req.TierIDs {
		if seen[tierID] {
			return smartPlan{}, smartFail("smart_price.duplicate_category", "a category is named twice")
		}
		seen[tierID] = true
		t, ok := sc.tiers[tierID]
		idx := sc.categoryIndex(tierID)
		switch {
		case !ok || idx < 0:
			return smartPlan{}, smartFail("smart_price.category_not_found", "the category is not in this session")
		case t.PricingMode != "fixed" || t.PriceAmount <= 0:
			return smartPlan{}, smartprice.ErrFreeCategory
		case sc.activeRuleFor(tierID):
			return smartPlan{}, smartFail("smart_price.already_on", "the category already follows a smart price")
		case sc.next[tierID].NextTierID != uuid.Nil || sc.incoming[tierID]:
			return smartPlan{}, smartFail("smart_price.category_in_chain", "the category already sells in steps")
		}
		pc := SmartPricePreviewCategory{TierID: tierID, Name: t.Name, Currency: t.Currency, BasePrice: t.PriceAmount}
		cat := out.bundle.CategoryList[idx]
		switch req.Kind {
		case smartprice.KindBySales:
			// A running or future price change would sell the head dearer
			// than the steps after it: the price must only go up.
			if pastWindows(sc.src.Windows[tierID], now) == nil {
				return smartPlan{}, smartFail("smart_price.schedule_exists", "the category already has future price changes")
			}
			st := sc.ss.Stats[tierID]
			steps, err := smartprice.ExpandBySales(req.Params, smartprice.SalesInput{
				Base: t.PriceAmount, Currency: t.Currency, Owned: st.Quantity, Used: st.Sold + st.Held,
			})
			if err != nil {
				return smartPlan{}, err
			}
			pc.CapPrice = smartprice.CapPrice(t.PriceAmount, req.CapPercent, t.Currency)
			names := stepNames(sc, t.Name, len(steps)-1)
			out.stepNames = names
			cat.Availability = st.Quantity
			first := len(out.bundle.CategoryList)
			limit := steps[0].Limit
			cat.NextCategoryIndex, cat.SellLimit = &first, &limit
			out.bundle.CategoryList[idx] = cat
			for k, s := range steps {
				pc.Steps = append(pc.Steps, SmartPriceStep{Places: s.Places, Price: s.Price})
				if k == 0 {
					continue
				}
				empty := []bil24compat.ImportPriceWindow{}
				next, lim := -1, int32(0)
				if k+1 < len(steps) {
					next, lim = len(out.bundle.CategoryList)+1, s.Limit
				}
				out.bundle.CategoryList = append(out.bundle.CategoryList, bil24compat.ImportSessionCategory{
					CategoryPriceName: names[k-1],
					Price:             money.Major(s.Price),
					PriceSchedule:     &empty,
					NextCategoryIndex: &next,
					SellLimit:         &lim,
				})
			}
		default:
			past := pastWindows(sc.src.Windows[tierID], now)
			if past == nil {
				return smartPlan{}, smartFail("smart_price.schedule_exists", "the category already has future price changes")
			}
			ws, err := smartprice.ExpandByDays(req.Params, smartprice.DaysInput{
				Base: t.PriceAmount, Currency: t.Currency, Loc: sc.loc,
				SessionStart: sc.ss.Session.StartAt, SalesEnd: sc.ss.Session.SalesEndAt, Now: now,
			})
			if err != nil {
				return smartPlan{}, err
			}
			pc.CapPrice = smartprice.CapPrice(t.PriceAmount, req.CapPercent, t.Currency)
			if req.Kind == smartprice.KindLastDays {
				pc.CapPrice = ws[0].Price
			}
			schedule := *past
			// The import keeps at most maxImportPriceWindows windows: the
			// oldest past ones, which no longer price anything, go first.
			if extra := len(schedule) + len(ws) - maxImportPriceWindows; extra > 0 {
				schedule = schedule[min(extra, len(schedule)):]
			}
			for _, w := range ws {
				from := w.From
				pc.Steps = append(pc.Steps, SmartPriceStep{From: &from, Price: w.Price})
				iw := bil24compat.ImportPriceWindow{ValidFrom: w.From.In(sc.loc).Format(time.RFC3339), Price: money.Major(w.Price)}
				if w.To != nil {
					iw.ValidTo = w.To.In(sc.loc).Format(time.RFC3339)
				}
				schedule = append(schedule, iw)
			}
			cat.PriceSchedule = &schedule
			out.bundle.CategoryList[idx] = cat
		}
		out.preview.Categories = append(out.preview.Categories, pc)
	}
	return out, nil
}

// pastWindows keeps the windows that ended before now, as bundle windows. It
// answers nil when a window is still running or still to come: a preset never
// overwrites price changes the organizer set by hand.
func pastWindows(ws []gen.TicketTierPriceRow, now time.Time) *[]bil24compat.ImportPriceWindow {
	out := []bil24compat.ImportPriceWindow{}
	for _, w := range ws {
		if w.ValidTo == nil || w.ValidTo.After(now) {
			return nil
		}
		out = append(out, bil24compat.ImportPriceWindow{
			ValidFrom: w.ValidFrom.UTC().Format(time.RFC3339),
			ValidTo:   w.ValidTo.UTC().Format(time.RFC3339),
			Price:     money.Major(w.PriceAmount),
		})
	}
	return &out
}

// stepNames names the categories after the first: the import matches a
// category by name, so each step needs its own. The buyer sees the first
// one's name on every step (owner decision В-4, hfeed).
func stepNames(sc smartScope, base string, n int) []string {
	taken := map[string]bool{}
	for _, t := range sc.tiers {
		taken[strings.ToLower(strings.TrimSpace(t.Name))] = true
	}
	out := make([]string, 0, n)
	for suffix := 2; len(out) < n; suffix++ {
		name := fmt.Sprintf("%s · %d", base, suffix)
		if taken[strings.ToLower(name)] {
			continue
		}
		taken[strings.ToLower(name)] = true
		out = append(out, name)
	}
	return out
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

func (h *Handler) smartPathScope(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return uuid.Nil, uuid.Nil, false
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	if !h.requireOrgMembership(w, r, orgID) {
		return uuid.Nil, uuid.Nil, false
	}
	return orgID, sessionID, true
}

// smartLoad reads the scope and answers its own errors.
func (h *Handler) smartLoad(w http.ResponseWriter, r *http.Request, orgID, sessionID uuid.UUID, needBundle bool) (smartScope, bool) {
	sc, err := h.loadSmartScope(r.Context(), orgID, sessionID)
	var unsupported *smartSessionUnsupportedError
	switch {
	case errors.Is(err, errSmartSessionNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return sc, false
	case errors.As(err, &unsupported):
		if !needBundle && sc.ss != nil {
			return sc, true
		}
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"smart_price.session_unsupported", "a smart price cannot be applied to this session: "+unsupported.msg, r,
			map[string]any{"reason": unsupported.code}))
		return sc, false
	case err != nil:
		h.logger.Error("smart price: read failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"smart_price.read_failed", "failed to read the session", r))
		return sc, false
	}
	return sc, true
}

func (h *Handler) writeSmartError(w http.ResponseWriter, r *http.Request, err error) {
	if e, ok := smartprice.AsError(err); ok {
		status := http.StatusUnprocessableEntity
		switch e.Code {
		case "smart_price.already_on", "smart_price.category_in_chain", "smart_price.schedule_exists", "smart_price.not_active", "smart_price.changed":
			status = http.StatusConflict
		case "smart_price.not_found":
			status = http.StatusNotFound
		}
		httputil.WriteJSON(w, status, httputil.ErrorEnvelope(e.Code, e.Message, r))
		return
	}
	var ie *importError
	if errors.As(err, &ie) {
		h.writeImportError(w, r, err)
		return
	}
	h.logger.Error("smart price: write failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"smart_price.write_failed", "failed to save the smart price", r))
}

func decodeSmartRequest(w http.ResponseWriter, r *http.Request) (SmartPriceRequest, bool) {
	var req SmartPriceRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"smart_price.invalid_body", "request body is not valid JSON: "+err.Error(), r))
		return req, false
	}
	return req, true
}

// HandleSmartPriceList serves GET .../sessions/{session_id}/smart-price.
func (h *Handler) HandleSmartPriceList(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.smartPathScope(w, r)
	if !ok {
		return
	}
	sc, ok := h.smartLoad(w, r, orgID, sessionID, false)
	if !ok {
		return
	}
	httputil.WriteJSON(w, http.StatusOK, SmartPriceRulesResponse{Rules: smartRules(sc, h.now()), Warnings: []Warning{}})
}

// HandleSmartPricePreview serves POST .../smart-price/preview: the table a
// preset would write. Nothing is written.
func (h *Handler) HandleSmartPricePreview(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.smartPathScope(w, r)
	if !ok {
		return
	}
	req, ok := decodeSmartRequest(w, r)
	if !ok {
		return
	}
	sc, ok := h.smartLoad(w, r, orgID, sessionID, true)
	if !ok {
		return
	}
	plan, err := planSmart(sc, req, h.now())
	if err != nil {
		h.writeSmartError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, plan.preview)
}

// HandleSmartPriceEnable serves POST .../smart-price: the preset is written
// through the event-bundle import and recorded in smart_price_rules in the
// same transaction.
func (h *Handler) HandleSmartPriceEnable(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.smartPathScope(w, r)
	if !ok {
		return
	}
	req, ok := decodeSmartRequest(w, r)
	if !ok {
		return
	}
	sc, ok := h.smartLoad(w, r, orgID, sessionID, true)
	if !ok {
		return
	}
	params, err := json.Marshal(req.Params)
	if err != nil {
		h.writeSmartError(w, r, err)
		return
	}
	ctx := r.Context()
	createdBy := actorUUID(ctx)
	warnings, err := h.runSmartImport(ctx, orgID, sessionID, sc, func(sc smartScope) (bil24compat.ImportSessionRequest, func(q *gen.Queries) error, error) {
		plan, err := planSmart(sc, req, h.now())
		if err != nil {
			return bil24compat.ImportSessionRequest{}, nil, err
		}
		return plan.bundle, func(q *gen.Queries) error {
			var stepIDs []uuid.UUID
			if len(plan.stepNames) > 0 {
				tiers, err := q.ListTicketTiersBySession(ctx, sessionID)
				if err != nil {
					return err
				}
				byName := map[string]uuid.UUID{}
				for _, t := range tiers {
					byName[t.Name] = t.ID
				}
				for _, n := range plan.stepNames {
					id, ok := byName[n]
					if !ok {
						return fmt.Errorf("step category %q was not created", n)
					}
					stepIDs = append(stepIDs, id)
				}
			}
			for _, c := range plan.preview.Categories {
				ids := stepIDs
				if req.Kind != smartprice.KindBySales {
					ids = nil
				}
				if _, err := q.InsertSmartPriceRule(ctx, gen.InsertSmartPriceRuleParams{
					OrgID: orgID, SessionID: sessionID, TierID: c.TierID, Kind: req.Kind,
					Params: params, BasePrice: c.BasePrice, StepTierIDs: ids, CreatedBy: createdBy,
				}); err != nil {
					var pgErr *pgconn.PgError
					if errors.As(err, &pgErr) && pgErr.Code == "23505" {
						return smartFail("smart_price.already_on", "the category already follows a smart price")
					}
					return err
				}
			}
			return nil
		}, nil
	})
	if err != nil {
		h.writeSmartError(w, r, err)
		return
	}
	h.writeSmartAudit(ctx, r, orgID, sessionID, "v1.smart_price.enable", map[string]any{
		"kind": req.Kind, "tier_ids": req.TierIDs, "step_percent": req.StepPercent,
		"every": req.Every, "cap_percent": req.CapPercent,
	})
	h.answerSmartRules(w, r, orgID, sessionID, warnings)
}

// HandleSmartPriceStop serves POST .../smart-price/{rule_id}/stop. The price
// stops where it is now: the later steps of a chain take the current step's
// price (no place moves), the future windows of a schedule are removed.
func (h *Handler) HandleSmartPriceStop(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.smartPathScope(w, r)
	if !ok {
		return
	}
	ruleID, ok := httputil.UUIDPathParam(w, r, "rule_id")
	if !ok {
		return
	}
	sc, ok := h.smartLoad(w, r, orgID, sessionID, true)
	if !ok {
		return
	}
	var kind string
	ctx := r.Context()
	warnings, err := h.runSmartImport(ctx, orgID, sessionID, sc, func(sc smartScope) (bil24compat.ImportSessionRequest, func(q *gen.Queries) error, error) {
		bundle, rule, err := planSmartStop(sc, ruleID, h.now())
		if err != nil {
			return bil24compat.ImportSessionRequest{}, nil, err
		}
		kind = rule.Kind
		return bundle, func(q *gen.Queries) error {
			if _, err := q.GetActiveSmartPriceRule(ctx, ruleID, orgID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return smartFail("smart_price.not_active", "the smart price is already off")
				}
				return err
			}
			return q.StopSmartPriceRule(ctx, ruleID)
		}, nil
	})
	if err != nil {
		h.writeSmartError(w, r, err)
		return
	}
	h.writeSmartAudit(ctx, r, orgID, sessionID, "v1.smart_price.stop", map[string]any{"rule_id": ruleID.String(), "kind": kind})
	h.answerSmartRules(w, r, orgID, sessionID, warnings)
}

// planSmartStop builds the bundle that freezes a preset's price.
func planSmartStop(sc smartScope, ruleID uuid.UUID, now time.Time) (bil24compat.ImportSessionRequest, gen.SmartPriceRuleRow, error) {
	var rule *gen.SmartPriceRuleRow
	for i := range sc.rules {
		if sc.rules[i].ID == ruleID {
			rule = &sc.rules[i]
		}
	}
	switch {
	case rule == nil:
		return bil24compat.ImportSessionRequest{}, gen.SmartPriceRuleRow{}, smartFail("smart_price.not_found", "smart price not found")
	case rule.Status != "active":
		return bil24compat.ImportSessionRequest{}, gen.SmartPriceRuleRow{}, smartFail("smart_price.not_active", "the smart price is already off")
	}
	bundle := sc.bundle
	bundle.CategoryList = append([]bil24compat.ImportSessionCategory(nil), sc.bundle.CategoryList...)
	if rule.Kind == smartprice.KindBySales {
		members := chainMembers(sc, rule.TierID)
		sell := sellingMember(sc, members)
		price := sc.tiers[members[sell]].PriceAmount
		for _, id := range members[sell+1:] {
			if i := sc.categoryIndex(id); i >= 0 {
				bundle.CategoryList[i].Price = money.Major(price)
			}
		}
	} else if i := sc.categoryIndex(rule.TierID); i >= 0 {
		kept := []bil24compat.ImportPriceWindow{}
		for _, win := range sc.src.Windows[rule.TierID] {
			if win.ValidFrom.After(now) {
				continue
			}
			iw := bil24compat.ImportPriceWindow{ValidFrom: win.ValidFrom.UTC().Format(time.RFC3339), Price: money.Major(win.PriceAmount)}
			if win.ValidTo != nil && !win.ValidTo.After(now) {
				iw.ValidTo = win.ValidTo.UTC().Format(time.RFC3339)
			}
			kept = append(kept, iw)
		}
		bundle.CategoryList[i].PriceSchedule = &kept
	}
	return bundle, *rule, nil
}

func (h *Handler) answerSmartRules(w http.ResponseWriter, r *http.Request, orgID, sessionID uuid.UUID, warnings []Warning) {
	sc, ok := h.smartLoad(w, r, orgID, sessionID, false)
	if !ok {
		return
	}
	if warnings == nil {
		warnings = []Warning{}
	}
	httputil.WriteJSON(w, http.StatusOK, SmartPriceRulesResponse{Rules: smartRules(sc, h.now()), Warnings: warnings})
}

// smartAttempts bounds how often a smart price write reads the session again
// after it changed under it (a colleague's edit, a hand-over of places).
const smartAttempts = 3

// errSmartChanged: the session changed between the read the plan was built
// from and the write. The write is planned again from a fresh read.
var errSmartChanged = smartFail("smart_price.changed", "the date's categories changed at the same moment; open the card again and repeat")

// smartBuild plans the bundle and the preset rows from one read of the session.
type smartBuild func(sc smartScope) (bil24compat.ImportSessionRequest, func(q *gen.Queries) error, error)

// runSmartImport plans from sc and posts the bundle through the import's own
// preparation and executor, with the preset rows in the same transaction.
// The transaction first locks the session row and its categories and checks
// that nothing the plan was built from changed since sc was read; when
// something did, it reads the session again and plans anew.
func (h *Handler) runSmartImport(ctx context.Context, orgID, sessionID uuid.UUID, sc smartScope, build smartBuild) ([]Warning, error) {
	for attempt := 1; ; attempt++ {
		warnings, err := h.smartImportOnce(ctx, orgID, sessionID, sc, build)
		retry := errors.Is(err, errSmartChanged) || txretry.IsSerializationFailure(err)
		if !retry || attempt == smartAttempts {
			return warnings, err
		}
		if sc, err = h.loadSmartScope(ctx, orgID, sessionID); err != nil {
			return nil, err
		}
	}
}

func (h *Handler) smartImportOnce(ctx context.Context, orgID, sessionID uuid.UUID, sc smartScope, build smartBuild) ([]Warning, error) {
	req, extra, err := build(sc)
	if err != nil {
		return nil, err
	}
	warnings := newWarningSink()
	issues := newIssueSink(importMode{})
	prep := h.prepareImport(ctx, orgID, req, "", importMode{}, warnings, issues)
	if err := issues.first(); err != nil {
		return nil, err
	}
	plan := prep.plan
	plan.SmartPriceWrite = true
	plan.PosterMediaID = h.sideLoadPoster(ctx, orgID, req,
		h.currentPosterMediaID(ctx, orgID, plan.Source, plan.ExternalRef, req), warnings)
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	if err := q.LockSmartSession(ctx, sessionID); err != nil {
		return nil, err
	}
	fp, err := q.SmartSessionFingerprint(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if fp != sc.fingerprint {
		return nil, errSmartChanged
	}
	if _, err := h.executorFor(plan.Source)(ctx, q, tx, plan, warnings); err != nil {
		return nil, err
	}
	if err := extra(q); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return warnings.list(), nil
}

func (h *Handler) writeSmartAudit(ctx context.Context, r *http.Request, orgID, sessionID uuid.UUID, action string, meta map[string]any) {
	if h.audit == nil {
		return
	}
	actor, _ := auth.ActorFromContext(ctx)
	meta["org_id"] = orgID.String()
	if err := h.audit.Write(ctx, audit.Event{
		OccurredAt:   h.now(),
		ActorType:    actorType(actor),
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "session",
		ResourceID:   sessionID.String(),
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     meta,
	}); err != nil {
		h.logger.Error("smart price: audit write failed", slog.String("error", err.Error()))
	}
}

func actorUUID(ctx context.Context) *uuid.UUID {
	actor, _ := auth.ActorFromContext(ctx)
	if id, err := uuid.Parse(actor.ID); err == nil {
		return &id
	}
	return nil
}

// ── where a rule stands ──────────────────────────────────────────────────────

// chainMembers walks a chain from its head.
func chainMembers(sc smartScope, head uuid.UUID) []uuid.UUID {
	out := []uuid.UUID{head}
	seen := map[uuid.UUID]bool{head: true}
	for cur := head; ; {
		l, ok := sc.next[cur]
		if !ok || seen[l.NextTierID] {
			return out
		}
		cur = l.NextTierID
		seen[cur] = true
		out = append(out, cur)
	}
}

// sellingMember is the first member that has not handed its places on.
func sellingMember(sc smartScope, members []uuid.UUID) int {
	for i, id := range members {
		l, ok := sc.next[id]
		if !ok || l.HandedOverAt == nil {
			return i
		}
	}
	return len(members) - 1
}

func smartRules(sc smartScope, now time.Time) []SmartPriceRule {
	out := make([]SmartPriceRule, 0, len(sc.rules))
	for _, r := range sc.rules {
		var p smartprice.Params
		_ = json.Unmarshal(r.Params, &p)
		steps := r.StepTierIDs
		if steps == nil {
			steps = []uuid.UUID{}
		}
		item := SmartPriceRule{ID: r.ID, TierID: r.TierID, StepTierIDs: steps, Kind: r.Kind, Params: p, BasePrice: r.BasePrice,
			Status: r.Status, CreatedAt: r.CreatedAt, StoppedAt: r.StoppedAt}
		t, ok := sc.tiers[r.TierID]
		if ok {
			item.TierName, item.Currency = t.Name, t.Currency
		}
		if ok && r.Status == "active" {
			item.Now = smartNow(sc, r, t, now)
		}
		out = append(out, item)
	}
	return out
}

func smartNow(sc smartScope, r gen.SmartPriceRuleRow, t gen.TicketTierRow, now time.Time) *SmartPriceNow {
	if r.Kind == smartprice.KindBySales {
		members := chainMembers(sc, r.TierID)
		sell := sellingMember(sc, members)
		cur := sc.tiers[members[sell]]
		n := &SmartPriceNow{Price: cur.PriceAmount, Step: sell + 1, Steps: len(members)}
		if sell+1 < len(members) {
			np := sc.tiers[members[sell+1]].PriceAmount
			n.NextPrice = &np
			left := sc.ss.Stats[cur.ID].Available
			n.RemainingAtPrice = &left
		}
		return n
	}
	var ws []pricing.Window
	for _, w := range sc.src.Windows[t.ID] {
		ws = append(ws, pricing.Window{From: w.ValidFrom, To: w.ValidTo, Amount: w.PriceAmount})
	}
	price, next := pricing.Resolve(t.PriceAmount, ws, now)
	n := &SmartPriceNow{Price: price, Steps: len(ws) + 1, Step: 1}
	for _, w := range ws {
		if !w.From.After(now) {
			n.Step++
		}
	}
	if next != nil {
		np, _ := pricing.Resolve(t.PriceAmount, ws, *next)
		n.NextPrice, n.NextAt = &np, next
	}
	return n
}
