package opswatchdog

import (
	"context"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
)

// fakeAlertStore is an in-memory AlertStore for unit tests.
type fakeAlertStore struct {
	rows map[string]AlertRow
}

func newFakeAlertStore() *fakeAlertStore {
	return &fakeAlertStore{rows: map[string]AlertRow{}}
}

func (f *fakeAlertStore) Get(_ context.Context, fingerprint string) (*AlertRow, error) {
	r, ok := f.rows[fingerprint]
	if !ok {
		return nil, nil
	}
	cp := r
	return &cp, nil
}

func (f *fakeAlertStore) Upsert(_ context.Context, row AlertRow) error {
	row.ResolvedAt = nil
	f.rows[row.Fingerprint] = row
	return nil
}

func (f *fakeAlertStore) MarkResolved(_ context.Context, fingerprint string, at time.Time) error {
	r, ok := f.rows[fingerprint]
	if !ok || r.ResolvedAt != nil {
		return nil
	}
	r.ResolvedAt = &at
	f.rows[fingerprint] = r
	return nil
}

func (f *fakeAlertStore) ListOpenFingerprints(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for fp, r := range f.rows {
		if r.ResolvedAt != nil {
			continue
		}
		if len(fp) > len(prefix)+1 && fp[:len(prefix)+1] == prefix+":" {
			out = append(out, fp)
		}
	}
	return out, nil
}

// recordingNotifier records every message it was asked to send.
type recordingNotifier struct {
	sent []string
}

func (r *recordingNotifier) Send(_ context.Context, text string) error {
	r.sent = append(r.sent, text)
	return nil
}

func newEngine(store AlertStore, notifier *recordingNotifier, fc *clock.FakeClock) *AlertEngine {
	return &AlertEngine{
		Store:            store,
		Notifier:         notifier,
		Clock:            fc,
		RenotifyInterval: 30 * time.Minute,
	}
}

