package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RollupJobType is the worker_jobs.job_type of the hourly processing of raw
// events into identities, sessions, profiles and flow statistics (spec 50
// section 7).
const RollupJobType = "analytics.rollup"

// RollupInterval is the cadence of the job.
const RollupInterval = time.Hour

// rollupStateKey is the analytics.state row that holds the cursor.
const rollupStateKey = "rollup"

// rollupOverlap is how far before the previous run the next one looks again.
// Every step is idempotent, so the overlap only protects against events that
// became visible late (a writer flush that committed after the run started).
const rollupOverlap = 2 * time.Hour

// rollupLockKey serializes runs (pg_advisory_xact_lock).
const rollupLockKey = 5_050_001

// RollupOptions configure a Rollup.
type RollupOptions struct {
	Pool *pgxpool.Pool
	// TelegramSecret is JWT_SIGNING_SECRET, the key of TGHasher. Empty means
	// the bot-onboarding join (tg + e-mail) is skipped.
	TelegramSecret string
	Weights        ScoreWeights
	Logger         *slog.Logger
	// Now nil means time.Now.
	Now func() time.Time
}

// Rollup is the processing job of stage A5. It reads raw events and the
// business tables, and writes only the analytics schema.
type Rollup struct {
	pool *pgxpool.Pool
	tg   *TGHasher
	w    ScoreWeights
	log  *slog.Logger
	now  func() time.Time
}

// NewRollup builds the job.
func NewRollup(o RollupOptions) *Rollup {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Weights == (ScoreWeights{}) {
		o.Weights = DefaultScoreWeights
	}
	r := &Rollup{pool: o.Pool, w: o.Weights, log: o.Logger, now: o.Now}
	if o.TelegramSecret != "" {
		r.tg = NewTGHasher(o.TelegramSecret)
	}
	return r
}

// RollupResult counts what a run did.
type RollupResult struct {
	PeopleCreated   int
	PeopleMerged    int
	People          int
	SessionsWritten int
	ProfilesWritten int
	ProfilesChanged int
	FlowRows        int
	PersonFailures  int
}

type rollupState struct {
	Through time.Time `json:"through"`
}

// Run executes one pass. It is idempotent: a second pass over the same data
// changes nothing (profiles.changed_at included).
func (r *Rollup) Run(ctx context.Context) (RollupResult, error) {
	var res RollupResult
	started := r.now()
	st, err := r.loadState(ctx)
	if err != nil {
		return res, err
	}
	since := time.Unix(0, 0).UTC()
	if !st.Through.IsZero() {
		since = st.Through.Add(-rollupOverlap)
	}

	if err := r.joinStep(ctx, since, &res); err != nil {
		return res, err
	}

	ids, err := r.listPeople(ctx)
	if err != nil {
		return res, err
	}
	res.People = len(ids)
	var firstErr error
	for _, id := range ids {
		if err := r.processPerson(ctx, id, started, &res); err != nil {
			res.PersonFailures++
			if firstErr == nil {
				firstErr = err
			}
			r.log.Warn("analytics rollup: person failed", slog.String("person_id", id.String()), slog.Any("error", err))
		}
	}

	if err := r.flowStep(ctx, since, &res); err != nil {
		return res, err
	}
	if firstErr != nil {
		// Do not advance the cursor: the next run looks at the same window.
		return res, fmt.Errorf("analytics: rollup: %d person(s) failed, first: %w", res.PersonFailures, firstErr)
	}
	if err := r.saveState(ctx, rollupState{Through: started}); err != nil {
		return res, err
	}
	return res, nil
}

func (r *Rollup) loadState(ctx context.Context) (rollupState, error) {
	var raw []byte
	err := r.pool.QueryRow(ctx, `SELECT value FROM analytics.state WHERE key = $1`, rollupStateKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return rollupState{}, nil
	}
	if err != nil {
		return rollupState{}, fmt.Errorf("analytics: load rollup state: %w", err)
	}
	var st rollupState
	_ = json.Unmarshal(raw, &st)
	return st, nil
}

