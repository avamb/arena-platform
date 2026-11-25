// Package holdclose closes the provider payment page of an expired seat hold
// before its seats go back on sale (spec 08_architecture/67 section 5.2), and
// erases the visitor hashes of closed holds after 24 hours (section 6.1).
//
// hold.close_payment (every 15 s, self-scheduling like
// reservation.expire_sweep) takes the holds of the click path whose
// expires_at passed while a payment attempt with a closable provider page
// (Stripe, embedded or hosted) is still open, oldest expires_at first, and
// for each:
//
//  1. claims it: one statement locks the reservation row (SKIP LOCKED),
//     re-checks expires_at < now() and paying_until, and writes
//     closing_started_at. After the mark the extend and "paying" routes
//     refuse, so neither can arrive late to the close;
//  2. reads the provider session and its payment outside any transaction. A
//     payment being confirmed (processing, requires_action) clears the mark;
//     the hold is tried again a minute later, up to the hard limit after
//     which reservation.expire_sweep takes over;
//  3. expires the session at the provider, outside any transaction;
//  4. on a refusal (ErrCheckoutSessionNotExpirable) reads the session again:
//     expired goes on as 5, complete is left to the payment webhook, open is
//     tried again on the next pass;
//  5. when every open page of the hold is closed: one transaction, lock order
//     reservation row, sessions, seats, inventory_ledger: the attempts
//     fail with session_expired, the order expires, the checkout expires,
//     and the seats are released (hcheckout.ExpireHoldForOrderTx);
//  6. when the provider does not answer the mark is cleared and the hold is
//     tried again on the next pass. No seat is released.
//
// At most MaxConcurrentCalls holds are worked at once; each makes its
// provider calls one after another, so no more than that many provider calls
// run at the same time.
//
// Holds of the old path (no hold_token_hash) are never candidates: their
// provider page already expired before their own deadline, and the
// reservation sweep releases them exactly as before.
package holdclose

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// JobType is the worker_jobs.job_type of the page-closing job.
const JobType = "hold.close_payment"

// DefaultInterval is the gap between runs of hold.close_payment.
const DefaultInterval = 15 * time.Second

// DefaultBatchSize caps the holds one run looks at.
const DefaultBatchSize = 100

// MaxConcurrentCalls is how many holds (and so provider calls) are worked at
// the same time.
const MaxConcurrentCalls = 10

// InProgressRetry is how long a hold whose payment is being confirmed waits
// before it is tried again.
const InProgressRetry = time.Minute

// StaleClaim is the age after which a closing mark is taken for the trace of
// a crashed run: the hold becomes a candidate again.
const StaleClaim = 5 * time.Minute

// Outcomes of arena_hold_stripe_expire_total.
const (
	OutcomeClosed         = "closed"
	OutcomeAlreadyExpired = "already_expired"
	OutcomeComplete       = "complete"
	OutcomeInProgress     = "in_progress"
	OutcomeOpen           = "open"
	OutcomeError          = "error"
)

// FailureCodeSessionExpired is the failure_code of an attempt whose page this
// job closed: the same the provider's checkout.session.expired webhook writes.
const FailureCodeSessionExpired = "session_expired"

// PageCloser reads and closes provider checkout sessions with the
// organization's own module keys. *hfeed.StripePaymentStarter satisfies it.
type PageCloser interface {
	ReadCheckoutSessionPayment(ctx context.Context, orgID, channelID uuid.UUID, provider, sessionID string) (payments.CheckoutSessionPayment, error)
	ExpireEmbeddedCheckout(ctx context.Context, orgID, channelID uuid.UUID, provider, sessionID string) error
}