func TestAlertEngine_Raise_NotifiesOnFirstSight(t *testing.T) {
	store := newFakeAlertStore()
	notifier := &recordingNotifier{}
	fc := clock.NewFake(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	e := newEngine(store, notifier, fc)

	err := e.Raise(context.Background(), AlertInput{
		Fingerprint: "paid_no_tickets:abc",
		Severity:    "critical",
		Title:       "order paid but no tickets issued",
		Details:     map[string]any{"order": "1000000500"},
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("sent = %d messages, want 1", len(notifier.sent))
	}
	if got := notifier.sent[0]; got == "" {
		t.Fatal("expected non-empty message")
	}
	row := store.rows["paid_no_tickets:abc"]
	if row.ResolvedAt != nil {
		t.Fatal("newly raised alert must not be resolved")
	}
	if row.LastNotifiedAt == nil {
		t.Fatal("first raise must set last_notified_at")
	}
}

func TestAlertEngine_Raise_SecondCallWithinWindowDoesNotRenotify(t *testing.T) {
	store := newFakeAlertStore()
	notifier := &recordingNotifier{}
	fc := clock.NewFake(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	e := newEngine(store, notifier, fc)

	in := AlertInput{Fingerprint: "fp1", Severity: "high", Title: "t"}
	if err := e.Raise(context.Background(), in); err != nil {
		t.Fatalf("first Raise: %v", err)
	}
	fc.Advance(5 * time.Minute)
	if err := e.Raise(context.Background(), in); err != nil {
		t.Fatalf("second Raise: %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("sent = %d messages, want 1 (re-notify window not elapsed)", len(notifier.sent))
	}
}

func TestAlertEngine_Raise_RenotifiesAfterWindowElapses(t *testing.T) {
	store := newFakeAlertStore()
	notifier := &recordingNotifier{}
	fc := clock.NewFake(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	e := newEngine(store, notifier, fc)

	in := AlertInput{Fingerprint: "fp1", Severity: "high", Title: "t"}
	if err := e.Raise(context.Background(), in); err != nil {
		t.Fatalf("first Raise: %v", err)
	}
	fc.Advance(31 * time.Minute)
	if err := e.Raise(context.Background(), in); err != nil {
		t.Fatalf("second Raise: %v", err)
	}
	if len(notifier.sent) != 2 {
		t.Fatalf("sent = %d messages, want 2 (re-notify window elapsed)", len(notifier.sent))
	}
}

func TestAlertEngine_Resolve_SendsResolvedMessageOnce(t *testing.T) {
	store := newFakeAlertStore()
	notifier := &recordingNotifier{}
	fc := clock.NewFake(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	e := newEngine(store, notifier, fc)

	in := AlertInput{Fingerprint: "fp1", Severity: "high", Title: "t"}
	if err := e.Raise(context.Background(), in); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if err := e.Resolve(context.Background(), "fp1", "t"); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if err := e.Resolve(context.Background(), "fp1", "t"); err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	// 1 raise + 1 resolve = 2 messages total; the second Resolve is a no-op.
	if len(notifier.sent) != 2 {
		t.Fatalf("sent = %d messages, want 2 (raise + one resolve)", len(notifier.sent))
	}
	row := store.rows["fp1"]
	if row.ResolvedAt == nil {
		t.Fatal("expected alert to be resolved")
	}
}

func TestAlertEngine_Resolve_UnknownFingerprintIsNoop(t *testing.T) {
	store := newFakeAlertStore()
	notifier := &recordingNotifier{}
	fc := clock.NewFake(time.Now())
	e := newEngine(store, notifier, fc)

	if err := e.Resolve(context.Background(), "never-seen", "t"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d messages, want 0", len(notifier.sent))
	}
}

func TestAlertEngine_Raise_RecurringAfterResolveIsTreatedAsNew(t *testing.T) {
	store := newFakeAlertStore()
	notifier := &recordingNotifier{}
	fc := clock.NewFake(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	e := newEngine(store, notifier, fc)

	in := AlertInput{Fingerprint: "fp1", Severity: "high", Title: "t"}
	if err := e.Raise(context.Background(), in); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if err := e.Resolve(context.Background(), "fp1", "t"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	fc.Advance(time.Minute)
	if err := e.Raise(context.Background(), in); err != nil {
		t.Fatalf("second Raise: %v", err)
	}
	if len(notifier.sent) != 3 {
		t.Fatalf("sent = %d messages, want 3 (raise, resolve, raise-again)", len(notifier.sent))
	}
	row := store.rows["fp1"]
	if row.ResolvedAt != nil {
		t.Fatal("recurring alert must be open again")
	}
}

func TestAlertEngine_Sync_ResolvesFingerprintsNoLongerCurrent(t *testing.T) {
	store := newFakeAlertStore()
	notifier := &recordingNotifier{}
	fc := clock.NewFake(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	e := newEngine(store, notifier, fc)

	ctx := context.Background()
	first := []AlertInput{
		{Fingerprint: "paid_no_tickets:1", Severity: "critical", Title: "a"},
		{Fingerprint: "paid_no_tickets:2", Severity: "critical", Title: "b"},
	}
	if err := e.Sync(ctx, "paid_no_tickets", first, nil); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if len(notifier.sent) != 2 {
		t.Fatalf("sent after first sync = %d, want 2", len(notifier.sent))
	}

	// Second run: only fingerprint 1 still reproduces; 2 cleared.
	second := []AlertInput{
		{Fingerprint: "paid_no_tickets:1", Severity: "critical", Title: "a"},
	}
	if err := e.Sync(ctx, "paid_no_tickets", second, nil); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if len(notifier.sent) != 3 {
		t.Fatalf("sent after second sync = %d, want 3 (one resolved message for #2)", len(notifier.sent))
	}
	if store.rows["paid_no_tickets:2"].ResolvedAt == nil {
		t.Fatal("fingerprint 2 should be resolved")
	}
	if store.rows["paid_no_tickets:1"].ResolvedAt != nil {
		t.Fatal("fingerprint 1 should still be open")
	}
}

func TestAlertEngine_Sync_DoesNotTouchOtherPrefixes(t *testing.T) {
	store := newFakeAlertStore()
	notifier := &recordingNotifier{}
	fc := clock.NewFake(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	e := newEngine(store, notifier, fc)
	ctx := context.Background()

	if err := e.Raise(ctx, AlertInput{Fingerprint: "other_check:1", Severity: "warn", Title: "x"}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if err := e.Sync(ctx, "paid_no_tickets", nil, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if store.rows["other_check:1"].ResolvedAt != nil {
		t.Fatal("Sync must not resolve fingerprints outside its own prefix")
	}
}
