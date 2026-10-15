// dry_run.go — POST /v1/organizations/{org_id}/imports/event-bundle?dry_run=true
// (task 3-1, plan docs/plans/event_center_unified_plan_2026-10-14_ru.md §8.2).
//
// A dry run answers "what would this bundle do, and what is wrong with it?"
// with the SAME code the import runs, and writes nothing. It runs in three
// stages:
//
//  1. The pre-transaction ladder (prepareImport) plus checkBundleFields: every
//     check that needs no database, over the whole bundle. In an import the
//     first failure stops the ladder; a dry run collects them all.
//  2. The references (checkArenaReferences / checkBil24References): every id,
//     UUID and name the bundle points at is looked up, read only, inside a
//     transaction, each one on its own, so all of them are reported together.
//     These are the executors' own lookup helpers.
//  3. Only when stages 1-2 found nothing: the executor itself runs inside that
//     transaction in dry-run mode, and the transaction is rolled back. The
//     errors only this stage can find (they depend on the writes before
//     them: a chain cycle through stored links, a quantity step without a
//     link, a refused session move) are therefore reported one at a time.
//
// "Rolled back" is not the same as "nothing happened". In dry-run mode
// (importPlan.Mode) the executors skip every effect that outlives the
// transaction or reaches outside it: the poster side-load (no download, no
// media object — only posterPrecheck's reasons), sessionchange.Apply (its
// journal and worker.EnqueueInTx letters; sessionchange.Preview answers the
// same refusals instead), the publish transition, channel publications and
// feed tokens, compat ids (their sequence would move), the audit event and
// the post-commit catalog webhook. Sequences behind rows the rolled-back
// transaction inserted (session_seats system ids and the like) may still
// advance: gaps are harmless and nothing persists.
package himports

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// importMode is how handleImport runs: an import, or a dry run.
type importMode struct {
	DryRun bool
}

// issueSink collects the errors of an import. In an import it keeps the
// first one and tells the caller to stop; in a dry run it keeps every
// *importError and only stops on an infrastructure fault.
type issueSink struct {
	all   bool
	items []error
}

func newIssueSink(mode importMode) *issueSink {
	return &issueSink{all: mode.DryRun}
}

// add records err and reports whether the caller must stop.
func (s *issueSink) add(err error) bool {
	if err == nil {
		return false
	}
	s.items = append(s.items, err)
	if !s.all {
		return true
	}
	var ie *importError
	return !errors.As(err, &ie)
}

// first is the first recorded error, nil when there is none.
func (s *issueSink) first() error {
	if len(s.items) == 0 {
		return nil
	}
	return s.items[0]
}

// fatal is the first error that is not a caller error (an infrastructure
// fault), nil when there is none.
func (s *issueSink) fatal() error {
	for _, err := range s.items {
		var ie *importError
		if !errors.As(err, &ie) {
			return err
		}
	}
	return nil
}

func (s *issueSink) empty() bool { return len(s.items) == 0 }

// list renders the caller errors in the order they were found. A field
// reported once is not reported again: a later stage that trips over the
// same field (an out-of-range id the lookup then cannot resolve) adds
// nothing the caller can act on.
func (s *issueSink) list() []ImportIssue {
	out := []ImportIssue{}
	seenField := map[string]bool{}
	seenCode := map[string]bool{}
	for _, err := range s.items {
		var ie *importError
		if !errors.As(err, &ie) {
			continue
		}
		if ie.field != "" {
			if seenField[ie.field] {
				continue
			}
			seenField[ie.field] = true
		} else {
			if seenCode[ie.code] {
				continue
			}
			seenCode[ie.code] = true
		}
		out = append(out, ImportIssue{Code: ie.code, Message: ie.message, Field: ie.field, Status: ie.status})
	}
	return out
}

// dryFacts is what the dry run learned about the bundle's target.
type dryFacts struct {
	eventID    *uuid.UUID
	sessionID  *uuid.UUID
	newEvent   bool
	newSession bool
	slug       string
	loc        *time.Location
	startAt    time.Time
}