// Store is the database side of the job. *PGStore is the production one.
type Store interface {
	// ListCandidates lists holds to close, oldest expires_at first.
	ListCandidates(ctx context.Context, providers []string, staleBefore time.Time, limit int32) ([]gen.HoldCloseCandidate, error)
	// Claim is step 1. ok=false: skip the hold.
	Claim(ctx context.Context, reservationID uuid.UUID, staleBefore time.Time) (mark, expiresAt time.Time, ok bool, err error)
	// ClearMark takes the claim back.
	ClearMark(ctx context.Context, reservationID uuid.UUID, mark time.Time) error
	// OpenIntents lists the hold's open attempts with a closable page.
	OpenIntents(ctx context.Context, reservationID uuid.UUID, providers []string) ([]gen.OpenPageIntent, error)
	// Release is step 5. released=false: the hold moved on (converted,
	// cancelled, mark cleared) and nothing was written.
	Release(ctx context.Context, reservationID uuid.UUID, mark time.Time, intentIDs []uuid.UUID) (released bool, err error)
}

// Stats is what one run did.
type Stats struct {
	Examined   int
	Released   int
	InProgress int
	Complete   int
	Retry      int
	Skipped    int
	Failed     int
}

// ProcessorOptions configures NewProcessor.
type ProcessorOptions struct {
	Store  Store
	Closer PageCloser
	// Providers are the payment modules whose page can be closed.
	Providers []string
	Metrics   *Metrics
	Logger    *slog.Logger
	BatchSize int32
	// Now is injectable for tests; time.Now by default.
	Now func() time.Time
}

// Processor runs hold.close_payment passes. It keeps, between passes, the
// holds whose payment was being confirmed so they wait InProgressRetry.
type Processor struct {
	opts ProcessorOptions

	mu      sync.Mutex
	backoff map[uuid.UUID]time.Time
}

// NewProcessor builds a Processor.
func NewProcessor(opts ProcessorOptions) *Processor {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Processor{opts: opts, backoff: map[uuid.UUID]time.Time{}}
}

// verdict is the decision about one open page.
type verdict int

const (
	verdictClosed verdict = iota
	verdictComplete
	verdictInProgress
	verdictRetry
)

// Run is one pass.
func (p *Processor) Run(ctx context.Context) (Stats, error) {
	var st Stats
	if p.opts.Store == nil || p.opts.Closer == nil || len(p.opts.Providers) == 0 {
		return st, nil
	}
	now := p.opts.Now()
	cands, err := p.opts.Store.ListCandidates(ctx, p.opts.Providers, now.Add(-StaleClaim), p.opts.BatchSize)
	if err != nil {
		return st, fmt.Errorf("holdclose: list candidates: %w", err)
	}

	var (
		wg  sync.WaitGroup
		smu sync.Mutex
		sem = make(chan struct{}, MaxConcurrentCalls)
	)
	for _, c := range cands {
		if p.waiting(c.ReservationID, now) {
			st.Skipped++
			continue
		}
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(c gen.HoldCloseCandidate) {
			defer wg.Done()
			defer func() { <-sem }()
			one := p.processOne(ctx, c)
			smu.Lock()
			st.Examined += one.Examined
			st.Released += one.Released
			st.InProgress += one.InProgress
			st.Complete += one.Complete
			st.Retry += one.Retry
			st.Skipped += one.Skipped
			st.Failed += one.Failed
			smu.Unlock()
		}(c)
	}
	wg.Wait()
	p.pruneBackoff(now)
	return st, ctx.Err()
}

func (p *Processor) waiting(id uuid.UUID, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.backoff[id]
	return ok && now.Before(until)
}

func (p *Processor) wait(id uuid.UUID, until time.Time) {
	p.mu.Lock()
	p.backoff[id] = until
	p.mu.Unlock()
}

func (p *Processor) pruneBackoff(now time.Time) {
	p.mu.Lock()
	for id, until := range p.backoff {
		if !now.Before(until) {
			delete(p.backoff, id)
		}
	}
	p.mu.Unlock()
}

