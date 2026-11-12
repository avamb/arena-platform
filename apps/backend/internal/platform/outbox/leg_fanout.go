// Package outbox — LegFanOut: a named-leg fan-out with per-leg delivery state
// (finding F-FC-01, migration 0215).
//
// arena-worker hands every outbox_events row to several legs (Telegram sales
// notifications, ad conversions, the generic webhook, MACS, the WordPress
// sites, the billing accelerator). The old fan-out stopped at the first
// failing leg and returned its error, so the whole row was retried: while MACS
// answered 500 the WordPress sites and billing never received the event, and
// the legs before MACS were called again on every retry.
//
// LegFanOut calls every leg, even after one fails, and remembers per event
// which legs already delivered (outbox_event_legs). A delivered leg is never
// called again for that event; the row keeps retrying, and dead-letters, only
// because of the legs that still fail.
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// legErrorMaxBytes caps outbox_event_legs.last_error.
const legErrorMaxBytes = 2000

// legStateWriteTimeout bounds one state read or write.
const legStateWriteTimeout = 5 * time.Second

// Leg is one named member of a fan-out. Name is stored in
// outbox_event_legs.leg, so it must stay the same across releases: renaming a
// leg makes every pending event call it again.
type Leg struct {
	Name       string
	Dispatcher Dispatcher
}

// LegStateStore keeps the per-event, per-leg delivery state.
type LegStateStore interface {
	// LegStates returns the state rows of one event: leg name → delivered.
	// A leg without a row has never failed for this event.
	LegStates(ctx context.Context, eventID string) (map[string]bool, error)

	// MarkLegDelivered records that leg delivered the event.
	MarkLegDelivered(ctx context.Context, eventID, leg string) error

	// MarkLegFailed records one failed attempt of leg with its error text.
	MarkLegFailed(ctx context.Context, eventID, leg, lastErr string) error
}

// LegFanOut is a Dispatcher that calls its legs in order and keeps per-leg
// state, see the package comment of this file.
type LegFanOut struct {
	legs   []Leg
	store  LegStateStore
	logger *slog.Logger
}

// NewLegFanOut builds a fan-out over legs. Every leg needs a non-empty, unique
// name and a dispatcher. A nil store keeps no state: every leg is called on
// every attempt, but a failing leg still does not stop the others.
func NewLegFanOut(store LegStateStore, logger *slog.Logger, legs ...Leg) (*LegFanOut, error) {
	seen := make(map[string]bool, len(legs))
	for i, l := range legs {
		if strings.TrimSpace(l.Name) == "" {
			return nil, fmt.Errorf("outbox leg fan-out: leg %d has no name", i)
		}
		if l.Dispatcher == nil {
			return nil, fmt.Errorf("outbox leg fan-out: leg %q has no dispatcher", l.Name)
		}
		if seen[l.Name] {
			return nil, fmt.Errorf("outbox leg fan-out: leg name %q is used twice", l.Name)
		}
		seen[l.Name] = true
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &LegFanOut{
		legs:   append([]Leg(nil), legs...),
		store:  store,
		logger: logger.With(slog.String("component", "outbox_leg_fanout")),
	}, nil
}

// Legs returns the leg names in call order.
func (f *LegFanOut) Legs() []string {
	out := make([]string, len(f.legs))
	for i, l := range f.legs {
		out[i] = l.Name
	}
	return out
}

// Dispatch implements Dispatcher.
//
// It loads the state of ev.ID, skips the delivered legs and calls every other
// leg in order, also after one has failed. It returns nil when every called
// leg delivered, otherwise errors.Join of the failures, each prefixed with its
// leg name, so the outbox row is retried.
//
// State is written only when it is needed later: when a leg failed in this
// pass, or when the event already has state from an earlier pass. A first pass
// in which every leg delivers writes nothing, because the row is then marked
// processed and never claimed again.
//
// When the state cannot be read, no leg is called and the error is returned,
// so the row is retried later instead of repeating legs that already
// delivered. A state write that fails after a successful leg is only logged:
// the leg may then be called again on the next attempt (at-least-once).
// An event without an ID gets a plain fan-out without state.
//
// A leg that answers ErrDispatchDisabled made no attempt: it is not recorded
// and not part of the returned error. Only when every leg still owed the
// event is disabled does Dispatch return ErrDispatchDisabled.
func (f *LegFanOut) Dispatch(ctx context.Context, ev Event) error {
	stateful := f.store != nil && ev.ID != ""

	var states map[string]bool
	if stateful {
		readCtx, cancel := context.WithTimeout(ctx, legStateWriteTimeout)
		var err error
		states, err = f.store.LegStates(readCtx, ev.ID)
		cancel()
		if err != nil {
			return fmt.Errorf("outbox leg fan-out: read leg state of event %s: %w", ev.ID, err)
		}
	}

	type result struct {
		leg string
		err error
	}
	results := make([]result, 0, len(f.legs))
	var failures []error
	disabled := 0
	for _, l := range f.legs {
		if states[l.Name] {
			continue // delivered on an earlier attempt
		}
		err := l.Dispatcher.Dispatch(ctx, ev)
		if errors.Is(err, ErrDispatchDisabled) {
			// A disabled leg made no attempt: it is neither a delivery nor a
			// failure. Joined into the error it would make the outbox leave
			// the row untouched (no attempts, no dead-letter) and hide the
			// real failures of the other legs.
			disabled++
			continue
		}
		results = append(results, result{leg: l.Name, err: err})
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", l.Name, err))
		}
	}

	if stateful && (len(failures) > 0 || len(states) > 0) {
		// The leg results must be recorded even when ctx was cancelled during
		// the pass: a delivered leg that is not recorded is called again.
		writeBase := context.WithoutCancel(ctx)
		for _, r := range results {
			writeCtx, cancel := context.WithTimeout(writeBase, legStateWriteTimeout)
			var werr error
			if r.err == nil {
				werr = f.store.MarkLegDelivered(writeCtx, ev.ID, r.leg)
			} else {
				werr = f.store.MarkLegFailed(writeCtx, ev.ID, r.leg, truncateLegError(r.err.Error()))
			}
			cancel()
			if werr != nil {
				f.logger.Error("outbox leg fan-out: write leg state failed",
					slog.String("event_id", ev.ID),
					slog.String("event_type", ev.EventType),
					slog.String("leg", r.leg),
					slog.Bool("delivered", r.err == nil),
					slog.String("error", werr.Error()),
				)
			}
		}
	}

	for _, r := range results {
		if r.err != nil {
			f.logger.Warn("outbox leg fan-out: leg failed",
				slog.String("event_id", ev.ID),
				slog.String("event_type", ev.EventType),
				slog.String("leg", r.leg),
				slog.String("error", r.err.Error()),
			)
		}
	}

	if len(results) == 0 && disabled > 0 {
		// Every leg still owed the event is disabled: keep today's disabled
		// mode, the row stays untouched until a leg is switched back on.
		return ErrDispatchDisabled
	}
	return errors.Join(failures...)
}

