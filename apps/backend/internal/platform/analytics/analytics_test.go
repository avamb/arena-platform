package analytics

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// forbiddenProps may never be an allowed property: a person's data has no
// place in an event (spec 50 section 11).
var forbiddenProps = []string{"email", "phone", "name", "text", "value", "user_id", "id", "ip"}

// dataTrackName is the one allowed use of the word "name": the value of the
// element's data-track attribute, a constant written by our developers.
var dataTrackName = map[string]bool{"click": true, "rage_click": true}

func TestDictionary_NoPersonalDataProperty(t *testing.T) {
	for name, def := range Events {
		for prop := range def.Props {
			bad := strings.Contains(prop, "email") || strings.Contains(prop, "phone")
			for _, f := range forbiddenProps {
				if prop == f {
					bad = true
				}
			}
			if prop == "name" && dataTrackName[name] {
				bad = false
			}
			if bad {
				t.Errorf("event %q allows property %q", name, prop)
			}
		}
	}
}

func TestDictionary_EveryEventHasASurface(t *testing.T) {
	valid := map[string]bool{SurfaceSite: true, SurfaceEventCenter: true, SurfaceBot: true}
	if len(Events) == 0 {
		t.Fatal("empty dictionary")
	}
	for name, def := range Events {
		if len(def.Surfaces) == 0 {
			t.Errorf("event %q has no surface", name)
		}
		for _, s := range def.Surfaces {
			if !valid[s] {
				t.Errorf("event %q: unknown surface %q", name, s)
			}
		}
		if len(def.Props) == 0 {
			t.Errorf("event %q has no properties", name)
		}
	}
}

func TestDictionary_SpecEventsPresent(t *testing.T) {
	for _, n := range []string{"page_view", "engaged", "scroll", "click", "email_click", "consent",
		"screen_view", "flow_step", "ui_error", "rage_click", "bot_update", "wizard_step",
		"wizard_input_error", "wizard_exit"} {
		if _, ok := Events[n]; !ok {
			t.Errorf("spec event %q missing", n)
		}
	}
}

func TestSanitize_UnknownEventAndSurface(t *testing.T) {
	if _, ok := Sanitize(SurfaceSite, "nope", map[string]any{}, true); ok {
		t.Error("unknown event kept")
	}
	if _, ok := Sanitize(SurfaceBot, "page_view", map[string]any{"path": "/x"}, true); ok {
		t.Error("page_view accepted from the bot surface")
	}
	if _, ok := Sanitize(SurfaceSite, "email_click", map[string]any{"path": "/x"}, true); ok {
		t.Error("server-only event accepted from a client")
	}
	if _, ok := Sanitize(SurfaceSite, "email_click", map[string]any{"path": "/x"}, false); !ok {
		t.Error("server-only event refused for the server")
	}
}

func TestSanitize_PropsFilteredAndCut(t *testing.T) {
	long := strings.Repeat("я", 300)
	got, ok := Sanitize(SurfaceSite, "page_view", map[string]any{
		"path":     "/pricing?email=a@b.c#top",
		"title":    long,
		"lang":     "ru",
		"email":    "a@b.c",
		"ref_host": "https://User:pw@Google.COM:443/search?q=x",
		"utm_term": 5.0, // wrong type
	}, true)
	if !ok {
		t.Fatal("page_view refused")
	}
	if got["path"] != "/pricing" {
		t.Errorf("path = %q", got["path"])
	}
	if n := len([]rune(got["title"].(string))); n != MaxStringLen {
		t.Errorf("title length = %d, want %d", n, MaxStringLen)
	}
	if got["ref_host"] != "google.com" {
		t.Errorf("ref_host = %q", got["ref_host"])
	}
	for _, k := range []string{"email", "utm_term"} {
		if _, has := got[k]; has {
			t.Errorf("property %q kept", k)
		}
	}
}

