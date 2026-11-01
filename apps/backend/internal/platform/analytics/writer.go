package analytics

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Defaults of the writer (spec 50 section 4.3).
const (
	DefaultBuffer     = 10000
	DefaultBatchSize  = 500
	DefaultFlushEvery = 2 * time.Second
	flushTimeout      = 15 * time.Second
)

var idValueRe = regexp.MustCompile(`^[A-Za-z0-9_:.-]{1,64}$`)

// Event is one thing to record. Props are the raw properties: Track passes
// them through the dictionary, so a caller cannot store anything it lists
// wrongly.
type Event struct {
	// At is the server time of the event; zero means now.
	At time.Time
	// Surface is site, event_center or bot.
	Surface string
	Name    string
	// IDKind and IDValue identify who did it (IDKind* constants).
	IDKind  string
	IDValue string
	// OrgID is the organization of the current screen, when there is one.
	OrgID   *uuid.UUID
	Props   map[string]any
	Suspect bool
	// ServerOrigin lets a server-only event (email_click) pass the
	// dictionary; the HTTP receivers set it only for events they build.
	ServerOrigin bool
}

// Tracker is what the receivers and the bot need from the writer.
type Tracker interface {
	Track(ctx context.Context, ev Event)
}

// Counter is the part of a Prometheus counter the writer uses.
type Counter interface{ Add(float64) }

// Copier is the part of *pgxpool.Pool the writer uses.
type Copier interface {
	CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error)
}

// Options configure a Writer. DB is required; everything else has a default.
type Options struct {
	DB     Copier
	Logger *slog.Logger
	// Dropped counts events thrown away because the buffer was full
	// (arena_analytics_dropped_total).
	Dropped Counter
	// FlushFailed counts events lost because a batched write failed
	// (arena_analytics_flush_failed_total).
	FlushFailed Counter
	Buffer      int
	BatchSize   int
	FlushEvery  time.Duration
}

type row struct {
	at      time.Time
	surface string
	name    string
	idKind  string
	idValue string
	orgID   any
	props   []byte
	suspect bool
}

// Writer buffers events and writes them to analytics.events in batches with
// COPY. Track never blocks and never touches the database: the request of a
// user is never slowed or failed by analytics.
type Writer struct {
	db          Copier
	log         *slog.Logger
	dropped     Counter
	flushFailed Counter
	batchSize   int
	flushEvery  time.Duration

	ch     chan row
	stop   chan struct{}
	done   chan struct{}
	closed atomic.Bool
}

// NewWriter starts the background flusher. Call Close at shutdown.
func NewWriter(o Options) *Writer {
	if o.Buffer <= 0 {
		o.Buffer = DefaultBuffer
	}
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultBatchSize
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = DefaultFlushEvery
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	w := &Writer{
		db:          o.DB,
		log:         o.Logger,
		dropped:     o.Dropped,
		flushFailed: o.FlushFailed,
		batchSize:   o.BatchSize,
		flushEvery:  o.FlushEvery,
		ch:          make(chan row, o.Buffer),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	go w.run()
	return w
}

// Track puts the event into the buffer. An unknown event, a surface the event
// is not allowed on or an unusable identifier is dropped silently; a full
// buffer drops the event and counts it. Safe on a nil Writer.
func (w *Writer) Track(_ context.Context, ev Event) {
	if w == nil {
		return
	}
	r, ok := buildRow(ev)
	if !ok {
		return
	}
	if w.closed.Load() {
		w.drop(1)
		return
	}
	select {
	case w.ch <- r:
	default:
		w.drop(1)
	}
}

func (w *Writer) drop(n float64) {
	if w.dropped != nil {
		w.dropped.Add(n)
	}
}

// buildRow validates ev against the dictionary and freezes it into a row.
func buildRow(ev Event) (row, bool) {
	props, ok := Sanitize(ev.Surface, ev.Name, ev.Props, !ev.ServerOrigin)
	if !ok {
		return row{}, false
	}
	switch ev.IDKind {
	case IDKindAID, IDKindCID, IDKindUser, IDKindTG, IDKindAnon:
	default:
		return row{}, false
	}
	if !idValueRe.MatchString(ev.IDValue) {
		return row{}, false
	}
	b, err := json.Marshal(props)
	if err != nil {
		return row{}, false
	}
	at := ev.At
	if at.IsZero() {
		at = time.Now()
	}
	var org any
	if ev.OrgID != nil && *ev.OrgID != uuid.Nil {
		org = *ev.OrgID
	}
	return row{
		at: at.UTC(), surface: ev.Surface, name: ev.Name, idKind: ev.IDKind,
		idValue: ev.IDValue, orgID: org, props: b, suspect: ev.Suspect,
	}, true
}

var copyColumns = []string{"at", "surface", "name", "id_kind", "id_value", "org_id", "props", "suspect"}

func (w *Writer) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.flushEvery)
	defer ticker.Stop()
	batch := make([]row, 0, w.batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		w.write(batch)
		batch = batch[:0]
	}
	for {
		select {
		case r := <-w.ch:
			batch = append(batch, r)
			if len(batch) >= w.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-w.stop:
			// Take everything still buffered, then the last write.
			for {
				select {
				case r := <-w.ch:
					batch = append(batch, r)
					if len(batch) >= w.batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// write sends one batch. A failure is logged once without any event content
// and counted; the batch is not retried (analytics must not pile up behind a
// broken table).
func (w *Writer) write(batch []row) {
	if w.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	rows := make([][]any, len(batch))
	for i, r := range batch {
		rows[i] = []any{r.at, r.surface, r.name, r.idKind, r.idValue, r.orgID, r.props, r.suspect}
	}
	if _, err := w.db.CopyFrom(ctx, pgx.Identifier{"analytics", "events"}, copyColumns, pgx.CopyFromRows(rows)); err != nil {
		w.log.Warn("analytics: batch write failed", slog.Int("events", len(batch)), slog.String("error", err.Error()))
		if w.flushFailed != nil {
			w.flushFailed.Add(float64(len(batch)))
		}
	}
}

// Close stops accepting events, writes what is buffered and waits for the
// flusher, bounded by ctx. Safe to call more than once and on a nil Writer.
func (w *Writer) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if w.closed.CompareAndSwap(false, true) {
		close(w.stop)
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