func (r *Rollup) saveState(ctx context.Context, st rollupState) error {
	raw, _ := json.Marshal(st)
	_, err := r.pool.Exec(ctx, `
INSERT INTO analytics.state (key, value, updated_at) VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
		rollupStateKey, raw, r.now())
	if err != nil {
		return fmt.Errorf("analytics: save rollup state: %w", err)
	}
	return nil
}

func (r *Rollup) listPeople(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `SELECT id FROM analytics.people ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("analytics: list people: %w", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ─── step 1: joins ─────────────────────────────────────────────────────────

// joinStep gathers the pairs that say two identifiers are one person and
// applies them in a single transaction (a merge moves identities and deletes
// the younger person atomically).
func (r *Rollup) joinStep(ctx context.Context, since time.Time, res *RollupResult) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("analytics: begin join tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, rollupLockKey); err != nil {
		return fmt.Errorf("analytics: rollup lock: %w", err)
	}
	apply := func(req linkReq) error {
		lr, err := linkIdentities(ctx, tx, req)
		if err != nil {
			return err
		}
		if lr.created {
			res.PeopleCreated++
		}
		res.PeopleMerged += lr.merged
		return nil
	}
	if err := r.joinEmailClicks(ctx, tx, since, apply); err != nil {
		return err
	}
	if err := r.joinApplications(ctx, tx, apply); err != nil {
		return err
	}
	if err := r.joinEventIdentities(ctx, tx, since, apply); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// joinEmailClicks: an email_click with props.aid is the proof that cid and aid
// are one person (the server set aid itself). The cid's CRM contact comes from
// contact_codes. A click without aid still gives the contact a person.
func (r *Rollup) joinEmailClicks(ctx context.Context, tx pgx.Tx, since time.Time, apply func(linkReq) error) error {
	rows, err := tx.Query(ctx, `
SELECT e.id_value, COALESCE(e.props->>'aid', ''), min(e.at), COALESCE(cc.twenty_person_id, '')
  FROM analytics.events e
  LEFT JOIN analytics.contact_codes cc ON cc.code = e.id_value
 WHERE e.name = 'email_click' AND e.id_kind = 'cid' AND e.at >= $1 AND NOT e.suspect
 GROUP BY e.id_value, COALESCE(e.props->>'aid', ''), COALESCE(cc.twenty_person_id, '')
 ORDER BY min(e.at)`, since)
	if err != nil {
		return fmt.Errorf("analytics: read email clicks: %w", err)
	}
	var reqs []linkReq
	for rows.Next() {
		var cid, aid, twenty string
		var at time.Time
		if err := rows.Scan(&cid, &aid, &at, &twenty); err != nil {
			rows.Close()
			return err
		}
		req := linkReq{idents: []ident{{IDKindCID, cid}}, twentyID: twenty, at: at}
		if ValidAID(aid) {
			req.idents = append(req.idents, ident{IDKindAID, aid})
		}
		reqs = append(reqs, req)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, req := range reqs {
		if err := apply(req); err != nil {
			return err
		}
	}
	return nil
}

// joinApplications links the applicant: aid (site start) and e-mail, the
// Telegram hash (bot code confirmation) and the user (approval).
func (r *Rollup) joinApplications(ctx context.Context, tx pgx.Tx, apply func(linkReq) error) error {
	rows, err := tx.Query(ctx, `
SELECT a.applicant_email, COALESCE(a.aid, ''), a.status, a.telegram_user_id, a.email_confirmed_at,
       a.created_at, a.org_id
  FROM onboarding_applications a
 WHERE a.purged_at IS NULL
   AND (a.status <> 'draft' OR a.email_confirmed_at IS NOT NULL OR a.aid IS NOT NULL)
 ORDER BY a.created_at`)
	if err != nil {
		return fmt.Errorf("analytics: read applications: %w", err)
	}
	type app struct {
		email, aid, status string
		tg                 *int64
		confirmed          *time.Time
		created            time.Time
		orgID              *uuid.UUID
	}
	var apps []app
	for rows.Next() {
		var a app
		if err := rows.Scan(&a.email, &a.aid, &a.status, &a.tg, &a.confirmed, &a.created, &a.orgID); err != nil {
			rows.Close()
			return err
		}
		apps = append(apps, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range apps {
		req := linkReq{idents: []ident{{IdentityEmailSHA256, EmailSHA256(a.email)}}, at: a.created}
		if ValidAID(a.aid) {
			req.idents = append(req.idents, ident{IDKindAID, a.aid})
		}
		if a.tg != nil && a.confirmed != nil && r.tg != nil {
			req.idents = append(req.idents, ident{IDKindTG, r.tg.Hash(*a.tg)})
		}
		if a.status == "approved" {
			uid, err := approvedUser(ctx, tx, a.email, a.orgID)
			if err != nil {
				return err
			}
			if uid != nil {
				req.userID = uid
				req.idents = append(req.idents, ident{IDKindUser, uid.String()})
			}
		}
		if err := apply(req); err != nil {
			return err
		}
	}
	return nil
}

// approvedUser finds the account an approval created: the user with the
// applicant's e-mail, else the organization's owner.
func approvedUser(ctx context.Context, tx pgx.Tx, email string, orgID *uuid.UUID) (*uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM users WHERE lower(email) = lower($1)`, email).Scan(&id)
	if err == nil {
		return &id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("analytics: find approved user: %w", err)
	}
	if orgID == nil {
		return nil, nil
	}
	err = tx.QueryRow(ctx, `
SELECT user_id FROM memberships
 WHERE org_id = $1 AND role = 'org_admin' AND status = 'active'
 ORDER BY joined_at LIMIT 1`, *orgID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("analytics: find organization owner: %w", err)
	}
	return &id, nil
}

// joinEventIdentities gives every identifier that appears in recent events a
// person (a visitor who never clicked a letter or applied still has a
// history), and binds a user identifier to people.user_id.
func (r *Rollup) joinEventIdentities(ctx context.Context, tx pgx.Tx, since time.Time, apply func(linkReq) error) error {
	rows, err := tx.Query(ctx, `
SELECT e.id_kind, e.id_value, min(e.at)
  FROM analytics.events e
 WHERE e.at >= $1 AND e.id_kind IN ('aid', 'cid', 'user', 'tg') AND NOT e.suspect
   AND NOT EXISTS (SELECT 1 FROM analytics.identities i WHERE i.kind = e.id_kind AND i.value = e.id_value)
 GROUP BY e.id_kind, e.id_value
 ORDER BY min(e.at)`, since)
	if err != nil {
		return fmt.Errorf("analytics: read new identifiers: %w", err)
	}
	type seen struct {
		kind, value string
		at          time.Time
	}
	var all []seen
	for rows.Next() {
		var s seen
		if err := rows.Scan(&s.kind, &s.value, &s.at); err != nil {
			rows.Close()
			return err
		}
		all = append(all, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range all {
		req := linkReq{idents: []ident{{s.kind, s.value}}, at: s.at}
		switch s.kind {
		case IDKindCID:
			// A cid without a contact code was never confirmed by the server.
			var tw string
			err := tx.QueryRow(ctx, `SELECT twenty_person_id FROM analytics.contact_codes WHERE code = $1`, s.value).Scan(&tw)
			if err != nil {
				continue
			}
			req.twentyID = tw
		case IDKindUser:
			uid, perr := uuid.Parse(s.value)
			if perr != nil {
				continue
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, uid).Scan(&exists); err != nil {
				return err
			}
			if exists {
				req.userID = &uid
			}
		}
		if err := apply(req); err != nil {
			return err
		}
	}
	return nil
}

// ─── step 6: flow_daily ────────────────────────────────────────────────────

func (r *Rollup) flowStep(ctx context.Context, since time.Time, res *RollupResult) error {
	startDay := TruncateDay(since)
	rows, err := r.pool.Query(ctx, `
SELECT at, surface, name, id_kind || ':' || id_value, props
  FROM analytics.events
 WHERE at >= $1 AND NOT suspect
   AND name IN ('flow_step', 'ui_error', 'wizard_step', 'wizard_input_error', 'wizard_exit')
 ORDER BY at, id`, startDay.Add(-time.Hour))
	if err != nil {
		return fmt.Errorf("analytics: read flow events: %w", err)
	}
	var evs []FlowEv
	for rows.Next() {
		var e FlowEv
		var props []byte
		if err := rows.Scan(&e.At, &e.Surface, &e.Name, &e.Who, &props); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal(props, &e.Props)
		evs = append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var keep []FlowRow
	for _, row := range ComputeFlowDaily(evs) {
		if !row.Day.Before(startDay) {
			keep = append(keep, row)
		}
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("analytics: begin flow tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM analytics.flow_daily WHERE day >= $1::date`, startDay); err != nil {
		return fmt.Errorf("analytics: clear flow_daily: %w", err)
	}
	for _, row := range keep {
		var med any
		if row.MedianSeconds != nil {
			med = *row.MedianSeconds
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO analytics.flow_daily (day, surface, flow, step, entered, left_here, errors, median_seconds)
VALUES ($1::date, $2, $3, $4, $5, $6, $7, $8)`,
			row.Day, row.Surface, row.Flow, row.Step, row.Entered, row.LeftHere, row.Errors, med); err != nil {
			return fmt.Errorf("analytics: write flow_daily: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	res.FlowRows = len(keep)
	return nil
}