func TestSanitize_TypesAndEnums(t *testing.T) {
	got, _ := Sanitize(SurfaceSite, "scroll", map[string]any{"path": "/a", "depth": 50.0}, true)
	if got["depth"] != 50 {
		t.Errorf("depth = %v", got["depth"])
	}
	got, _ = Sanitize(SurfaceSite, "scroll", map[string]any{"path": "/a", "depth": 33.0}, true)
	if _, has := got["depth"]; has {
		t.Error("depth outside 25/50/75/100 kept")
	}
	got, _ = Sanitize(SurfaceBot, "wizard_exit", map[string]any{"step": "venue", "how": "explode"}, true)
	if _, has := got["how"]; has {
		t.Error("unknown how kept")
	}
	got, _ = Sanitize(SurfaceSite, "consent", map[string]any{"analytics": true}, true)
	if got["analytics"] != true {
		t.Errorf("analytics = %v", got["analytics"])
	}
	got, _ = Sanitize(SurfaceSite, "engaged", map[string]any{"path": "/a", "seconds": 1.5}, true)
	if _, has := got["seconds"]; has {
		t.Error("fractional seconds kept")
	}
}

func TestSanitize_ControlCharactersRemoved(t *testing.T) {
	got, _ := Sanitize(SurfaceEventCenter, "ui_error", map[string]any{"screen": "a\x00b\tc", "code": "\x00"}, true)
	if got["screen"] != "abc" {
		t.Errorf("screen = %q", got["screen"])
	}
	if _, has := got["code"]; has {
		t.Error("an empty string after cleaning was kept")
	}
}

// ---------------------------------------------------------------- writer ---

type fakeCopier struct {
	mu      sync.Mutex
	batches []int
	block   chan struct{}
	err     error
}

func (f *fakeCopier) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, src pgx.CopyFromSource) (int64, error) {
	if f.block != nil {
		<-f.block
	}
	n := 0
	for src.Next() {
		if _, err := src.Values(); err != nil {
			return 0, err
		}
		n++
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, n)
	return int64(n), f.err
}

func (f *fakeCopier) total() (events, batches int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.batches {
		events += n
	}
	return events, len(f.batches)
}

type countAdder struct {
	mu sync.Mutex
	n  float64
}

