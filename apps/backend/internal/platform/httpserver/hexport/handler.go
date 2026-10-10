// Package hexport serves the organizer's CSV exports (EC-07, spec
// 08_architecture/35_telegram_event_center_full_ru.md §5.8):
//
//	GET /v1/organizations/{org_id}/sessions/{session_id}/sales.csv          (order.read + session.read)
//	GET /v1/organizations/{org_id}/events/{event_id}/sales.csv              (order.read + session.read)
//	GET /v1/organizations/{org_id}/sessions/{session_id}/summary.csv        (order.read + session.read)
//	GET /v1/organizations/{org_id}/promo-codes/{promo_code_id}/redemptions.csv (promo.read)
//
// Every file is written through csvexport (the only CSV writer of the
// platform) and STREAMED: rows are read in keyset batches and flushed to the
// response as they go, the handler never holds the file. Before the first
// byte the rows are counted; a file over MaxRows is refused with 413
// `export.too_many_rows` — a spreadsheet of that size is not what an
// organizer opens on a phone, and the cap keeps one request from reading a
// whole database.
//
// A row of another organization is the route's own 404, never a 403: the
// header queries are scoped by org_id, so a foreign id is indistinguishable
// from a missing one.
package hexport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// DefaultMaxRows is the hard cap of rows one export may stream.
const DefaultMaxRows int64 = 50_000

// batchSize is how many rows one keyset page reads before flushing.
const batchSize int32 = 1000

// Handler holds the shared dependencies of the export routes.
type Handler struct {
	queries *gen.Queries
	logger  *slog.Logger
	maxRows int64
}

// New constructs a Handler. A nil queries is allowed; handlers self-gate
// with a 503 dependency.database_unavailable envelope.
func New(queries *gen.Queries, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{queries: queries, logger: logger, maxRows: DefaultMaxRows}
}

// WithMaxRows lowers (or raises) the row cap; tests use it to reach the 413
// without seeding fifty thousand tickets.
func (h *Handler) WithMaxRows(n int64) *Handler {
	h.maxRows = n
	return h
}

// ready answers the 503 envelope when the handler has no database.
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) bool {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "export store not configured", r))
		return false
	}
	return true
}

// tooManyRows refuses a file above the cap before any byte is streamed.
func (h *Handler) tooManyRows(w http.ResponseWriter, r *http.Request, rows int64) bool {
	if rows <= h.maxRows {
		return false
	}
	httputil.WriteJSON(w, http.StatusRequestEntityTooLarge, httputil.ErrorEnvelopeWithDetails(
		"export.too_many_rows", "the export exceeds the row limit; narrow it to one session", r,
		map[string]any{"rows": rows, "max_rows": h.maxRows},
	))
	return true
}

// uuidParam reads a UUID path parameter, answering 400 when it is not one.
func uuidParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	return httputil.UUIDPathParam(w, r, name)
}

// notFound is the route's own 404 for a missing or foreign row.
func notFound(w http.ResponseWriter, r *http.Request, code, msg string) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(code, msg, r))
}

// internal logs a failure that happened before the response started.
func (h *Handler) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.Error("hexport: export failed", slog.String("part", what), slog.Any("error", err))
	httputil.WriteJSON(w, http.StatusInternalServerError,
		httputil.ErrorEnvelope("export.internal", "failed to build the export", r))
}

// stream is an open CSV response. Once begin() has sent the headers the
// status is committed; a later failure aborts the connection (the client
// sees a truncated transfer, never a complete-looking but short file).
type stream struct {
	w   http.ResponseWriter
	csv *csvexport.Writer
}

// begin sends the attachment headers and the 200, and returns the writer.
func begin(w http.ResponseWriter, filename string) *stream {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	return &stream{w: w, csv: csvexport.NewWriter(w)}
}

// flush pushes the batch to the client so a long file streams.
func (s *stream) flush() error {
	if err := s.csv.Flush(); err != nil {
		return err
	}
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// abort ends a response whose headers are already sent. A client write
// error (the buyer closed the download) is not logged as a failure.
func (h *Handler) abort(ctx context.Context, what string, err error) {
	if ctx.Err() == nil {
		h.logger.Error("hexport: export aborted mid-stream", slog.String("part", what), slog.Any("error", err))
	}
	// allow:panic: net/http's documented way to abort a response whose
	// status is already committed — the chunked body is left unterminated so
	// the client cannot mistake a half-written file for a complete one.
	panic(http.ErrAbortHandler)
}

// isNoRows tells a missing/foreign row from a database failure.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// venueLocation resolves a venue's IANA zone; an unknown or empty zone falls
// back to UTC rather than failing the file.
func venueLocation(tz *string) *time.Location {
	if tz == nil || *tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

var unsafeFilenameChars = regexp.MustCompile(`[^a-z0-9._-]+`)

// filename builds `<kind>_<slug>_<YYYY-MM-DD>.csv` from a kind, the event's
// slug (or its id when it has none) and the export date in the venue's zone.
// Only [a-z0-9._-] survive, so the header needs no escaping.
func filename(kind, slug string, now time.Time, loc *time.Location) string {
	s := strings.ToLower(strings.TrimSpace(slug))
	s = unsafeFilenameChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		s = "export"
	}
	if len(s) > 80 {
		s = s[:80]
	}
	// allow:timeformat: a calendar date in a file name, in the venue's zone.
	return kind + "_" + s + "_" + now.In(loc).Format("2006-01-02") + ".csv"
}

// slugOr returns the event's slug, or its id when the event has none.
func slugOr(slug *string, id string) string {
	if slug != nil && strings.TrimSpace(*slug) != "" {
		return *slug
	}
	return id
}
