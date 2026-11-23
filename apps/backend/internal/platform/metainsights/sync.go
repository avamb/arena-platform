package metainsights

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
)

// Short codes stored in meta_insights_state.last_error besides the Graph
// codes of Classify.
const (
	CodeNotConnected     = "not_connected"
	CodeNoAdAccount      = "no_ad_account"
	CodeTokenUnreadable  = "token_unreadable"
	CodeGraphRateLimited = "graph_rate_limited"
)

// Announcer posts a text once per key to the organization's chats that hear
// a kind; *salesnotify.Dispatcher (AnnounceKindToOrg) is the production one.
type Announcer interface {
	AnnounceKindToOrg(ctx context.Context, orgID, kind, key, text string) (chats, delivered int, err error)
}

// Options configures a Syncer.
type Options struct {
	Pool      *pgxpool.Pool
	Box       *secretbox.Box
	Client    *Client
	Announcer Announcer
	Logger    *slog.Logger
	// Every is META_INSIGHTS_EVERY (0 = nightly only).
	Every time.Duration
	// SweepInterval is how often the sweep runs (DefaultSweepInterval).
	SweepInterval time.Duration
}

// Syncer runs the insights sync of one organization and the sweep.
type Syncer struct {
	pool      *pgxpool.Pool
	box       *secretbox.Box
	client    *Client
	announcer Announcer
	logger    *slog.Logger
	every     time.Duration
	interval  time.Duration
}

// New builds a Syncer. A nil Client reads the real Graph host.
func New(o Options) *Syncer {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Client == nil {
		o.Client = NewClient("", "", nil)
	}
	if o.SweepInterval <= 0 {
		o.SweepInterval = DefaultSweepInterval
	}
	return &Syncer{pool: o.Pool, box: o.Box, client: o.Client, announcer: o.Announcer, logger: o.Logger, every: o.Every, interval: o.SweepInterval}
}

// Result is what one run did.
type Result struct {
	// Mode is the effective mode (backfill when the history had to be read
	// again).
	Mode         string
	Since, Until string
	Rows         int
	Deleted      int64
	Campaigns    int
	// ErrorCode is "" on success, else the code stored in last_error.
	ErrorCode string
	// SummarySent: the daily summary went to at least one chat.
	SummarySent bool
}

// PlanRange picks the effective mode and the date range (account timezone,
// both ends included) of a run: 90 days ending today when the selected ad
// account is not the one of the stored history or the backfill never
// finished; otherwise 3 days for a manual run and 7 days for any other.
// today is the account's local date (only its year, month and day count).
func PlanRange(st State, selectedAccount, mode string, today time.Time) (effMode string, since, until time.Time) {
	until = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	days := ScheduledDays
	switch {
	case st.AdAccountID != selectedAccount || st.BackfillDoneAt == nil:
		mode, days = ModeBackfill, BackfillDays
	case mode == ModeManual:
		days = ManualDays
	case mode == ModeInterval:
	default:
		mode = ModeScheduled
	}
	return mode, until.AddDate(0, 0, -(days - 1)), until
}

