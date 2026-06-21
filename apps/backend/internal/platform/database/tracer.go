// Package database — pgx QueryTracer that mirrors every SQL statement into
// the slog stream so AutoForge's "Backend API queries real database"
// verification (feature #5) can prove that handlers are talking to PostgreSQL
// and not to an in-memory mock.
//
// The tracer logs two records per query:
//
//   - msg="db query" at TraceQueryStart  → records the SQL, args, and the
//     trace_id / request_id / correlation_id picked up from ctx.
//   - msg="db query end" at TraceQueryEnd → records elapsed_ms, the
//     pgx command tag (rows affected / SELECT 1), and any error.
//
// Logging is gated on cfg.DBLogQueries OR cfg.LogLevel=="debug" so production
// can run the tracer silently when verbose logging is undesirable.
//
// Implementation notes:
//
//   - The tracer fulfils pgx.QueryTracer; pgx invokes Start before every
//     Query / QueryRow / Exec and End after the same call returns, even
//     inside a transaction. That means BEGIN/COMMIT/ROLLBACK and the inner
//     statements all show up — which is exactly what feature #5 step 6
//     wants (audit INSERT, outbox INSERT, idempotency SELECT/INSERT).
//   - We attach a per-query start timestamp via a private context key so
//     End can compute elapsed_ms without sharing state on the tracer.
//   - Args are formatted via fmt's %#v fallback to a short safe form:
//     primitives inline, byte slices as "[%d B]", anything else as
//     fmt.Sprintf("%v"). Sensitive values (passwords, tokens) MUST be
//     redacted by the caller before reaching the driver; the tracer makes
//     no attempt to guess.
package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/jackc/pgx/v5"
)

// QueryTracer is a pgx.QueryTracer that mirrors every SQL statement into the
// configured slog stream. Construct one with NewQueryTracer and attach it to
// pgxpool.Config.ConnConfig.Tracer before opening the pool.
type QueryTracer struct {
	logger *slog.Logger
	level  slog.Level
}

// NewQueryTracer builds a QueryTracer that logs at slog.LevelInfo by default.
// Callers can pin a different level via the optional level argument; debug is
// recommended for production so a future LOG_LEVEL flip surfaces the SQL
// stream without redeploying.
func NewQueryTracer(logger *slog.Logger, level slog.Level) *QueryTracer {
	if logger == nil {
		logger = slog.Default()
	}
	return &QueryTracer{logger: logger, level: level}
}

// queryStartKey is the unexported context key used to thread the per-query
// start time from TraceQueryStart into TraceQueryEnd.
type queryStartCtxKey struct{}

// TraceQueryStart implements pgx.QueryTracer. It records the SQL and args of
// the impending query and stamps the start time on ctx so TraceQueryEnd can
// compute elapsed_ms.
func (t *QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	// Stamp start time even if logging is disabled — TraceQueryEnd needs it.
	ctx = context.WithValue(ctx, queryStartCtxKey{}, time.Now())

	// Pull the contextual logger so request_id / correlation_id / trace_id
	// land on the record automatically.
	logger := logging.FromContext(ctx)
	if !logger.Enabled(ctx, t.level) {
		return ctx
	}

	logger.LogAttrs(ctx, t.level, "db query",
		slog.String("sql", normalizeSQL(data.SQL)),
		slog.String("op", queryOp(data.SQL)),
		slog.Int("args_count", len(data.Args)),
		slog.String("args", formatArgs(data.Args)),
	)
	return ctx
}

