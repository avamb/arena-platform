package opswatchdog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGAlertStore is the production AlertStore backed by the ops_alerts table
// (migration 0104). It is read-write ONLY on that watchdog-owned table —
// never on any business table.
type PGAlertStore struct {
	pool *pgxpool.Pool
}

// NewPGAlertStore wraps a pgx pool into a PGAlertStore.
func NewPGAlertStore(pool *pgxpool.Pool) *PGAlertStore {
	return &PGAlertStore{pool: pool}
}

func (s *PGAlertStore) Get(ctx context.Context, fingerprint string) (*AlertRow, error) {
	const q = `
		SELECT fingerprint, severity, title, details, first_seen_at, last_seen_at,
		       last_notified_at, resolved_at
		  FROM ops_alerts
		 WHERE fingerprint = $1
	`
	var row AlertRow
	var detailsJSON []byte
	err := s.pool.QueryRow(ctx, q, fingerprint).Scan(
		&row.Fingerprint, &row.Severity, &row.Title, &detailsJSON,
		&row.FirstSeenAt, &row.LastSeenAt, &row.LastNotifiedAt, &row.ResolvedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opswatchdog: query ops_alerts: %w", err)
	}
	if len(detailsJSON) > 0 {
		if uerr := json.Unmarshal(detailsJSON, &row.Details); uerr != nil {
			return nil, fmt.Errorf("opswatchdog: decode ops_alerts.details: %w", uerr)
		}
	}
	return &row, nil
}

func (s *PGAlertStore) Upsert(ctx context.Context, row AlertRow) error {
	detailsJSON, err := json.Marshal(row.Details)
	if err != nil {
		return fmt.Errorf("opswatchdog: encode alert details: %w", err)
	}
	const q = `
		INSERT INTO ops_alerts
		    (fingerprint, severity, title, details, first_seen_at, last_seen_at,
		     last_notified_at, resolved_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULL)
		ON CONFLICT (fingerprint) DO UPDATE SET
		    severity         = EXCLUDED.severity,
		    title            = EXCLUDED.title,
		    details          = EXCLUDED.details,
		    first_seen_at    = EXCLUDED.first_seen_at,
		    last_seen_at     = EXCLUDED.last_seen_at,
		    last_notified_at = COALESCE(EXCLUDED.last_notified_at, ops_alerts.last_notified_at),
		    resolved_at      = NULL
	`
	if _, err := s.pool.Exec(ctx, q,
		row.Fingerprint, row.Severity, row.Title, detailsJSON,
		row.FirstSeenAt, row.LastSeenAt, row.LastNotifiedAt,
	); err != nil {
		return fmt.Errorf("opswatchdog: upsert ops_alerts: %w", err)
	}
	return nil
}

func (s *PGAlertStore) MarkResolved(ctx context.Context, fingerprint string, at time.Time) error {
	const q = `
		UPDATE ops_alerts SET resolved_at = $2
		 WHERE fingerprint = $1 AND resolved_at IS NULL
	`
	if _, err := s.pool.Exec(ctx, q, fingerprint, at); err != nil {
		return fmt.Errorf("opswatchdog: mark ops_alerts resolved: %w", err)
	}
	return nil
}

func (s *PGAlertStore) ListOpenFingerprints(ctx context.Context, prefix string) ([]string, error) {
	const q = `
		SELECT fingerprint FROM ops_alerts
		 WHERE resolved_at IS NULL AND fingerprint LIKE $1
	`
	rows, err := s.pool.Query(ctx, q, prefix+":%")
	if err != nil {
		return nil, fmt.Errorf("opswatchdog: list open ops_alerts: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, fmt.Errorf("opswatchdog: scan fingerprint: %w", err)
		}
		out = append(out, fp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("opswatchdog: iterate ops_alerts: %w", err)
	}
	return out, nil
}

// Cursor is one check's dedup position: the timestamp/id of the last row it
// already processed.
type Cursor struct {
	TS *time.Time
	ID string
}

// CursorStore persists per-check cursors in ops_watchdog_state.
type CursorStore interface {
	// Get returns the stored cursor for key, or nil if no row exists yet
	// (the caller must then seed one — never replay full history).
	Get(ctx context.Context, key string) (*Cursor, error)
	// Set stores/overwrites the cursor for key.
	Set(ctx context.Context, key string, c Cursor) error
}

// PGCursorStore is the production CursorStore backed by ops_watchdog_state.
type PGCursorStore struct {
	pool *pgxpool.Pool
}

// NewPGCursorStore wraps a pgx pool into a PGCursorStore.
func NewPGCursorStore(pool *pgxpool.Pool) *PGCursorStore {
	return &PGCursorStore{pool: pool}
}

func (s *PGCursorStore) Get(ctx context.Context, key string) (*Cursor, error) {
	const q = `SELECT cursor_ts, cursor_id FROM ops_watchdog_state WHERE key = $1`
	var c Cursor
	var id *string
	err := s.pool.QueryRow(ctx, q, key).Scan(&c.TS, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opswatchdog: query ops_watchdog_state: %w", err)
	}
	if id != nil {
		c.ID = *id
	}
	return &c, nil
}

func (s *PGCursorStore) Set(ctx context.Context, key string, c Cursor) error {
	const q = `
		INSERT INTO ops_watchdog_state (key, cursor_ts, cursor_id, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (key) DO UPDATE SET
		    cursor_ts  = EXCLUDED.cursor_ts,
		    cursor_id  = EXCLUDED.cursor_id,
		    updated_at = now()
	`
	if _, err := s.pool.Exec(ctx, q, key, c.TS, c.ID); err != nil {
		return fmt.Errorf("opswatchdog: set cursor %s: %w", key, err)
	}
	return nil
}

// EnsureInitialCursors seeds a cursor_ts=now() row for every key that has no
// row yet. Called once at worker startup so the very first watchdog run
// never replays pre-existing sales/refunds history — only rows that appear
// AFTER the watchdog started exist "since the cursor".
func EnsureInitialCursors(ctx context.Context, pool *pgxpool.Pool, keys []string, now time.Time) error {
	const q = `
		INSERT INTO ops_watchdog_state (key, cursor_ts, cursor_id)
		VALUES ($1, $2, '')
		ON CONFLICT (key) DO NOTHING
	`
	for _, k := range keys {
		if _, err := pool.Exec(ctx, q, k, now); err != nil {
			return fmt.Errorf("opswatchdog: seed initial cursor %s: %w", k, err)
		}
	}
	return nil
}
