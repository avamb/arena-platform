// Package cartreminder sends ONE reminder about an unpaid widget order while
// its hold is still alive (spec 51-1 section 3, owner decisions 2026-11-02):
//
//   - The sweep (JobTypeSweep, every minute) picks orders of the widget and
//     the tickets page (source public_feed) that are unpaid DueAfter after
//     checkout/start, still have at least MinLeftAtPick of their payment
//     window, carry a buyer e-mail and an amount, and whose organization has
//     not switched reminders off. One transaction inserts the order into
//     checkout_reminders and queues JobTypeEmail; the primary key on order_id
//     is what makes a second reminder impossible.
//   - The letter (delivery.NewCartReminderHandler) checks everything again
//     and records sent_at or the reason it skipped.
//   - Nothing is sent after the hold is gone: a "come back" letter then is
//     advertising, which needs a marketing consent.
//
// The letter is about one specific open order and carries no other offer,
// which is what keeps it a service letter (GDPR art. 6(1)(b)).
package cartreminder

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Worker jobs.
const (
	JobTypeSweep = "checkout.reminder_sweep"
	JobTypeEmail = "checkout.reminder_email"
)

// The rule (owner decision В-1, 2026-11-02).
const (
	// DueAfter is how long after checkout/start an unpaid order is reminded.
	DueAfter = 15 * time.Minute
	// MinLeftAtPick is the least payment window an order must still have to
	// be picked; MinLeftAtSend the least it must have when the letter goes.
	MinLeftAtPick = 8 * time.Minute
	MinLeftAtSend = 5 * time.Minute
	// lookback bounds the sweep: an order older than this is never reminded.
	lookback = 2 * time.Hour
	// Every is the sweep's cadence; batch the most orders one pass picks.
	Every = time.Minute
	batch = 200
)

// Skip reasons stored in checkout_reminders.skipped_reason.
const (
	SkipPaid         = "paid"
	SkipNotPending   = "not_pending"
	SkipExpired      = "window_closed"
	SkipNoPayment    = "no_open_payment"
	SkipNewerOrder   = "newer_order"
	SkipDisabled     = "disabled"
	SkipNoRecipient  = "no_recipient"
	SkipSessionGone  = "session_gone"
	SkipNoSender     = "no_sender"
	SkipNoReturnPage = "no_return_page"
)

// JobPayload is the letter's payload.
type JobPayload struct {
	OrderID string `json:"order_id"`
}

// Sweep picks the due orders and queues their letters. It answers how many
// it queued.
func Sweep(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int, error) {
	return SweepOrgs(ctx, pool, now, nil)
}

// SweepOrgs is Sweep limited to the given organizations (nil = all). Tests
// use it so a pass never claims another package's orders in a shared database.
func SweepOrgs(ctx context.Context, pool *pgxpool.Pool, now time.Time, orgs []uuid.UUID) (int, error) {
	now = now.UTC()
	rows, err := pool.Query(ctx, `
		SELECT o.id, o.org_id, o.checkout_session_id
		FROM   orders o
		WHERE  o.status = 'pending_payment' AND o.source = 'public_feed'
		  AND  o.created_at <= $1 AND o.created_at > $2
		  AND  o.total > 0 AND COALESCE(btrim(o.buyer_email), '') <> ''
		  AND  o.expires_at >= $3
		  AND  NOT EXISTS (SELECT 1 FROM checkout_reminders r WHERE r.order_id = o.id)
		  AND  COALESCE((SELECT g.cart_reminder_enabled FROM org_growth_settings g WHERE g.org_id = o.org_id), true)
		  AND  ($5::uuid[] IS NULL OR o.org_id = ANY($5::uuid[]))
		ORDER  BY o.created_at
		LIMIT  $4`,
		now.Add(-DueAfter), now.Add(-lookback), now.Add(MinLeftAtPick), batch, orgs)
	if err != nil {
		return 0, err
	}
	type due struct{ order, org, checkout uuid.UUID }
	var picked []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.order, &d.org, &d.checkout); err != nil {
			rows.Close()
			return 0, err
		}
		picked = append(picked, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	queued := 0
	for _, d := range picked {
		ok, err := claim(ctx, pool, d.order, d.org, d.checkout, now)
		if err != nil {
			return queued, err
		}
		if ok {
			queued++
		}
	}
	return queued, nil
}

func claim(ctx context.Context, pool *pgxpool.Pool, orderID, orgID, checkoutID uuid.UUID, now time.Time) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		INSERT INTO checkout_reminders (order_id, org_id, checkout_session_id, queued_at)
		VALUES ($1, $2, $3, $4) ON CONFLICT (order_id) DO NOTHING`, orderID, orgID, checkoutID, now)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	body, err := json.Marshal(JobPayload{OrderID: orderID.String()})
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, $2::jsonb, 5, 'pending', $3)`, JobTypeEmail, body, now); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// scheduleNext queues the next pass unless one is already pending, so a
// retried pass never forks a second chain.
func scheduleNext(ctx context.Context, pool *pgxpool.Pool, at time.Time) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', $2
		WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`,
		JobTypeSweep, at)
	return err
}

// ScheduleInitialJob seeds the chain at worker start; a pending or claimed
// pass means it is alive and nothing is added.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', now()
		WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed'))`,
		JobTypeSweep)
	return err
}