// TraceQueryEnd implements pgx.QueryTracer. It computes elapsed_ms from the
// timestamp stored by TraceQueryStart, then emits one slog record with the
// command tag and (if any) error. Errors are always recorded at LevelError
// regardless of t.level so failed queries never silently disappear.
func (t *QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	var elapsed time.Duration
	if v, ok := ctx.Value(queryStartCtxKey{}).(time.Time); ok && !v.IsZero() {
		elapsed = time.Since(v)
	}

	logger := logging.FromContext(ctx)

	if data.Err != nil {
		// pgx returns pgx.ErrNoRows from QueryRow for legitimate zero-row
		// reads — that is not an error condition, so log it at the tracer's
		// regular level instead of Error.
		level := slog.LevelError
		if errors.Is(data.Err, pgx.ErrNoRows) {
			level = t.level
		}
		logger.LogAttrs(ctx, level, "db query end",
			slog.String("status", "error"),
			slog.String("error", data.Err.Error()),
			slog.Float64("elapsed_ms", floatMillis(elapsed)),
			slog.String("command_tag", data.CommandTag.String()),
		)
		return
	}

	if !logger.Enabled(ctx, t.level) {
		return
	}
	logger.LogAttrs(ctx, t.level, "db query end",
		slog.String("status", "ok"),
		slog.Float64("elapsed_ms", floatMillis(elapsed)),
		slog.String("command_tag", data.CommandTag.String()),
		slog.Int64("rows_affected", data.CommandTag.RowsAffected()),
	)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// normalizeSQL flattens whitespace so multi-line query strings show up as a
// single, scrubbable log line. We do NOT redact placeholders; pgx forwards
// args separately via TraceQueryStartData.Args.
func normalizeSQL(sql string) string {
	// Strip carriage returns first so Windows-saved migrations don't show
	// "\r" escapes in JSON output.
	sql = strings.ReplaceAll(sql, "\r", " ")
	sql = strings.ReplaceAll(sql, "\n", " ")
	sql = strings.ReplaceAll(sql, "\t", " ")
	for strings.Contains(sql, "  ") {
		sql = strings.ReplaceAll(sql, "  ", " ")
	}
	return strings.TrimSpace(sql)
}

// queryOp returns a coarse operation label (SELECT, INSERT, UPDATE, DELETE,
// BEGIN, COMMIT, ROLLBACK, OTHER) derived from the leading SQL keyword.
// Useful for log filters / dashboards without parsing the whole statement.
func queryOp(sql string) string {
	sql = strings.TrimLeft(sql, " \t\r\n")
	// Skip a leading "-- comment" or "/* comment */".
	for strings.HasPrefix(sql, "--") {
		if idx := strings.IndexAny(sql, "\r\n"); idx >= 0 {
			sql = strings.TrimLeft(sql[idx+1:], " \t\r\n")
			continue
		}
		break
	}
	upper := strings.ToUpper(sql)
	switch {
	case strings.HasPrefix(upper, "SELECT"):
		return "SELECT"
	case strings.HasPrefix(upper, "INSERT"):
		return "INSERT"
	case strings.HasPrefix(upper, "UPDATE"):
		return "UPDATE"
	case strings.HasPrefix(upper, "DELETE"):
		return "DELETE"
	case strings.HasPrefix(upper, "BEGIN"), strings.HasPrefix(upper, "START TRANSACTION"):
		return "BEGIN"
	case strings.HasPrefix(upper, "COMMIT"):
		return "COMMIT"
	case strings.HasPrefix(upper, "ROLLBACK"):
		return "ROLLBACK"
	case strings.HasPrefix(upper, "WITH"):
		return "WITH"
	default:
		return "OTHER"
	}
}

// formatArgs renders the pgx argument slice into a short, slog-friendly
// string. Each argument is rendered with %v; byte slices are summarised by
// length so long jsonb payloads don't blow up the log line.
func formatArgs(args []any) string {
	if len(args) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "$%d=", i+1)
		switch v := a.(type) {
		case []byte:
			fmt.Fprintf(&b, "[%d B]", len(v))
		case string:
			if len(v) > 200 {
				fmt.Fprintf(&b, "%q…(%d)", v[:200], len(v))
			} else {
				fmt.Fprintf(&b, "%q", v)
			}
		default:
			s := fmt.Sprintf("%v", a)
			if len(s) > 200 {
				fmt.Fprintf(&b, "%s…(%d)", s[:200], len(s))
			} else {
				b.WriteString(s)
			}
		}
	}
	b.WriteByte(']')
	return b.String()
}

func floatMillis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}
