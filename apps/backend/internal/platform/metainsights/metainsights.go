// Package metainsights reads the Meta ad spend of an organization (spec
// 08_architecture/66 section 5, stage M2): the Marketing API insights at ad
// level, one row per ad and day, and the campaigns of the selected ad
// account, with the token of the organization's Meta connection
// (internal/platform/metaconnect, sealed by secretbox). The rows live in
// ad_spend_daily and ad_campaigns (migration 0220), the sync state in
// meta_insights_state, the call volume in meta_graph_calls.
//
// Two worker jobs run it: meta.insights_sweep every 10 minutes (database
// only) queues meta.insights_sync for every organization that is due; a
// manual refresh (EnqueueRefresh) queues one too, at most every 10 minutes.
// After a successful nightly run the organization's sales chats that hear
// salesnotify.KindAds get a summary of yesterday, once.
//
// Nothing here logs or stores a token, a Graph URL or Meta's message text:
// errors are short codes ("graph_190", "graph_rate_limited"...).
package metainsights

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
)

// URLParametersTemplate is the "URL parameters" an organizer pastes into an
// ad in Meta Ads Manager, so an order is tied back to its campaign, ad set
// and ad (utm_campaign, utm_term, utm_content).
const URLParametersTemplate = "utm_source=facebook&utm_medium=paid_social&utm_campaign={{campaign.id}}&utm_term={{adset.id}}&utm_content={{ad.id}}"

// Worker job types.
const (
	// SweepJobType is the self-scheduling job that queues the due syncs.
	SweepJobType = "meta.insights_sweep"
	// SyncJobType reads one organization; payload {"org_id","mode"}.
	SyncJobType = "meta.insights_sync"
)

// Run modes. The range of a run follows its mode; a run whose ad account
// changed or that never finished a backfill always reads 90 days.
const (
	ModeBackfill  = "backfill"  // 90 days
	ModeScheduled = "scheduled" // the nightly run, 7 days, then the summary
	ModeInterval  = "interval"  // META_INSIGHTS_EVERY, 7 days, no summary
	ModeManual    = "manual"    // a refresh asked for by the organizer, 3 days
)

// Range lengths in days, today included.
const (
	BackfillDays  = 90
	ScheduledDays = 7
	ManualDays    = 3
)

// RefreshThrottle is how long a manual refresh blocks the next one.
const RefreshThrottle = 10 * time.Minute

// Querier is what this package needs of a pool or a transaction; gen.DBTX,
// *pgxpool.Pool and pgx.Tx all satisfy it.
type Querier = metaconnect.Querier

// State is the insights sync state of an organization (meta_insights_state).
// The zero State means the organization was never synced.
type State struct {
	// AdAccountID is the account ("act_<n>") the stored history belongs to.
	AdAccountID string
	// BackfillDoneAt is nil until a 90-day backfill of AdAccountID finished.
	BackfillDoneAt *time.Time
	LastRunAt      *time.Time
	LastSuccessAt  *time.Time
	// LastError is a short code ("" after a success), never a token or
	// Meta's message.
	LastError string
	// ManualNotBefore is the earliest time of the next manual refresh.
	ManualNotBefore *time.Time
}

// LoadState reads the sync state of orgID; an organization without a row
// gets the zero State and no error.
func LoadState(ctx context.Context, q Querier, orgID uuid.UUID) (State, error) {
	var s State
	var acct, lastErr *string
	err := q.QueryRow(ctx, `
		SELECT ad_account_id, backfill_done_at, last_run_at, last_success_at, last_error, manual_not_before
		  FROM meta_insights_state WHERE org_id = $1`, orgID).
		Scan(&acct, &s.BackfillDoneAt, &s.LastRunAt, &s.LastSuccessAt, &lastErr, &s.ManualNotBefore)
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	if acct != nil {
		s.AdAccountID = *acct
	}
	if lastErr != nil {
		s.LastError = *lastErr
	}
	return s, nil
}

// syncPayload is the payload of SyncJobType.
type syncPayload struct {
	OrgID string `json:"org_id"`
	Mode  string `json:"mode"`
}