func (c *countAdder) Add(v float64) { c.mu.Lock(); c.n += v; c.mu.Unlock() }
func (c *countAdder) get() float64  { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func pv(n string) Event {
	return Event{Surface: SurfaceSite, Name: "page_view", IDKind: IDKindAnon, IDValue: "p_" + n,
		Props: map[string]any{"path": "/"}}
}

func TestWriter_CloseFlushesBufferedEvents(t *testing.T) {
	db := &fakeCopier{}
	w := NewWriter(Options{DB: db, FlushEvery: time.Hour})
	for i := 0; i < 7; i++ {
		w.Track(context.Background(), pv("abcdefgh"))
	}
	w.Track(context.Background(), Event{Surface: SurfaceSite, Name: "unknown", IDKind: IDKindAnon, IDValue: "p_x"})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.total(); got != 7 {
		t.Errorf("stored %d events, want 7 (the unknown event is dropped)", got)
	}
	// Track after Close neither panics nor blocks.
	w.Track(context.Background(), pv("late0000"))
}

func TestWriter_BatchBySize(t *testing.T) {
	db := &fakeCopier{}
	w := NewWriter(Options{DB: db, FlushEvery: time.Hour, BatchSize: 5})
	for i := 0; i < 12; i++ {
		w.Track(context.Background(), pv("abcdefgh"))
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, b := db.total(); b >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, b := db.total(); b < 2 {
		t.Fatalf("batches = %d, want at least 2 before the timer", b)
	}
	_ = w.Close(context.Background())
	if got, _ := db.total(); got != 12 {
		t.Errorf("stored %d, want 12", got)
	}
}

func TestWriter_FlushByTimer(t *testing.T) {
	db := &fakeCopier{}
	w := NewWriter(Options{DB: db, FlushEvery: 20 * time.Millisecond})
	defer func() { _ = w.Close(context.Background()) }()
	w.Track(context.Background(), pv("abcdefgh"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := db.total(); n == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("event not flushed by the timer")
}

func TestWriter_FullBufferDropsWithoutBlocking(t *testing.T) {
	db := &fakeCopier{block: make(chan struct{})}
	dropped := &countAdder{}
	w := NewWriter(Options{DB: db, Dropped: dropped, Buffer: 10, BatchSize: 2, FlushEvery: time.Hour})
	start := time.Now()
	for i := 0; i < 1000; i++ {
		w.Track(context.Background(), pv("abcdefgh"))
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("1000 Track calls took %v with a blocked database", d)
	}
	// The flusher holds one batch of 2, the buffer 10: the rest is dropped.
	if got := dropped.get(); got < 980 {
		t.Errorf("dropped = %v, want at least 980", got)
	}
	close(db.block)
	_ = w.Close(context.Background())
}

func TestWriter_FlushFailureCountedAndLoopSurvives(t *testing.T) {
	db := &fakeCopier{err: context.DeadlineExceeded}
	failed := &countAdder{}
	w := NewWriter(Options{DB: db, FlushFailed: failed, FlushEvery: time.Hour})
	for i := 0; i < 3; i++ {
		w.Track(context.Background(), pv("abcdefgh"))
	}
	_ = w.Close(context.Background())
	if failed.get() != 3 {
		t.Errorf("flush failures = %v, want 3", failed.get())
	}
}

func TestWriter_RejectsBadIdentifiers(t *testing.T) {
	db := &fakeCopier{}
	w := NewWriter(Options{DB: db, FlushEvery: time.Hour})
	bad := pv("abcdefgh")
	bad.IDValue = "has space"
	w.Track(context.Background(), bad)
	bad = pv("abcdefgh")
	bad.IDKind = "email"
	w.Track(context.Background(), bad)
	_ = w.Close(context.Background())
	if got, _ := db.total(); got != 0 {
		t.Errorf("stored %d events with bad identifiers", got)
	}
}

func TestNilWriterIsSafe(t *testing.T) {
	var w *Writer
	w.Track(context.Background(), pv("abcdefgh"))
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// ------------------------------------------------------------ partitions ---

func TestPartitionPolicy(t *testing.T) {
	now := time.Date(2026, 11, 30, 23, 59, 0, 0, time.UTC)
	want := []string{"events_y2026m11", "events_y2026m12", "events_y2027m01", "events_y2027m02"}
	var got []string
	for _, m := range PartitionsWanted(now) {
		got = append(got, PartitionName(m))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("wanted %v, want %v", got, want)
	}
	existing := []string{"events_y2025m09", "events_y2025m10", "events_y2026m11", "events_default", "events_foo"}
	drop := PartitionsToDrop(existing, now)
	// 13 months back from November 2026 is October 2025: September goes, October stays.
	if len(drop) != 1 || drop[0] != "events_y2025m09" {
		t.Errorf("drop = %v, want [events_y2025m09]", drop)
	}
}

func TestParsePartitionName(t *testing.T) {
	if m, ok := ParsePartitionName("events_y2026m02"); !ok || m != time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) {
		t.Errorf("parsed %v %v", m, ok)
	}
	for _, bad := range []string{"events_y2026m13", "events_y2026m00", "events", "x_events_y2026m01"} {
		if _, ok := ParsePartitionName(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestPartitionsHandler_SchedulesNextEvenOnFailure(t *testing.T) {
	sched := &fakeScheduler{}
	h := NewPartitionsHandler(PartitionsOptions{DB: failingDB{}, Scheduler: sched})
	if err := h(context.Background(), nil); err == nil {
		t.Fatal("expected the maintenance failure to be returned")
	}
	if sched.calls != 1 {
		t.Errorf("next run scheduled %d times, want 1", sched.calls)
	}
}

type fakeScheduler struct{ calls int }

func (f *fakeScheduler) ScheduleNext(context.Context, time.Time) error { f.calls++; return nil }

type failingDB struct{}

func (failingDB) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, context.DeadlineExceeded
}
func (failingDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, context.DeadlineExceeded
}
