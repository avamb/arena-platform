package eventbot

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// memDialogTable stands in for bot_dialogs: the three queries the store
// uses, over a map keyed like the table's UNIQUE (telegram_user_id, kind).
// The store under test is the production pgDialogStore, so the expiry rules
// proven here are the ones the bot runs with.
type memDialogTable struct {
	mu   sync.Mutex
	rows map[memDialogKey]gen.BotDialogRow
	// seq orders the saves exactly: two saves inside one clock tick must
	// still list the later one first.
	seq     int64
	touched map[memDialogKey]int64
}

type memDialogKey struct {
	tg   int64
	kind string
}

func (m *memDialogTable) UpsertBotDialog(_ context.Context, tg int64, orgID *uuid.UUID, kind, step string, state json.RawMessage, expiresAt time.Time) (gen.BotDialogRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memDialogKey{tg, kind}
	row, ok := m.rows[k]
	if !ok {
		row = gen.BotDialogRow{ID: uuid.New(), TelegramUserID: tg, Kind: kind, CreatedAt: time.Now()}
	}
	row.OrgID, row.Step, row.State, row.ExpiresAt, row.UpdatedAt = orgID, step, append(json.RawMessage(nil), state...), expiresAt, time.Now()
	m.rows[k] = row
	if m.touched == nil {
		m.touched = map[memDialogKey]int64{}
	}
	m.seq++
	m.touched[k] = m.seq
	return row, nil
}

func (m *memDialogTable) UpdateBotDialogQuiet(_ context.Context, tg int64, orgID *uuid.UUID, kind, step string, state json.RawMessage, expiresAt time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memDialogKey{tg, kind}
	row, ok := m.rows[k]
	if !ok {
		return 0, nil
	}
	row.OrgID, row.Step, row.State, row.ExpiresAt = orgID, step, append(json.RawMessage(nil), state...), expiresAt
	m.rows[k] = row // touched (the recency order) is left as it was
	return 1, nil
}

func (m *memDialogTable) GetBotDialog(_ context.Context, tg int64, kind string) (gen.BotDialogRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[memDialogKey{tg, kind}]
	if !ok {
		return gen.BotDialogRow{}, pgx.ErrNoRows
	}
	return row, nil
}

func (m *memDialogTable) DeleteBotDialog(_ context.Context, tg int64, kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, memDialogKey{tg, kind})
	return nil
}

func (m *memDialogTable) DeleteBotDialogIfExpired(_ context.Context, tg int64, kind string, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memDialogKey{tg, kind}
	row, ok := m.rows[k]
	if !ok || row.ExpiresAt.After(now) {
		return 0, nil
	}
	delete(m.rows, k)
	return 1, nil
}

func (m *memDialogTable) ListBotDialogKindsByRecency(_ context.Context, tg int64) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []memDialogKey
	for k := range m.rows {
		if k.tg == tg {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return m.touched[keys[i]] > m.touched[keys[j]] })
	kinds := make([]string, 0, len(keys))
	for _, k := range keys {
		kinds = append(kinds, k.kind)
	}
	return kinds, nil
}

// testClock is a settable now() for the store.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newMemDialogStore is a DialogStore with no database behind it, for unit
// tests of the dialogs.
func newMemDialogStore(clock *testClock) *pgDialogStore {
	return &pgDialogStore{q: &memDialogTable{rows: map[memDialogKey]gen.BotDialogRow{}}, now: clock.now}
}

type sampleDialog struct {
	Email string `json:"email"`
	MsgID int    `json:"msg_id"`
}

func TestDialogStore_SaveLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	s := newMemDialogStore(clock)
	org := uuid.New()

	if step, found, expired, err := s.Load(ctx, 1, "team", nil); err != nil || found || expired || step != "" {
		t.Fatalf("an absent dialog: step=%q found=%v expired=%v err=%v", step, found, expired, err)
	}
	if err := s.Save(ctx, 1, &org, "team", "role", sampleDialog{Email: "a@b.cz", MsgID: 7}, dialogTTL); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var got sampleDialog
	step, found, expired, err := s.Load(ctx, 1, "team", &got)
	if err != nil || !found || expired || step != "role" {
		t.Fatalf("a live dialog: step=%q found=%v expired=%v err=%v", step, found, expired, err)
	}
	if got != (sampleDialog{Email: "a@b.cz", MsgID: 7}) {
		t.Fatalf("state = %+v", got)
	}
	if err := s.Delete(ctx, 1, "team"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, expired, _ := s.Load(ctx, 1, "team", nil); found || expired {
		t.Fatal("a deleted dialog is neither live nor expired")
	}
	if err := s.Delete(ctx, 1, "team"); err != nil {
		t.Fatalf("deleting a missing dialog must not fail: %v", err)
	}
}

// An expired dialog is reported once and then is simply gone.
func TestDialogStore_ExpiryIsReportedOnce(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	s := newMemDialogStore(clock)

	if err := s.Save(ctx, 1, nil, "team", "email", sampleDialog{MsgID: 3}, dialogTTL); err != nil {
		t.Fatalf("Save: %v", err)
	}
	clock.advance(dialogTTL)
	var got sampleDialog
	if _, found, expired, err := s.Load(ctx, 1, "team", &got); err != nil || found || !expired {
		t.Fatalf("at the expiry: found=%v expired=%v err=%v", found, expired, err)
	}
	if got != (sampleDialog{}) {
		t.Fatalf("an expired dialog must not hand out its state: %+v", got)
	}
	if _, found, expired, err := s.Load(ctx, 1, "team", nil); err != nil || found || expired {
		t.Fatalf("the second look: found=%v expired=%v err=%v", found, expired, err)
	}
}

