// Package collect is the billing.collect job (billing wave B-A, task B2-2,
// spec 41 section 6.2): it reads the sales tables and writes billable events,
// the append-only facts the rate job (B2-3) turns into charges.
//
// Seven sources, one per kind of fact: ticket_sold and ticket_complimentary
// from paid order items, ticket_refunded from succeeded refunds,
// ticket_cancelled from cancelled or revoked tickets no refund cancelled,
// session_cancelled from the session change journal, event_published from the
// first event publication, and ticket_complimentary again from invitations an
// operator issued without an order.
//
// Every fact is written idempotently by (kind, subject_id): a pass may read
// the same row any number of times and writes it once. Each source keeps a
// cursor in billing_job_cursors. A pass that drains a source stores the pass's
// start minus the overlap, so a row that committed a little late is still read
// by the next pass; a pass that stops at its batch cap stores the exact place
// it reached. Nothing before the date X (Options.Since) is ever read.
//
// The collector runs on plain pool-bound queries, never inside a transaction:
// each event is one idempotent statement, and a failure leaves the cursor where
// it was so the next pass resumes at the failing row.
package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

const (
	// DefaultOverlap is how far behind its start a drained pass stores the
	// cursor: an order whose paid_at was written a few minutes before its
	// transaction committed is still seen by the next pass.
	DefaultOverlap = 10 * time.Minute
	// DefaultBatchSize is the page size of one source query.
	DefaultBatchSize = 500
	// maxBatchesPerSource bounds how many pages one source reads in one pass,
	// so a long backlog (the first pass after the date X) never makes one job
	// run unbounded; the next pass resumes where this one stopped.
	maxBatchesPerSource = 20

	// SourceCollect and SourceBackfill label billable_events.source: backfill
	// is a fact read by the first pass of a source (history from the date X).
	SourceCollect  = "collect"
	SourceBackfill = "backfill"
)

// Store is the query set the collector needs. *gen.Queries satisfies it; the
// collector never opens a transaction.
type Store interface {
	billing.AccountQueries
	billing.TermsQueries

	ListCollectOrderItems(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32, complimentary bool) ([]gen.CollectCandidate, error)
	ListCollectRefunds(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]gen.CollectCandidate, error)
	ListCollectCancelledTickets(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]gen.CollectCandidate, error)
	ListCollectSessionCancellations(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]gen.CollectCandidate, error)
	ListCollectEventPublications(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]gen.CollectCandidate, error)
	ListCollectComplimentaryTickets(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]gen.CollectCandidate, error)
	GetOrgBillingCountry(ctx context.Context, orgID uuid.UUID) (string, error)

	InsertBillableEvent(ctx context.Context, p gen.InsertBillableEventParams) (id uuid.UUID, inserted bool, err error)
	GetBillingJobCursor(ctx context.Context, key string) (gen.BillingJobCursorRow, error)
	UpsertBillingJobCursor(ctx context.Context, key string, at time.Time) error
}

// Options configures a Collector.
type Options struct {
	// Since is the date X: the collector reads sales whose business time is at
	// or after it, never earlier. The zero value switches collection off.
	Since time.Time
	// Overlap, BatchSize: see DefaultOverlap and DefaultBatchSize.
	Overlap   time.Duration
	BatchSize int32
	// CursorPrefix prefixes the billing_job_cursors keys; empty means
	// "collect.". Tests that scope a collector to their own fixture use
	// another prefix so they never touch the production cursors.
	CursorPrefix string
	// Now is the clock; nil uses time.Now. Tests set it.
	Now func() time.Time
	// Logger receives one summary line per pass. nil uses slog.Default().
	Logger *slog.Logger
}

// Collector runs the collect passes.
type Collector struct {
	store Store
	opts  Options
}

