package invitations

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type fakeChannels struct {
	insertID  uuid.UUID
	insertErr error
	getID     uuid.UUID
	getErr    error
	name      string
}

func (f *fakeChannels) InsertInternalChannel(_ context.Context, _ uuid.UUID, name string) (uuid.UUID, error) {
	f.name = name
	return f.insertID, f.insertErr
}

func (f *fakeChannels) GetInternalChannelID(context.Context, uuid.UUID) (uuid.UUID, error) {
	return f.getID, f.getErr
}

func TestEnsureInternalChannel_CreatesOrReadsTheWinner(t *testing.T) {
	ctx := context.Background()
	org := uuid.New()

	created := uuid.New()
	f := &fakeChannels{insertID: created}
	got, err := ensureInternalChannel(ctx, f, org)
	if err != nil || got != created {
		t.Fatalf("first create: got %v, %v", got, err)
	}
	if f.name != InternalChannelName {
		t.Fatalf("name = %q", f.name)
	}

	winner := uuid.New()
	f = &fakeChannels{insertErr: pgx.ErrNoRows, getID: winner}
	if got, err = ensureInternalChannel(ctx, f, org); err != nil || got != winner {
		t.Fatalf("lost race: got %v, %v", got, err)
	}

	boom := errors.New("boom")
	f = &fakeChannels{insertErr: boom}
	if _, err = ensureInternalChannel(ctx, f, org); !errors.Is(err, boom) {
		t.Fatalf("insert failure: err = %v", err)
	}
	f = &fakeChannels{insertErr: pgx.ErrNoRows, getErr: boom}
	if _, err = ensureInternalChannel(ctx, f, org); !errors.Is(err, boom) {
		t.Fatalf("read failure: err = %v", err)
	}
	if _, err = ensureInternalChannel(ctx, &fakeChannels{}, uuid.Nil); err == nil {
		t.Fatal("nil organization accepted")
	}
}