// processOne runs steps 1 to 6 for one hold.
func (p *Processor) processOne(ctx context.Context, c gen.HoldCloseCandidate) Stats {
	var st Stats
	log := p.opts.Logger.With(slog.String("reservation_id", c.ReservationID.String()))
	now := p.opts.Now()

	// 1. Claim.
	mark, expiresAt, ok, err := p.opts.Store.Claim(ctx, c.ReservationID, now.Add(-StaleClaim))
	if err != nil {
		log.Error("hold close: claim failed", slog.String("error", err.Error()))
		st.Failed++
		return st
	}
	if !ok {
		st.Skipped++
		return st
	}
	st.Examined++
	clearMark := func() {
		if err := p.opts.Store.ClearMark(context.WithoutCancel(ctx), c.ReservationID, mark); err != nil {
			log.Error("hold close: clearing the closing mark failed", slog.String("error", err.Error()))
		}
	}

	intents, err := p.opts.Store.OpenIntents(ctx, c.ReservationID, p.opts.Providers)
	if err != nil {
		log.Error("hold close: open attempts could not be listed", slog.String("error", err.Error()))
		clearMark()
		st.Failed++
		return st
	}
	if len(intents) == 0 {
		// The attempts ended meanwhile (the webhook failed them): nothing to
		// close, the reservation sweep releases the hold.
		clearMark()
		st.Skipped++
		return st
	}

	// 2 to 4, page by page, outside any transaction.
	ids := make([]uuid.UUID, 0, len(intents))
	for _, in := range intents {
		switch p.decide(ctx, log, in) {
		case verdictClosed:
			ids = append(ids, in.PaymentIntentID)
			continue
		case verdictComplete:
			// Paid: the webhook issues the tickets.
			clearMark()
			st.Complete++
			return st
		case verdictInProgress:
			clearMark()
			p.wait(c.ReservationID, p.opts.Now().Add(InProgressRetry))
			st.InProgress++
			return st
		default:
			clearMark()
			st.Retry++
			return st
		}
	}

	// 5. Every page is closed: release in one transaction.
	released, err := p.opts.Store.Release(context.WithoutCancel(ctx), c.ReservationID, mark, ids)
	if err != nil {
		// The pages are closed already: the mark stays so no extension or
		// "paying" lands on a hold nobody can pay any more; it goes stale
		// after StaleClaim and the next pass releases the hold.
		log.Error("hold close: releasing the hold failed", slog.String("error", err.Error()))
		st.Failed++
		return st
	}
	if !released {
		st.Skipped++
		return st
	}
	if p.opts.Metrics != nil {
		p.opts.Metrics.ObserveLag(p.opts.Now().Sub(expiresAt))
	}
	log.Info("hold close: the payment page was closed and the seats released",
		slog.Time("expires_at", expiresAt), slog.Int("pages", len(ids)))
	st.Released++
	return st
}

// decide reads the page, closes it when it may be closed, and says what
// became of it.
func (p *Processor) decide(ctx context.Context, log *slog.Logger, in gen.OpenPageIntent) verdict {
	log = log.With(slog.String("payment_intent_id", in.PaymentIntentID.String()))
	sess, err := p.opts.Closer.ReadCheckoutSessionPayment(ctx, in.OrgID, in.ChannelID, in.Provider, in.ProviderSessionID)
	if err != nil {
		log.Warn("hold close: the provider did not answer the read", slog.String("error", err.Error()))
		p.count(OutcomeError)
		return verdictRetry
	}
	if v, done := p.byStatus(sess); done {
		return v
	}

	// 3. Close the open page.
	expErr := p.opts.Closer.ExpireEmbeddedCheckout(ctx, in.OrgID, in.ChannelID, in.Provider, in.ProviderSessionID)
	if expErr == nil {
		p.count(OutcomeClosed)
		return verdictClosed
	}
	if !errors.Is(expErr, payments.ErrCheckoutSessionNotExpirable) {
		// 6. No answer: try again on the next pass.
		log.Warn("hold close: the provider did not confirm the expiry", slog.String("error", expErr.Error()))
		p.count(OutcomeError)
		return verdictRetry
	}

	// 4. Refused: read again and decide by the session.
	sess, err = p.opts.Closer.ReadCheckoutSessionPayment(ctx, in.OrgID, in.ChannelID, in.Provider, in.ProviderSessionID)
	if err != nil {
		log.Warn("hold close: the provider did not answer the read after a refusal", slog.String("error", err.Error()))
		p.count(OutcomeError)
		return verdictRetry
	}
	if v, done := p.byStatus(sess); done {
		return v
	}
	p.count(OutcomeOpen)
	return verdictRetry
}

