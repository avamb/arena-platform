package audit

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// SlogWriter is an audit.Writer implementation that records every Event as a
// structured slog log entry at INFO level with the "audit" category attribute.
//
// It is the in-process writer for the initial milestone — no database table is
// required. The real persistent writer (PGWriter) stores rows in audit_events;
// SlogWriter is a drop-in replacement for development, testing, and any
// workflow that only needs the observable audit trail in structured logs.
type SlogWriter struct {
	logger *slog.Logger
}

// NewSlogWriter returns a SlogWriter backed by logger. If logger is nil, the
// default slog.Default() logger is used.
func NewSlogWriter(logger *slog.Logger) *SlogWriter {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogWriter{logger: logger}
}

// Write records ev as a structured log entry. The tx parameter is ignored
// because SlogWriter has no database involvement.
//
// Implements audit.Writer.
func (w *SlogWriter) Write(ctx context.Context, ev Event) error {
	w.logEvent(ctx, ev)
	return nil
}

// WriteTx records ev as a structured log entry. The transaction is accepted for
// interface compatibility but is not used — SlogWriter has no DB involvement.
//
// Implements audit.Writer.
func (w *SlogWriter) WriteTx(ctx context.Context, _ pgx.Tx, ev Event) error {
	w.logEvent(ctx, ev)
	return nil
}

// logEvent emits the structured slog record common to Write and WriteTx.
func (w *SlogWriter) logEvent(ctx context.Context, ev Event) {
	if ev.Metadata == nil {
		ev.Metadata = map[string]any{}
	}

	w.logger.InfoContext(ctx, "audit event",
		slog.String("category", "audit"),
		slog.String("actor_type", ev.ActorType),
		slog.String("actor_id", ev.ActorID),
		slog.String("action", ev.Action),
		slog.String("resource_type", ev.ResourceType),
		slog.String("resource_id", ev.ResourceID),
		slog.String("request_id", ev.RequestID),
		slog.String("trace_id", ev.TraceID),
		slog.String("ip", ev.IP),
		slog.Any("metadata", ev.Metadata),
		slog.Time("occurred_at", ev.OccurredAt),
	)
}

// Compile-time interface check.
var _ Writer = (*SlogWriter)(nil)