// truncateLegError clips s to legErrorMaxBytes without cutting a UTF-8
// character in two (PostgreSQL rejects invalid UTF-8 in a text column).
func truncateLegError(s string) string {
	if len(s) <= legErrorMaxBytes {
		return s
	}
	return strings.ToValidUTF8(s[:legErrorMaxBytes], "")
}

// Compile-time interface guard.
var _ Dispatcher = (*LegFanOut)(nil)

// =============================================================================
// PGLegStateStore — production implementation over outbox_event_legs
// =============================================================================

// PGLegStateStore implements LegStateStore against outbox_event_legs
// (migration 0215). Rows go away with their outbox_events row (ON DELETE
// CASCADE).
type PGLegStateStore struct {
	pool *pgxpool.Pool
}

// NewPGLegStateStore wraps a pool into a PGLegStateStore.
func NewPGLegStateStore(pool *pgxpool.Pool) *PGLegStateStore {
	return &PGLegStateStore{pool: pool}
}

const legStatesSQL = `
	SELECT leg, delivered_at IS NOT NULL
	  FROM outbox_event_legs
	 WHERE event_id = $1::uuid
`

// LegStates implements LegStateStore.
func (s *PGLegStateStore) LegStates(ctx context.Context, eventID string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, legStatesSQL, eventID)
	if err != nil {
		return nil, fmt.Errorf("outbox leg state: query: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			leg       string
			delivered bool
		)
		if err := rows.Scan(&leg, &delivered); err != nil {
			return nil, fmt.Errorf("outbox leg state: scan: %w", err)
		}
		out[leg] = delivered
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox leg state: rows: %w", err)
	}
	return out, nil
}

const markLegDeliveredSQL = `
	INSERT INTO outbox_event_legs (event_id, leg, attempts, delivered_at, updated_at)
	VALUES ($1::uuid, $2, 1, now(), now())
	ON CONFLICT (event_id, leg) DO UPDATE
	   SET attempts     = outbox_event_legs.attempts + 1,
	       delivered_at = now(),
	       updated_at   = now()
`

// MarkLegDelivered implements LegStateStore.
func (s *PGLegStateStore) MarkLegDelivered(ctx context.Context, eventID, leg string) error {
	if _, err := s.pool.Exec(ctx, markLegDeliveredSQL, eventID, leg); err != nil {
		return fmt.Errorf("outbox leg state: mark delivered: %w", err)
	}
	return nil
}

const markLegFailedSQL = `
	INSERT INTO outbox_event_legs (event_id, leg, attempts, last_error, updated_at)
	VALUES ($1::uuid, $2, 1, $3, now())
	ON CONFLICT (event_id, leg) DO UPDATE
	   SET attempts   = outbox_event_legs.attempts + 1,
	       last_error = EXCLUDED.last_error,
	       updated_at = now()
`

// MarkLegFailed implements LegStateStore.
func (s *PGLegStateStore) MarkLegFailed(ctx context.Context, eventID, leg, lastErr string) error {
	if _, err := s.pool.Exec(ctx, markLegFailedSQL, eventID, leg, truncateLegError(lastErr)); err != nil {
		return fmt.Errorf("outbox leg state: mark failed: %w", err)
	}
	return nil
}

// Compile-time interface guard.
var _ LegStateStore = (*PGLegStateStore)(nil)
