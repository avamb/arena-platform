// Package audit implements the AuditWriter boundary described in
// app_spec.txt §boundaries.
//
// Every authenticated mutating request records exactly one row in
// audit_events. The row captures who did what, on which resource, with which
// correlation identifiers (request_id, trace_id), and where it came from
// (client IP).
//
// Two persistence entry points are exposed:
//
//   - Writer.Write    — fire-and-forget INSERT against the pgx pool. Suitable
//                       for read paths or rare side effects.
//   - Writer.WriteTx  — INSERT inside a caller-supplied pgx.Tx. This is the
//                       recommended path for /v1/echo and any other endpoint
//                       that must keep the audit row atomic with its
//                       business writes and outbox emission.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is the value-object inserted into audit_events. All string fields
// except Metadata are stored verbatim; nil/empty metadata becomes "{}".
type Event struct {
	OccurredAt   time.Time
	ActorType    string
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	RequestID    string
	TraceID      string
	IP           string
	Metadata     map[string]any
}

// Writer persists Event rows. Implementations must be safe for concurrent use.
type Writer interface {
	Write(ctx context.Context, ev Event) error
	WriteTx(ctx context.Context, tx pgx.Tx, ev Event) error
}

// -----------------------------------------------------------------------------
// PostgreSQL implementation
// -----------------------------------------------------------------------------

// PGWriter is the production audit writer backed by audit_events.
type PGWriter struct {
	pool *pgxpool.Pool
}

// NewPGWriter constructs a PGWriter around a live pgx pool.
func NewPGWriter(pool *pgxpool.Pool) *PGWriter { return &PGWriter{pool: pool} }

// Write inserts ev outside any caller transaction.
func (w *PGWriter) Write(ctx context.Context, ev Event) error {
	if w.pool == nil {
		return errors.New("audit: PGWriter pool is nil")
	}
	args, err := prepareArgs(ev)
	if err != nil {
		return err
	}
	if _, err := w.pool.Exec(ctx, insertSQL, args...); err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}

// WriteTx inserts ev using the supplied transaction.
func (w *PGWriter) WriteTx(ctx context.Context, tx pgx.Tx, ev Event) error {
	if tx == nil {
		return errors.New("audit: WriteTx requires a non-nil tx")
	}
	args, err := prepareArgs(ev)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, insertSQL, args...); err != nil {
		return fmt.Errorf("audit: insert (tx): %w", err)
	}
	return nil
}

const insertSQL = `
	INSERT INTO audit_events (
		occurred_at, actor_type, actor_id, action, resource_type, resource_id,
		request_id, trace_id, ip, metadata
	) VALUES (
		$1, $2, NULLIF($3,'')::uuid, $4, $5, $6, $7, $8, NULLIF($9,'')::inet, $10
	)
`

func prepareArgs(ev Event) ([]any, error) {
	if strings.TrimSpace(ev.ActorType) == "" {
		ev.ActorType = "anonymous"
	}
	if strings.TrimSpace(ev.Action) == "" {
		return nil, errors.New("audit: Action is required")
	}
	if strings.TrimSpace(ev.ResourceType) == "" {
		return nil, errors.New("audit: ResourceType is required")
	}
	if strings.TrimSpace(ev.ResourceID) == "" {
		return nil, errors.New("audit: ResourceID is required")
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	if ev.Metadata == nil {
		ev.Metadata = map[string]any{}
	}
	meta, err := json.Marshal(ev.Metadata)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal metadata: %w", err)
	}
	return []any{
		ev.OccurredAt,
		ev.ActorType,
		ev.ActorID,
		ev.Action,
		ev.ResourceType,
		ev.ResourceID,
		ev.RequestID,
		ev.TraceID,
		canonicaliseIP(ev.IP),
		meta,
	}, nil
}

// canonicaliseIP normalises whatever IP string we received (X-Forwarded-For,
// RemoteAddr like "1.2.3.4:5678") into the canonical textual form Postgres
// stores in the inet column. Returns "" when the input is not a valid IP so
// the NULLIF($9,'') guard in insertSQL keeps the column NULL rather than
// failing the INSERT on a parse error.
func canonicaliseIP(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	return ""
}