// runDryImport finishes a dry run after prepareImport (stage 1) and answers
// with the report.
func (h *Handler) runDryImport(w http.ResponseWriter, r *http.Request, prep preparedImport, warnings *warningSink, issues *issueSink) {
	ctx := r.Context()
	if fatal := issues.fatal(); fatal != nil {
		h.writeFirstImportError(w, r, fatal)
		return
	}
	var facts dryFacts
	if prep.sourceOK {
		if raw := trimSpace(prep.plan.Request.Action.BigPosterURL); raw != "" {
			if reason := h.posterPrecheck(raw); reason != "" {
				warnings.add(WarnPosterSkipped, "poster "+raw+" was not imported: "+reason)
			}
		}
		tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			h.logger.Error("import dry run: begin tx failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"import.transaction_failed", "failed to start import transaction", r,
			))
			return
		}
		// The dry run's transaction is NEVER committed.
		defer func() { _ = tx.Rollback(ctx) }()
		q := gen.New(tx)
		if prep.plan.Source == bil24compat.SourceArena {
			facts = h.checkArenaReferences(ctx, q, tx, prep, warnings, issues)
		} else {
			facts = h.checkBil24References(ctx, q, tx, prep, warnings, issues)
		}
		if issues.empty() {
			if _, err := h.executorFor(prep.plan.Source)(ctx, q, tx, prep.plan, warnings); err != nil {
				issues.add(err)
			}
		}
		_ = tx.Rollback(ctx)
		if fatal := issues.fatal(); fatal != nil {
			h.writeImportError(w, r, fatal)
			return
		}
	}

	errs := issues.list()
	httputil.WriteJSON(w, http.StatusOK, ImportDryRunResponse{
		DryRun:     true,
		Valid:      len(errs) == 0,
		Source:     prep.plan.Source,
		Normalized: normalizeBundle(prep),
		Defaults:   dryDefaults(prep, facts),
		Warnings:   warnings.list(),
		Errors:     errs,
		EventID:    facts.eventID,
		SessionID:  facts.sessionID,
	})
}

// checkBundleFields is stage 1's second half: the checks the executors run on
// single fields inside the transaction, run here over the whole bundle so a
// dry run reports them all. They are the executors' own helpers.
func checkBundleFields(plan importPlan, issues *issueSink) {
	req := plan.Request
	arena := plan.Source == bil24compat.SourceArena
	if _, _, err := importDoorsOpen(req.ActionEvent, time.Time{}); err != nil {
		issues.add(err)
	}
	for i, c := range req.CategoryList {
		issues.add(checkCategoryPrice(i, c.PriceMinorUnits()))
		if !arena {
			continue
		}
		if _, _, err := categorySaleWindow(i, c, plan.SaleWindowStart); err != nil {
			issues.add(err)
		}
		if c.NextCategoryIndex != nil {
			if j := *c.NextCategoryIndex; j != -1 && nextIndexOutsideList(i, j, len(req.CategoryList)) {
				issues.add(errInvalidNextCategory(i, j))
			}
		}
		if c.SellLimit != nil && *c.SellLimit < 0 {
			issues.add(errNegativeSellLimit(i))
		}
		if c.PriceSchedule != nil {
			if _, err := parseImportedSchedule(i, c.PriceMinorUnits(), *c.PriceSchedule); err != nil {
				issues.add(err)
			}
		}
	}
}