// NewSweepHandler is the self-scheduling worker handler of JobTypeSweep. The
// next pass is queued BEFORE the work, so a failing pass never ends the chain.
func NewSweepHandler(pool *pgxpool.Pool, logger *slog.Logger) func(ctx context.Context, payload []byte) error {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, _ []byte) error {
		now := time.Now().UTC()
		if err := scheduleNext(ctx, pool, now.Add(Every)); err != nil {
			logger.Warn("cartreminder: could not schedule the next pass", slog.String("error", err.Error()))
		}
		n, err := Sweep(ctx, pool, now)
		if n > 0 {
			logger.Info("cartreminder: reminders queued", slog.Int("count", n))
		}
		return err
	}
}

// Order is what the letter needs to decide and to say.
type Order struct {
	ID, OrgID, SessionID, CheckoutID uuid.UUID
	SystemID                         int64
	Status                           string
	BuyerEmail                       string
	Locale                           string
	ExpiresAt                        *time.Time
	CreatedAt                        time.Time
	Total                            int64
	Currency                         string
	Tickets                          int
	CheckoutToken                    string
	ReturnURL                        string
	PaymentURL                       string
	NewerOrder                       bool
	ReminderOn                       bool
}

// LoadOrder reads the order the letter is about. pgx.ErrNoRows when it is gone.
func LoadOrder(ctx context.Context, db interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, orderID uuid.UUID) (Order, error) {
	var o Order
	err := db.QueryRow(ctx, `
		SELECT o.id, o.org_id, o.session_id, o.checkout_session_id, o.system_id, o.status,
		       COALESCE(o.buyer_email, ''), COALESCE(cs.buyer_locale, ''), o.expires_at, o.created_at,
		       o.total, o.currency,
		       (SELECT count(*) FROM order_items i WHERE i.order_id = o.id),
		       COALESCE(cs.checkout_token, ''), COALESCE(cs.return_url, ''),
		       COALESCE((SELECT pi.hosted_checkout_url FROM payment_intents pi
		                 WHERE pi.checkout_session_id = cs.id AND pi.state NOT IN ('succeeded', 'failed')
		                   AND COALESCE(pi.hosted_checkout_url, '') <> ''
		                 ORDER BY pi.created_at DESC LIMIT 1), ''),
		       EXISTS (SELECT 1 FROM orders n
		               WHERE n.session_id = o.session_id AND n.id <> o.id AND n.created_at > o.created_at
		                 AND lower(btrim(n.buyer_email)) = lower(btrim(o.buyer_email))
		                 AND n.status IN ('pending_payment', 'paid', 'partially_refunded')),
		       COALESCE((SELECT g.cart_reminder_enabled FROM org_growth_settings g WHERE g.org_id = o.org_id), true)
		FROM   orders o
		JOIN   checkout_sessions cs ON cs.id = o.checkout_session_id
		WHERE  o.id = $1`, orderID).Scan(
		&o.ID, &o.OrgID, &o.SessionID, &o.CheckoutID, &o.SystemID, &o.Status,
		&o.BuyerEmail, &o.Locale, &o.ExpiresAt, &o.CreatedAt, &o.Total, &o.Currency, &o.Tickets,
		&o.CheckoutToken, &o.ReturnURL, &o.PaymentURL, &o.NewerOrder, &o.ReminderOn)
	return o, err
}

// Decide answers "" when the letter may go now, else the reason to skip it.
func Decide(o Order, now time.Time) string {
	switch {
	case !o.ReminderOn:
		return SkipDisabled
	case o.Status == "paid" || o.Status == "partially_refunded" || o.Status == "refunded":
		return SkipPaid
	case o.Status != "pending_payment":
		return SkipNotPending
	case o.ExpiresAt == nil || o.ExpiresAt.Before(now.Add(MinLeftAtSend)):
		return SkipExpired
	case o.BuyerEmail == "":
		return SkipNoRecipient
	case o.PaymentURL == "":
		return SkipNoPayment
	case o.NewerOrder:
		return SkipNewerOrder
	case o.CheckoutToken == "":
		return SkipNoReturnPage
	}
	return ""
}

// Mark records the outcome of the letter: sent, or the reason it was skipped.
func Mark(ctx context.Context, db interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, orderID uuid.UUID, skipped string, now time.Time) error {
	if skipped == "" {
		_, err := db.Exec(ctx, `UPDATE checkout_reminders SET sent_at = $2 WHERE order_id = $1`, orderID, now)
		return err
	}
	_, err := db.Exec(ctx, `UPDATE checkout_reminders SET skipped_reason = $2 WHERE order_id = $1 AND sent_at IS NULL`, orderID, skipped)
	return err
}