// byStatus decides by a session read: done=false means the page is open with
// no payment being confirmed.
func (p *Processor) byStatus(sess payments.CheckoutSessionPayment) (verdict, bool) {
	switch sess.Status {
	case payments.EmbeddedSessionExpired:
		p.count(OutcomeAlreadyExpired)
		return verdictClosed, true
	case payments.EmbeddedSessionComplete:
		p.count(OutcomeComplete)
		return verdictComplete, true
	}
	if sess.PaymentInProgress() {
		p.count(OutcomeInProgress)
		return verdictInProgress, true
	}
	return verdictRetry, false
}

func (p *Processor) count(outcome string) {
	if p.opts.Metrics != nil {
		p.opts.Metrics.CountExpire(outcome)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler and scheduling
// ─────────────────────────────────────────────────────────────────────────────

// Scheduler enqueues the next run.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler is the worker_jobs-backed Scheduler of one job type.
type PGScheduler struct {
	pool    *pgxpool.Pool
	jobType string
}

// NewPGScheduler builds the Scheduler of jobType.
func NewPGScheduler(pool *pgxpool.Pool, jobType string) *PGScheduler {
	return &PGScheduler{pool: pool, jobType: jobType}
}

// ScheduleNext inserts a pending worker_jobs row due at at.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)
	`
	if _, err := s.pool.Exec(ctx, q, s.jobType, at); err != nil {
		return fmt.Errorf("holdclose: schedule next %s: %w", s.jobType, err)
	}
	return nil
}

// ScheduleInitialJob enqueues the first run of jobType unless one is already
// pending or claimed.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool, jobType string) error {
	const checkSQL = `
		SELECT count(*) FROM worker_jobs
		 WHERE job_type = $1 AND status IN ('pending', 'claimed')
	`
	var cnt int64
	if err := pool.QueryRow(ctx, checkSQL, jobType).Scan(&cnt); err != nil {
		return fmt.Errorf("holdclose: check initial %s job: %w", jobType, err)
	}
	if cnt > 0 {
		return nil
	}
	const insertSQL = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())
	`
	if _, err := pool.Exec(ctx, insertSQL, jobType); err != nil {
		return fmt.Errorf("holdclose: enqueue initial %s job: %w", jobType, err)
	}
	return nil
}

// HandlerOptions configures NewHandler.
type HandlerOptions struct {
	Processor *Processor
	Scheduler Scheduler
	Interval  time.Duration
	Logger    *slog.Logger
}

// NewHandler returns the handler of JobType (a worker.HandlerFunc).
func NewHandler(opts HandlerOptions) func(ctx context.Context, payload []byte) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	return func(ctx context.Context, _ []byte) error {
		st, err := opts.Processor.Run(ctx)
		if err != nil {
			// A failed pass still schedules the next one: the cadence must
			// not die with one database error.
			opts.Logger.Error("hold close pass failed", slog.String("error", err.Error()))
		}
		if opts.Scheduler != nil {
			if serr := opts.Scheduler.ScheduleNext(ctx, time.Now().Add(opts.Interval)); serr != nil {
				return fmt.Errorf("holdclose: schedule next run: %w", serr)
			}
		}
		if st.Examined > 0 || st.Failed > 0 {
			opts.Logger.Info("hold close pass complete",
				"examined", st.Examined, "released", st.Released, "in_progress", st.InProgress,
				"complete", st.Complete, "retry", st.Retry, "skipped", st.Skipped, "failed", st.Failed)
		}
		return nil
	}
}