// checkArenaReferences is stage 2 for source=arena: each reference of the
// bundle is resolved on its own, reading only, with the executor's helpers.
func (h *Handler) checkArenaReferences(ctx context.Context, q *gen.Queries, tx pgx.Tx, prep preparedImport, warnings *warningSink, issues *issueSink) dryFacts {
	plan := prep.plan
	req := plan.Request
	var f dryFacts

	match, err := h.matchArenaSession(ctx, q, tx, plan)
	matched := err == nil
	if !matched {
		if issues.add(err) {
			return f
		}
		match = arenaMatch{}
	} else if match.SessionID != uuid.Nil {
		id := match.SessionID
		f.sessionID = &id
	}

	eventID, err := h.findArenaEvent(ctx, q, tx, plan, match)
	switch {
	case err != nil:
		if issues.add(err) {
			return f
		}
	case eventID != uuid.Nil:
		f.eventID = &eventID
	case matched:
		f.newEvent = true
	}

	if _, err := resolveArenaPromoter(ctx, q, plan); err != nil && issues.add(err) {
		return f
	}

	// A bundle that names no venue keeps the addressed session's; when that
	// session could not be resolved there is nothing to check, and
	// "venue.venueName is required" would only echo the session error.
	v := req.Venue
	if !matched && trimSpace(v.VenueName) == "" && trimSpace(v.ArenaVenueID) == "" && v.VenueID <= 0 {
		h.noteNewEventSlug(ctx, q, plan, &f, issues)
		return f
	}
	d, err := h.planArenaVenue(ctx, q, tx, plan, match, warnings)
	if err != nil {
		if issues.add(err) {
			return f
		}
	} else {
		f.loc = d.loc
		if prep.startOK {
			start, startErr := req.ActionEvent.ParseLocalStart(d.loc)
			if startErr != nil {
				issues.add(failImportAt(http.StatusUnprocessableEntity, "import.invalid_start_time",
					startField(req.ActionEvent), startErr.Error()))
			} else {
				f.startAt = start.UTC()
				if _, endErr := req.ActionEvent.ParseLocalEnd(d.loc); endErr != nil {
					issues.add(failImportAt(http.StatusUnprocessableEntity, "import.end_time_invalid",
						"/actionEvent/endTime", endErr.Error()))
				}
				if _, _, doorsErr := importDoorsOpen(req.ActionEvent, f.startAt); doorsErr != nil {
					issues.add(doorsErr)
				}
			}
		}
	}

	if matched {
		f.newSession = match.SessionID == uuid.Nil
		if f.newSession && req.TotalAvailability() <= 0 {
			issues.add(errCapacityRequired())
		}
		var existing []gen.TicketTierRow
		if match.SessionID != uuid.Nil {
			existing, err = q.ListTicketTiersBySession(ctx, match.SessionID)
			if err != nil && issues.add(fmt.Errorf("list ticket tiers: %w", err)) {
				return f
			}
		}
		for i, c := range req.CategoryList {
			if _, err := arenaTierRef(ctx, tx, i, c, existing); err != nil && issues.add(err) {
				return f
			}
		}
	}

	h.noteNewEventSlug(ctx, q, plan, &f, issues)
	return f
}

// checkBil24References is stage 2 for source=bil24: the venue, event and
// session the payload's ids map to, each looked up on its own.
func (h *Handler) checkBil24References(ctx context.Context, q *gen.Queries, tx pgx.Tx, prep preparedImport, warnings *warningSink, issues *issueSink) dryFacts {
	plan := prep.plan
	var f dryFacts
	f.loc = prep.loc

	if _, _, err := findBil24Venue(ctx, q, plan); err != nil {
		// Stage 1 read the zone of the venue this Bil24 id belongs to. When
		// that venue is another organization's, or could not be checked,
		// nothing learned from it may reach the report: no venue_timezone
		// default and no "venue keeps its stored timezone" warning. The
		// import itself answers the 409 alone and shows neither.
		f.loc = nil
		warnings.drop(WarnVenueTimezoneKept)
		if issues.add(err) {
			return f
		}
	}
	eventID, err := findBil24Event(ctx, q, tx, plan)
	switch {
	case err != nil:
		if issues.add(err) {
			return f
		}
	case eventID != uuid.Nil:
		f.eventID = &eventID
	default:
		f.newEvent = true
	}
	sessionID, _, err := findBil24Session(ctx, q, tx, plan)
	switch {
	case err != nil:
		if issues.add(err) {
			return f
		}
	case sessionID != uuid.Nil:
		f.sessionID = &sessionID
	default:
		f.newSession = true
		if plan.Request.TotalAvailability() <= 0 {
			issues.add(errCapacityRequired())
		}
	}
	if f.loc != nil && prep.startOK && !plan.StartAt.IsZero() {
		f.startAt = plan.StartAt
		if _, _, doorsErr := importDoorsOpen(plan.Request.ActionEvent, plan.StartAt); doorsErr != nil {
			issues.add(doorsErr)
		}
	}
	h.noteNewEventSlug(ctx, q, plan, &f, issues)
	return f
}

// noteNewEventSlug records the public address a new event would get.
func (h *Handler) noteNewEventSlug(ctx context.Context, q *gen.Queries, plan importPlan, f *dryFacts, issues *issueSink) {
	if !f.newEvent || plan.Request.Action.Name() == "" {
		return
	}
	slug, err := freeEventSlug(ctx, q, plan.OrgID, plan.Request.Action.Name())
	if err != nil {
		issues.add(err)
		return
	}
	f.slug = slug
}