// Every save moves the expiry: a dialog answered every 20 minutes never runs out.
func TestDialogStore_SaveSlidesTheExpiry(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	s := newMemDialogStore(clock)

	if err := s.Save(ctx, 1, nil, "team", "email", sampleDialog{}, dialogTTL); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for i := 0; i < 4; i++ {
		clock.advance(20 * time.Minute)
		if _, found, _, err := s.Load(ctx, 1, "team", nil); err != nil || !found {
			t.Fatalf("answer %d, 20 min after the last: found=%v err=%v", i, found, err)
		}
		if err := s.Save(ctx, 1, nil, "team", "email", sampleDialog{}, dialogTTL); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	clock.advance(dialogTTL + time.Second)
	if _, found, expired, _ := s.Load(ctx, 1, "team", nil); found || !expired {
		t.Fatalf("30 minutes of silence must expire it: found=%v expired=%v", found, expired)
	}
}

// Kinds and accounts are separate dialogs: one never overwrites or expires another.
func TestDialogStore_KindsAndAccountsDoNotCollide(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	s := newMemDialogStore(clock)

	_ = s.Save(ctx, 1, nil, "team", "email", sampleDialog{Email: "team"}, dialogTTL)
	_ = s.Save(ctx, 1, nil, "promo", "code", sampleDialog{Email: "promo"}, time.Minute)
	_ = s.Save(ctx, 2, nil, "team", "role", sampleDialog{Email: "other"}, dialogTTL)

	clock.advance(2 * time.Minute) // only the promo dialog has run out
	var got sampleDialog
	if step, found, _, _ := s.Load(ctx, 1, "team", &got); !found || step != "email" || got.Email != "team" {
		t.Fatalf("account 1 team: found=%v step=%q %+v", found, step, got)
	}
	if _, found, expired, _ := s.Load(ctx, 1, "promo", nil); found || !expired {
		t.Fatalf("account 1 promo must be expired: found=%v expired=%v", found, expired)
	}
	got = sampleDialog{}
	if step, found, _, _ := s.Load(ctx, 2, "team", &got); !found || step != "role" || got.Email != "other" {
		t.Fatalf("account 2 team: found=%v step=%q %+v", found, step, got)
	}
}

// A store without a database says so instead of panicking.
func TestDialogStore_NoDatabaseIsAnError(t *testing.T) {
	ctx := context.Background()
	s := newPGDialogStore(nil)
	if err := s.Save(ctx, 1, nil, "team", "email", nil, dialogTTL); !errors.Is(err, errNoDialogDatabase) {
		t.Fatalf("Save: %v", err)
	}
	if _, _, _, err := s.Load(ctx, 1, "team", nil); !errors.Is(err, errNoDialogDatabase) {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Delete(ctx, 1, "team"); !errors.Is(err, errNoDialogDatabase) {
		t.Fatalf("Delete: %v", err)
	}
}

// Parallel Loads of one expired dialog: exactly one reports "expired", the
// rest see a dialog that never existed.
func TestDialogStore_ParallelLoadsReportExpiryOnce(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	s := newMemDialogStore(clock)
	if err := s.Save(ctx, 1, nil, "team", "email", sampleDialog{}, dialogTTL); err != nil {
		t.Fatalf("Save: %v", err)
	}
	clock.advance(dialogTTL)

	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	reported := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, found, expired, err := s.Load(ctx, 1, "team", nil)
			if err != nil || found {
				t.Errorf("found=%v err=%v", found, err)
				return
			}
			if expired {
				mu.Lock()
				reported++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if reported != 1 {
		t.Fatalf("expiry reported %d times, want exactly once", reported)
	}
}

// racingDialogTable lets a Save land between Load's read and its delete.
type racingDialogTable struct {
	*memDialogTable
	beforeDelete func()
}

func (r *racingDialogTable) DeleteBotDialogIfExpired(ctx context.Context, tg int64, kind string, now time.Time) (int64, error) {
	if r.beforeDelete != nil {
		r.beforeDelete()
	}
	return r.memDialogTable.DeleteBotDialogIfExpired(ctx, tg, kind, now)
}

// A Save that revives the dialog after Load read it as expired is never
// erased: Load answers with the revived dialog.
func TestDialogStore_SaveBetweenReadAndDeleteSurvives(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	mem := &memDialogTable{rows: map[memDialogKey]gen.BotDialogRow{}}
	racing := &racingDialogTable{memDialogTable: mem}
	s := &pgDialogStore{q: racing, now: clock.now}

	if err := s.Save(ctx, 1, nil, "team", "email", sampleDialog{Email: "old"}, dialogTTL); err != nil {
		t.Fatalf("Save: %v", err)
	}
	clock.advance(dialogTTL)
	racing.beforeDelete = func() {
		racing.beforeDelete = nil
		if err := s.Save(ctx, 1, nil, "team", "role", sampleDialog{Email: "new"}, dialogTTL); err != nil {
			t.Errorf("racing Save: %v", err)
		}
	}

	var got sampleDialog
	step, found, expired, err := s.Load(ctx, 1, "team", &got)
	if err != nil || !found || expired || step != "role" || got.Email != "new" {
		t.Fatalf("revived dialog: step=%q found=%v expired=%v err=%v got=%+v", step, found, expired, err, got)
	}
	if _, found, _, _ := s.Load(ctx, 1, "team", nil); !found {
		t.Fatal("the revived dialog was erased")
	}
}
