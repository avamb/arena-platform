package invitations

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// fakeChannels answers GetInternalChannelID from a queue (the last answer
// repeats) and counts the inserts.
type fakeChannels struct {
	insertID  uuid.UUID
	insertErr error
	gets      []getAnswer
	getCalls  int
	inserts   int
	name      string
}

type getAnswer struct {
	id  uuid.UUID
	err error
}

func (f *fakeChannels) InsertInternalChannel(_ context.Context, _ uuid.UUID, name string) (uuid.UUID, error) {
	f.inserts++
	f.name = name
	return f.insertID, f.insertErr
}

func (f *fakeChannels) GetInternalChannelID(context.Context, uuid.UUID) (uuid.UUID, error) {
	i := f.getCalls
	if i >= len(f.gets) {
		i = len(f.gets) - 1
	}
	f.getCalls++
	return f.gets[i].id, f.gets[i].err
}

func TestEnsureInternalChannel_ExistingChannelIsNeverInserted(t *testing.T) {
	existing := uuid.New()
	f := &fakeChannels{gets: []getAnswer{{id: existing}}}
	got, err := ensureInternalChannel(context.Background(), f, uuid.New())
	if err != nil || got != existing {
		t.Fatalf("got %v, %v", got, err)
	}
	if f.inserts != 0 {
		t.Fatalf("inserts = %d, want 0 (an insert burns a channel number)", f.inserts)
	}
}

func TestEnsureInternalChannel_CreatesOrReadsTheWinner(t *testing.T) {
	ctx := context.Background()
	org := uuid.New()
	noRows := getAnswer{err: pgx.ErrNoRows}

	created := uuid.New()
	f := &fakeChannels{gets: []getAnswer{noRows}, insertID: created}
	got, err := ensureInternalChannel(ctx, f, org)
	if err != nil || got != created {
		t.Fatalf("first create: got %v, %v", got, err)
	}
	if f.name != InternalChannelName || f.inserts != 1 {
		t.Fatalf("name = %q inserts = %d", f.name, f.inserts)
	}

	winner := uuid.New()
	f = &fakeChannels{gets: []getAnswer{noRows, {id: winner}}, insertErr: pgx.ErrNoRows}
	if got, err = ensureInternalChannel(ctx, f, org); err != nil || got != winner {
		t.Fatalf("lost race: got %v, %v", got, err)
	}

	boom := errors.New("boom")
	f = &fakeChannels{gets: []getAnswer{noRows}, insertErr: boom}
	if _, err = ensureInternalChannel(ctx, f, org); !errors.Is(err, boom) {
		t.Fatalf("insert failure: err = %v", err)
	}
	f = &fakeChannels{gets: []getAnswer{noRows, {err: boom}}, insertErr: pgx.ErrNoRows}
	if _, err = ensureInternalChannel(ctx, f, org); !errors.Is(err, boom) {
		t.Fatalf("re-read failure: err = %v", err)
	}
	f = &fakeChannels{gets: []getAnswer{{err: boom}}}
	if _, err = ensureInternalChannel(ctx, f, org); !errors.Is(err, boom) || f.inserts != 0 {
		t.Fatalf("read failure: err = %v inserts = %d", err, f.inserts)
	}
	if _, err = ensureInternalChannel(ctx, &fakeChannels{gets: []getAnswer{{}}}, uuid.Nil); err == nil {
		t.Fatal("nil organization accepted")
	}
}