// enqueueSync queues a sync of orgID unless one is already pending or
// claimed for it. It reports whether a row was added.
func enqueueSync(ctx context.Context, q Querier, orgID uuid.UUID, mode string) (bool, error) {
	body, err := json.Marshal(syncPayload{OrgID: orgID.String(), Mode: mode})
	if err != nil {
		return false, err
	}
	tag, err := q.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, $2::jsonb, 3, 'pending', now()
		 WHERE NOT EXISTS (
		       SELECT 1 FROM worker_jobs
		        WHERE job_type = $1 AND status IN ('pending', 'claimed')
		          AND payload ->> 'org_id' = $3)`, SyncJobType, body, orgID.String())
	if err != nil {
		return false, fmt.Errorf("metainsights: enqueue sync: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// EnqueueRefresh queues a manual sync of orgID (mode manual, the last 3
// days, or 90 days when the backfill is still due). At most one refresh per
// RefreshThrottle: inside the throttle nothing is queued, queued is false and
// notBefore is when the next refresh is allowed. When a sync of the
// organization is already waiting, the refresh joins it (queued true). The
// caller checks the connection and the permission; q may be a transaction.
func EnqueueRefresh(ctx context.Context, q Querier, orgID uuid.UUID, now time.Time) (queued bool, notBefore time.Time, err error) {
	next := now.UTC().Add(RefreshThrottle)
	var got time.Time
	err = q.QueryRow(ctx, `
		INSERT INTO meta_insights_state (org_id, manual_not_before, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (org_id) DO UPDATE
		   SET manual_not_before = EXCLUDED.manual_not_before, updated_at = now()
		 WHERE meta_insights_state.manual_not_before IS NULL
		    OR meta_insights_state.manual_not_before <= $3
		RETURNING manual_not_before`, orgID, next, now.UTC()).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		var nb *time.Time
		if err := q.QueryRow(ctx, `SELECT manual_not_before FROM meta_insights_state WHERE org_id = $1`, orgID).Scan(&nb); err != nil {
			return false, time.Time{}, err
		}
		if nb != nil {
			notBefore = nb.UTC()
		}
		return false, notBefore, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	if _, err := enqueueSync(ctx, q, orgID, ModeManual); err != nil {
		return false, time.Time{}, err
	}
	return true, got.UTC(), nil
}

// CallVolume sums the Graph calls and failed calls of orgID from the UTC day
// of since on (meta_graph_calls), for the Access Tier counters (500 calls in
// 15 days, errors under 15 %).
func CallVolume(ctx context.Context, q Querier, orgID uuid.UUID, since time.Time) (calls, errs int64, err error) {
	err = q.QueryRow(ctx, `
		SELECT COALESCE(sum(calls), 0)::bigint, COALESCE(sum(errors), 0)::bigint
		  FROM meta_graph_calls WHERE org_id = $1 AND day >= $2::date`,
		orgID, since.UTC().Format("2006-01-02")).Scan(&calls, &errs)
	return calls, errs, err
}

// DeleteOrgData removes the Meta ad data of orgID: ad_spend_daily,
// ad_campaigns and meta_insights_state. Only Meta's data-deletion callback
// calls it; a disconnect keeps the spend history (the organizer's own
// report). meta_graph_calls holds no Meta data and stays.
func DeleteOrgData(ctx context.Context, q Querier, orgID uuid.UUID) error {
	for _, sql := range []string{
		`DELETE FROM ad_spend_daily WHERE org_id = $1`,
		`DELETE FROM ad_campaigns WHERE org_id = $1`,
		`DELETE FROM meta_insights_state WHERE org_id = $1`,
	} {
		if _, err := q.Exec(ctx, sql, orgID); err != nil {
			return err
		}
	}
	return nil
}

// recordCall counts one Graph request of orgID on the UTC day of at.
func recordCall(ctx context.Context, q Querier, orgID uuid.UUID, at time.Time, endpoint string, failed bool) error {
	e := 0
	if failed {
		e = 1
	}
	_, err := q.Exec(ctx, `
		INSERT INTO meta_graph_calls (day, org_id, endpoint, calls, errors)
		VALUES ($1::date, $2, $3, 1, $4)
		ON CONFLICT (day, org_id, endpoint) DO UPDATE
		   SET calls = meta_graph_calls.calls + 1, errors = meta_graph_calls.errors + EXCLUDED.errors`,
		at.UTC().Format("2006-01-02"), orgID, endpoint, e)
	return err
}