// dryDefaults lists the values the import fills in where the bundle is
// silent.
func dryDefaults(prep preparedImport, f dryFacts) []ImportDefault {
	out := []ImportDefault{}
	req := prep.plan.Request
	ae := req.ActionEvent
	if prep.startOK && trimSpace(ae.Time) == "" {
		out = append(out, ImportDefault{Name: "start_time", Field: "/actionEvent/time", Value: "00:00"})
	}
	if !f.startAt.IsZero() && f.loc != nil {
		if trimSpace(ae.EndTime) == "" {
			out = append(out, ImportDefault{Name: "session_end_time", Field: "/actionEvent/endTime",
				Value: f.startAt.Add(defaultSessionDuration).In(f.loc).Format(time.RFC3339)})
		}
		if f.newSession && trimSpace(ae.SellEndTime) == "" {
			out = append(out, ImportDefault{Name: "sales_end_time", Field: "/actionEvent/sellEndTime",
				Value: f.startAt.In(f.loc).Format(time.RFC3339)})
		}
	}
	if f.loc != nil && trimSpace(req.Venue.Timezone) == "" {
		out = append(out, ImportDefault{Name: "venue_timezone", Field: "/venue/timezone", Value: f.loc.String()})
	}
	for i, c := range req.CategoryList {
		if trimSpace(c.CategoryPriceName) != "" {
			continue
		}
		name := "Category " + strconv.Itoa(i+1)
		if prep.plan.Source != bil24compat.SourceArena {
			name = "Category " + externalIDString(c.CategoryPriceID)
		}
		out = append(out, ImportDefault{Name: "category_name",
			Field: fmt.Sprintf("/categoryList/%d/categoryPriceName", i), Value: name})
	}
	if f.slug != "" {
		out = append(out, ImportDefault{Name: "event_slug", Value: f.slug})
	}
	if f.newSession {
		status := "draft"
		if req.Publish {
			status = "scheduled"
		}
		out = append(out, ImportDefault{Name: "session_status", Field: "/publish", Value: status})
	}
	return out
}

// normalizeBundle is the bundle as the import reads it: strings trimmed, the
// source and externalRef resolved, the currency upper-cased. Nothing is
// invented: a field the bundle leaves out stays out (see dryDefaults).
func normalizeBundle(prep preparedImport) bil24compat.ImportSessionRequest {
	n := prep.plan.Request
	if prep.sourceOK {
		n.Source = prep.plan.Source
	} else {
		n.Source = trimSpace(n.Source)
	}
	n.ExternalRef = n.NormalizedExternalRef()
	if prep.plan.Currency != "" {
		n.ActionEvent.Currency = prep.plan.Currency
	} else {
		n.ActionEvent.Currency = trimSpace(n.ActionEvent.Currency)
	}

	a := &n.Action
	a.ActionName, a.FullActionName = trimSpace(a.ActionName), trimSpace(a.FullActionName)
	a.Description, a.BigPosterURL = trimSpace(a.Description), trimSpace(a.BigPosterURL)
	a.Age, a.OrganizerName, a.ArenaEventID = trimSpace(a.Age), trimSpace(a.OrganizerName), trimSpace(a.ArenaEventID)
	a.PromoterID = trimPtr(a.PromoterID)

	e := &n.ActionEvent
	e.Day, e.Time, e.EndTime = trimSpace(e.Day), trimSpace(e.Time), trimSpace(e.EndTime)
	e.SellEndTime, e.SellStartTime = trimSpace(e.SellEndTime), trimSpace(e.SellStartTime)
	e.DoorsOpenTime = trimPtr(e.DoorsOpenTime)
	e.SeatingPlanName, e.ArenaSessionID = trimSpace(e.SeatingPlanName), trimSpace(e.ArenaSessionID)

	v := &n.Venue
	v.ArenaVenueID, v.VenueName, v.Address = trimSpace(v.ArenaVenueID), trimSpace(v.VenueName), trimSpace(v.Address)
	v.CityName, v.CountryName, v.Timezone = trimSpace(v.CityName), trimSpace(v.CountryName), trimSpace(v.Timezone)

	if n.CategoryList != nil {
		cats := make([]bil24compat.ImportSessionCategory, len(n.CategoryList))
		for i, c := range n.CategoryList {
			c.CategoryPriceName, c.ArenaTierID = trimSpace(c.CategoryPriceName), trimSpace(c.ArenaTierID)
			c.SellStartTime, c.SellEndTime = trimSpace(c.SellStartTime), trimSpace(c.SellEndTime)
			cats[i] = c
		}
		n.CategoryList = cats
	}
	if n.ChannelIDs != nil {
		ids := make([]string, len(n.ChannelIDs))
		for i, id := range n.ChannelIDs {
			ids[i] = trimSpace(id)
		}
		n.ChannelIDs = ids
	}
	return n
}

func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}
