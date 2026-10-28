package htickets

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

type fakeOriginReader struct {
	origin *string
	err    error
}

func (f fakeOriginReader) GetTicketSeatOrigin(context.Context, uuid.UUID) (*string, error) {
	return f.origin, f.err
}

func strPtr(s string) *string { return &s }

// TestSeatReleaseTarget_FollowsSeatOrigin pins owner decision R-2 of spec 46:
// only a place an invitation took from the reserve goes back to the reserve.
func TestSeatReleaseTarget_FollowsSeatOrigin(t *testing.T) {
	boom := errors.New("connection lost")
	cases := []struct {
		name    string
		reader  fakeOriginReader
		want    string
		wantErr error
	}{
		{"from the reserve", fakeOriginReader{origin: strPtr("unavailable")}, gen.SeatReleaseUnavailable, nil},
		{"from sale", fakeOriginReader{origin: strPtr("available")}, gen.SeatReleaseAvailable, nil},
		{"a purchase", fakeOriginReader{origin: nil}, gen.SeatReleaseAvailable, nil},
		{"legacy ticket without an order line", fakeOriginReader{err: pgx.ErrNoRows}, gen.SeatReleaseAvailable, nil},
		{"unknown value never closes a seat", fakeOriginReader{origin: strPtr("held")}, gen.SeatReleaseAvailable, nil},
		{"lookup failure aborts", fakeOriginReader{err: boom}, "", boom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SeatReleaseTargetTx(context.Background(), tc.reader, uuid.New())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("target = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestValidateSeatReleaseTarget_RefusesEverythingButTheTwoStates guards the
// *To release queries: a bad target is refused in Go, before any statement.
func TestValidateSeatReleaseTarget_RefusesEverythingButTheTwoStates(t *testing.T) {
	for _, ok := range []string{gen.SeatReleaseAvailable, gen.SeatReleaseUnavailable} {
		if err := gen.ValidateSeatReleaseTarget(ok); err != nil {
			t.Fatalf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "sold", "held", "AVAILABLE", "available "} {
		if err := gen.ValidateSeatReleaseTarget(bad); !errors.Is(err, gen.ErrInvalidSeatReleaseTarget) {
			t.Fatalf("%q: err = %v, want ErrInvalidSeatReleaseTarget", bad, err)
		}
	}
	// The query wrappers refuse before touching the database: a nil DBTX
	// would panic if they did not.
	q := gen.New(nil)
	if _, err := q.ReleaseSoldSessionSeatTo(context.Background(), uuid.New(), "A|1|1", 1, "sold"); !errors.Is(err, gen.ErrInvalidSeatReleaseTarget) {
		t.Fatalf("ReleaseSoldSessionSeatTo: err = %v", err)
	}
	if _, err := q.ReleaseSoldGAUnitBySeatKeyTo(context.Background(), uuid.New(), "ga|t1|000001", 1, ""); !errors.Is(err, gen.ErrInvalidSeatReleaseTarget) {
		t.Fatalf("ReleaseSoldGAUnitBySeatKeyTo: err = %v", err)
	}
}