// accountLocation is the ad account's timezone, UTC when unknown.
func accountLocation(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

type connInfo struct {
	status   string
	sealed   *string
	account  string
	currency string
	timezone string
}

func (s *Syncer) loadConn(ctx context.Context, orgID uuid.UUID) (connInfo, bool, error) {
	var c connInfo
	err := s.pool.QueryRow(ctx, `
		SELECT c.status, c.access_token_enc, COALESCE(a.meta_id, ''), COALESCE(a.currency, ''), COALESCE(a.timezone, '')
		  FROM org_meta_connections c
		  LEFT JOIN org_meta_assets a ON a.org_id = c.org_id AND a.kind = 'ad_account' AND a.selected
		 WHERE c.org_id = $1`, orgID).Scan(&c.status, &c.sealed, &c.account, &c.currency, &c.timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return connInfo{}, false, nil
	}
	if err != nil {
		return connInfo{}, false, err
	}
	return c, true, nil
}

// setError records a run that ended without data and counts it as one more
// consecutive failure (the back-off of Due).
func (s *Syncer) setError(ctx context.Context, orgID uuid.UUID, now time.Time, code string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO meta_insights_state (org_id, last_run_at, last_error, failures, updated_at)
		VALUES ($1, $2, $3, 1, now())
		ON CONFLICT (org_id) DO UPDATE
		   SET last_run_at = EXCLUDED.last_run_at, last_error = EXCLUDED.last_error,
		       failures = meta_insights_state.failures + 1, updated_at = now()`,
		orgID, now.UTC(), code)
	return err
}

// Run reads the insights of orgID and stores them. It needs an ACTIVE
// connection with a token and a selected ad account, else no Graph call is
// made and last_error is not_connected or no_ad_account. A Graph error stops
// the run: 190 moves the connection to error and wipes its token, 10/100/
// 200-299 record the code on the connection, a throttling answer is stored
// as graph_rate_limited; the next sweep tries again. Graph failures are in
// Result.ErrorCode, never in err: err is a database failure (the job is
// retried). now is the run time (injectable for tests).
func (s *Syncer) Run(ctx context.Context, orgID uuid.UUID, mode string, now time.Time) (Result, error) {
	conn, found, err := s.loadConn(ctx, orgID)
	if err != nil {
		return Result{}, err
	}
	if !found || conn.status != metaconnect.StatusActive || conn.sealed == nil || *conn.sealed == "" {
		return Result{ErrorCode: CodeNotConnected}, s.setError(ctx, orgID, now, CodeNotConnected)
	}
	if conn.account == "" {
		return Result{ErrorCode: CodeNoAdAccount}, s.setError(ctx, orgID, now, CodeNoAdAccount)
	}
	if s.box == nil {
		return Result{ErrorCode: CodeTokenUnreadable}, s.setError(ctx, orgID, now, CodeTokenUnreadable)
	}
	token, err := metaconnect.OpenToken(s.box, orgID, *conn.sealed)
	if err != nil {
		s.logger.Warn("metainsights: the Meta token does not open", "org_id", orgID.String())
		return Result{ErrorCode: CodeTokenUnreadable}, s.setError(ctx, orgID, now, CodeTokenUnreadable)
	}
	st, err := LoadState(ctx, s.pool, orgID)
	if err != nil {
		return Result{}, err
	}
	loc := accountLocation(conn.timezone)
	effMode, since, until := PlanRange(st, conn.account, mode, now.In(loc))
	res := Result{Mode: effMode, Since: since.Format("2006-01-02"), Until: until.Format("2006-01-02")} // allow:timeformat: calendar dates

	rec := func(endpoint string, failed bool) {
		if err := recordCall(ctx, s.pool, orgID, now, endpoint, failed); err != nil {
			s.logger.Warn("metainsights: could not count a Graph call", "org_id", orgID.String(), "error", err.Error())
		}
	}
	campaigns, err := s.client.Campaigns(ctx, token, conn.account, rec)
	if err != nil {
		return s.graphFailed(ctx, orgID, now, res, err)
	}
	rows, err := s.client.Insights(ctx, token, conn.account, res.Since, res.Until, conn.currency, rec)
	if err != nil {
		return s.graphFailed(ctx, orgID, now, res, err)
	}
	res.Rows, res.Campaigns = len(rows), len(campaigns)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	deleted, err := writeRun(ctx, tx, orgID, conn.account, since, until, rows, campaigns)
	if err != nil {
		return res, err
	}
	res.Deleted = deleted
	backfilled := effMode == ModeBackfill
	if _, err := tx.Exec(ctx, `
		INSERT INTO meta_insights_state (org_id, ad_account_id, backfill_done_at, last_run_at, last_success_at, last_error, failures, updated_at)
		VALUES ($1, $2, CASE WHEN $3 THEN $4::timestamptz END, $4, $4, NULL, 0, now())
		ON CONFLICT (org_id) DO UPDATE
		   SET ad_account_id    = EXCLUDED.ad_account_id,
		       backfill_done_at = CASE WHEN $3 THEN $4::timestamptz ELSE meta_insights_state.backfill_done_at END,
		       last_run_at      = $4,
		       last_success_at  = $4,
		       last_error       = NULL,
		       failures         = 0,
		       updated_at       = now()`,
		orgID, conn.account, backfilled, now.UTC()); err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}
	s.logger.Info("metainsights: insights stored", "org_id", orgID.String(), "mode", effMode,
		"since", res.Since, "until", res.Until, "rows", res.Rows, "deleted", res.Deleted, "campaigns", res.Campaigns)

	if mode == ModeScheduled && s.announcer != nil {
		sent, err := s.sendSummary(ctx, orgID, conn.account, conn.currency, loc, now)
		if err != nil {
			s.logger.Warn("metainsights: daily summary failed", "org_id", orgID.String(), "error", err.Error())
		}
		res.SummarySent = sent
	}
	return res, nil
}

// graphFailed handles a failed Graph call of a run (see Run).
func (s *Syncer) graphFailed(ctx context.Context, orgID uuid.UUID, now time.Time, res Result, gerr error) (Result, error) {
	class, code := Classify(gerr)
	res.ErrorCode = code
	switch class {
	case ClassTokenInvalid:
		changed, err := metaconnect.MarkTokenInvalid(ctx, s.pool, orgID, code)
		if err != nil {
			return res, err
		}
		if changed {
			s.logger.Warn("metainsights: the Meta connection token is invalid, the connection is now in error", "org_id", orgID.String(), "code", code)
		}
	case ClassPermission:
		if err := metaconnect.RecordError(ctx, s.pool, orgID, code); err != nil {
			return res, err
		}
		s.logger.Warn("metainsights: no access to the ad account", "org_id", orgID.String(), "code", code)
	default:
		s.logger.Warn("metainsights: Graph call failed, the next sweep tries again", "org_id", orgID.String(), "code", code)
	}
	return res, s.setError(ctx, orgID, now, code)
}

// writeRun upserts the rows and campaigns of a run and deletes the rows of
// the same ad account and range that Meta no longer returned (so a day that
// went to zero disappears). It returns how many rows were deleted.
func writeRun(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, account string, since, until time.Time, rows []InsightRow, campaigns []Campaign) (int64, error) {
	b := &pgx.Batch{}
	for _, r := range rows {
		b.Queue(`
			INSERT INTO ad_spend_daily (org_id, ad_account_id, day, campaign_id, campaign_name, adset_id, adset_name,
			       ad_id, ad_name, currency, spend_minor, impressions, clicks, link_clicks, meta_purchases,
			       meta_purchase_value_minor, fetched_at)
			VALUES ($1, $2, $3::date, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, now())
			ON CONFLICT (org_id, ad_id, day) DO UPDATE
			   SET ad_account_id = EXCLUDED.ad_account_id, campaign_id = EXCLUDED.campaign_id,
			       campaign_name = EXCLUDED.campaign_name, adset_id = EXCLUDED.adset_id,
			       adset_name = EXCLUDED.adset_name, ad_name = EXCLUDED.ad_name, currency = EXCLUDED.currency,
			       spend_minor = EXCLUDED.spend_minor, impressions = EXCLUDED.impressions,
			       clicks = EXCLUDED.clicks, link_clicks = EXCLUDED.link_clicks,
			       meta_purchases = EXCLUDED.meta_purchases,
			       meta_purchase_value_minor = EXCLUDED.meta_purchase_value_minor, fetched_at = now()`,
			orgID, account, r.Day, r.CampaignID, r.CampaignName, r.AdsetID, r.AdsetName, r.AdID, r.AdName,
			r.Currency, r.SpendMinor, r.Impressions, r.Clicks, r.LinkClicks, r.Purchases, r.PurchaseValueMinor)
	}
	for _, c := range campaigns {
		b.Queue(`
			INSERT INTO ad_campaigns (org_id, campaign_id, ad_account_id, name, effective_status, objective, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, now())
			ON CONFLICT (org_id, campaign_id) DO UPDATE
			   SET ad_account_id = EXCLUDED.ad_account_id, name = EXCLUDED.name,
			       effective_status = EXCLUDED.effective_status, objective = EXCLUDED.objective, updated_at = now()`,
			orgID, c.ID, account, c.Name, c.EffectiveStatus, c.Objective)
	}
	if b.Len() > 0 {
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return 0, err
		}
	}
	// now() is the transaction start: every row written above carries it.
	tag, err := tx.Exec(ctx, `
		DELETE FROM ad_spend_daily
		 WHERE org_id = $1 AND ad_account_id = $2 AND day BETWEEN $3::date AND $4::date AND fetched_at < now()`,
		orgID, account, since.Format("2006-01-02"), until.Format("2006-01-02")) // allow:timeformat: calendar dates
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