// New builds a Collector over store.
func New(store Store, opts Options) *Collector {
	if opts.Overlap <= 0 {
		opts.Overlap = DefaultOverlap
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.CursorPrefix == "" {
		opts.CursorPrefix = "collect."
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	opts.Since = opts.Since.UTC()
	return &Collector{store: store, opts: opts}
}

// Enabled reports whether a date X was given.
func (c *Collector) Enabled() bool { return !c.opts.Since.IsZero() }

// SourceResult is what one source did in one pass.
type SourceResult struct {
	Source   string
	Seen     int
	Inserted int
	Drained  bool
}

// source is one table-reading query with the cursor it keeps.
type source struct {
	name string
	list func(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]gen.CollectCandidate, error)
}

func (c *Collector) sources() []source {
	s := c.store
	return []source{
		{"ticket_sold", func(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
			return s.ListCollectOrderItems(ctx, at, id, n, false)
		}},
		{"order_complimentary", func(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
			return s.ListCollectOrderItems(ctx, at, id, n, true)
		}},
		{"ticket_refunded", s.ListCollectRefunds},
		{"ticket_cancelled", s.ListCollectCancelledTickets},
		{"session_cancelled", s.ListCollectSessionCancellations},
		{"event_published", s.ListCollectEventPublications},
		{"ticket_complimentary", s.ListCollectComplimentaryTickets},
	}
}

func (c *Collector) cursorKey(name string) string { return c.opts.CursorPrefix + name }

// Run makes one pass over every source and returns what each did. It stops at
// the first failing row of a source, returns the error and leaves that
// source's cursor where it was; the sources before it keep their progress.
func (c *Collector) Run(ctx context.Context) ([]SourceResult, error) {
	if !c.Enabled() {
		return nil, nil
	}
	runStart := c.opts.Now().UTC()
	cache := newRunCache()
	var results []SourceResult
	for _, src := range c.sources() {
		res, err := c.runSource(ctx, src, runStart, cache)
		results = append(results, res)
		if err != nil {
			return results, fmt.Errorf("billing collect %s: %w", src.name, err)
		}
	}
	return results, nil
}

func (c *Collector) runSource(ctx context.Context, src source, runStart time.Time, cache *runCache) (SourceResult, error) {
	res := SourceResult{Source: src.name}
	key := c.cursorKey(src.name)

	start := c.opts.Since
	label := SourceBackfill
	hadCursor := false
	cur, err := c.store.GetBillingJobCursor(ctx, key)
	switch {
	case err == nil:
		label = SourceCollect
		hadCursor = true
		if cur.CursorAt.After(start) {
			start = cur.CursorAt.UTC()
		}
	case errors.Is(err, pgx.ErrNoRows):
		// first pass of this source: everything it reads is history from X
	default:
		return res, fmt.Errorf("read cursor: %w", err)
	}

	afterAt, afterID := start, uuid.Nil
	var lastAt time.Time
	for batch := 0; batch < maxBatchesPerSource; batch++ {
		page, err := src.list(ctx, afterAt, afterID, c.opts.BatchSize)
		if err != nil {
			return res, fmt.Errorf("read page: %w", err)
		}
		for _, cand := range page {
			inserted, err := c.write(ctx, cand, label, cache)
			if err != nil {
				return res, fmt.Errorf("event %s %s: %w", cand.Kind, cand.SubjectID, err)
			}
			res.Seen++
			if inserted {
				res.Inserted++
			}
			afterAt, afterID, lastAt = cand.OccurredAt, cand.SubjectID, cand.OccurredAt
		}
		if len(page) < int(c.opts.BatchSize) {
			res.Drained = true
			break
		}
	}

	next := start
	if res.Drained {
		if t := runStart.Add(-c.opts.Overlap); t.After(next) {
			next = t
		}
	} else if lastAt.After(next) {
		next = lastAt
	}
	if !hadCursor || next.After(cur.CursorAt) {
		if err := c.store.UpsertBillingJobCursor(ctx, key, next); err != nil {
			return res, fmt.Errorf("store cursor: %w", err)
		}
	}
	return res, nil
}

// write turns one candidate into a billable event. It reports whether the
// event is new.
func (c *Collector) write(ctx context.Context, cand gen.CollectCandidate, source string, cache *runCache) (bool, error) {
	country := ""
	if cand.VenueCountry != nil {
		country = *cand.VenueCountry
	}
	if billing.NormalizeCountry(country) == "" {
		fallback, err := cache.orgCountry(ctx, c.store, cand.OrgID)
		if err != nil {
			return false, fmt.Errorf("organization country: %w", err)
		}
		country = fallback
	}
	acc, err := cache.account(ctx, c.store, cand.OrgID, country)
	if err != nil {
		return false, err
	}

	attrs := map[string]any{}
	if len(cand.Attributes) > 0 {
		if err := json.Unmarshal(cand.Attributes, &attrs); err != nil {
			return false, fmt.Errorf("attributes of the candidate: %w", err)
		}
	}

	p := gen.InsertBillableEventParams{
		Kind:               cand.Kind,
		SubjectID:          cand.SubjectID,
		OccurredAt:         cand.OccurredAt,
		OrgID:              cand.OrgID,
		AccountID:          acc.ID,
		EventID:            cand.EventID,
		SessionID:          cand.SessionID,
		OrderID:            cand.OrderID,
		TicketID:           cand.TicketID,
		Seated:             cand.Seated,
		UnitPriceMinor:     cand.UnitPriceMinor,
		DiscountMinor:      cand.DiscountMinor,
		ServiceChargeMinor: cand.ServiceChargeMinor,
		Source:             source,
	}
	if cur := strings.ToUpper(strings.TrimSpace(cand.Currency)); cur != "" {
		p.Currency = &cur
	}
	if vc := billing.NormalizeCountry(country); vc != "" && cand.VenueCountry != nil && billing.NormalizeCountry(*cand.VenueCountry) != "" {
		p.VenueCountry = &vc
	}

	if cand.Kind == "ticket_sold" {
		class, unresolved, terr := c.classify(ctx, cache, acc.ID, cand)
		if terr != nil && !isTermsDataError(terr) {
			return false, fmt.Errorf("terms of account %s: %w", acc.ID, terr)
		}
		p.PricingClass = &class
		if unresolved {
			attrs["low_price_unresolved"] = true
		}
		if terr != nil {
			// The sale is a fact whatever the terms say; the rate job asks for
			// the terms again and fails visibly when they are really missing.
			attrs["terms_unavailable"] = true
		}
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return false, err
	}
	p.Attributes = raw

	_, inserted, err := c.store.InsertBillableEvent(ctx, p)
	return inserted, err
}

// isTermsDataError tells "this account has no usable conditions at that date"
// (recorded on the event, the rate job fails visibly later) from a database
// failure (the pass stops and retries).
func isTermsDataError(err error) bool {
	return errors.Is(err, billing.ErrNoTerms) || errors.Is(err, billing.ErrNoPriceList) || errors.Is(err, billing.ErrBadRule)
}

// classify snapshots the LOW-PRICE class of a sale (policy sections 5 and
// 12.6): the price before the promo code decides, in the sale's own currency.
// unresolved is true when the price list has no threshold in that currency
// and no exchange rate is available (the class is then "standard").
func (c *Collector) classify(ctx context.Context, cache *runCache, accountID uuid.UUID, cand gen.CollectCandidate) (class string, unresolved bool, err error) {
	if cand.UnitPriceMinor == nil {
		return billing.PricingClassStandard, true, nil
	}
	terms, err := cache.terms(ctx, c.store, accountID, cand.OccurredAt)
	if err != nil {
		return billing.PricingClassStandard, true, err
	}
	class, resolved := billing.ClassifyLowPrice(terms, strings.ToUpper(strings.TrimSpace(cand.Currency)), *cand.UnitPriceMinor)
	return class, !resolved, nil
}

// runCache keeps lookups that repeat inside one pass: the billing account of
// an (organization, country), an organization's fallback country and the
// terms of an account at one instant. It lives for one pass only, so a new
// account or new terms are seen by the next pass.
type runCache struct {
	accounts  map[string]gen.BillingAccountRow
	countries map[uuid.UUID]string
	termsMemo map[string]billing.Terms
	termsErr  map[string]error
}

func newRunCache() *runCache {
	return &runCache{
		accounts:  map[string]gen.BillingAccountRow{},
		countries: map[uuid.UUID]string{},
		termsMemo: map[string]billing.Terms{},
		termsErr:  map[string]error{},
	}
}

func (rc *runCache) orgCountry(ctx context.Context, s Store, orgID uuid.UUID) (string, error) {
	if c, ok := rc.countries[orgID]; ok {
		return c, nil
	}
	c, err := s.GetOrgBillingCountry(ctx, orgID)
	if err != nil {
		return "", err
	}
	rc.countries[orgID] = c
	return c, nil
}

func (rc *runCache) account(ctx context.Context, s Store, orgID uuid.UUID, country string) (gen.BillingAccountRow, error) {
	key := orgID.String() + "|" + billing.NormalizeCountry(country)
	if a, ok := rc.accounts[key]; ok {
		return a, nil
	}
	a, err := billing.EnsureAccount(ctx, s, orgID, country)
	if err != nil {
		return gen.BillingAccountRow{}, err
	}
	rc.accounts[key] = a
	return a, nil
}

func (rc *runCache) terms(ctx context.Context, s Store, accountID uuid.UUID, at time.Time) (billing.Terms, error) {
	key := fmt.Sprintf("%s|%d", accountID, at.UnixNano())
	if t, ok := rc.termsMemo[key]; ok {
		return t, nil
	}
	if err, ok := rc.termsErr[key]; ok {
		return billing.Terms{}, err
	}
	t, err := billing.TermsAt(ctx, s, accountID, at)
	if err != nil {
		rc.termsErr[key] = err
		return billing.Terms{}, err
	}
	rc.termsMemo[key] = t
	return t, nil
}
