package seatlayout

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func strp(s string) *string { return &s }

func TestDecide_Rules(t *testing.T) {
	t1, t2 := uuid.New(), uuid.New()
	seat := func(status, kind string, tier *uuid.UUID) *gen.SessionSeatRow {
		return &gen.SessionSeatRow{SeatKey: "S|A|1", Status: status, Kind: kind, TierID: tier}
	}
	paint := Target{SeatKey: "S|A|1", SetTier: true, TierID: &t2}
	closeT := Target{SeatKey: "S|A|1", Status: strp(StatusUnavailable)}
	expectAvailT1 := SeatState{TierID: &t1, Status: StatusAvailable}
	all := func(uuid.UUID) bool { return true }
	none := func(uuid.UUID) bool { return false }

	cases := []struct {
		name        string
		row         *gen.SessionSeatRow
		t           Target
		tierOK      func(uuid.UUID) bool
		wantOutcome string
		wantReason  string
	}{
		{"missing row", nil, paint, all, OutcomeSkipped, ReasonNotFound},
		{"sold frozen", seat("sold", "seat", &t1), paint, all, OutcomeSkipped, ReasonSold},
		{"held frozen", seat("held", "seat", &t1), closeT, all, OutcomeSkipped, ReasonHeld},
		{"odd status", seat("weird", "seat", &t1), closeT, all, OutcomeSkipped, ReasonUnknownStatus},
		{"paint available", seat("available", "seat", &t1), paint, all, OutcomeApplied, ""},
		{"paint unavailable", seat("unavailable", "seat", &t1), paint, all, OutcomeApplied, ""},
		{"paint same tier noop", seat("available", "seat", &t2), paint, all, OutcomeNoop, ""},
		{"close available", seat("available", "seat", &t1), closeT, all, OutcomeApplied, ""},
		{"close unavailable noop", seat("unavailable", "seat", &t1), closeT, all, OutcomeNoop, ""},
		{"ga unit no repaint", seat("available", "ga_unit", &t1), paint, all, OutcomeSkipped, ReasonGAUnit},
		{"ga unit may close", seat("available", "ga_unit", &t1), closeT, all, OutcomeApplied, ""},
		{"revert expect ok", seat("available", "seat", &t1),
			Target{SeatKey: "S|A|1", SetTier: true, TierID: &t2, Status: strp(StatusAvailable), Expect: &expectAvailT1}, all, OutcomeApplied, ""},
		{"revert changed since", seat("unavailable", "seat", &t1),
			Target{SeatKey: "S|A|1", SetTier: true, TierID: &t2, Status: strp(StatusAvailable), Expect: &expectAvailT1}, all, OutcomeSkipped, ReasonChangedSince},
		{"revert sold before expect", seat("sold", "seat", &t1),
			Target{SeatKey: "S|A|1", SetTier: true, TierID: &t2, Expect: &expectAvailT1}, all, OutcomeSkipped, ReasonSold},
		{"revert deleted tier", seat("available", "seat", &t1),
			Target{SeatKey: "S|A|1", SetTier: true, TierID: &t2, Expect: &expectAvailT1}, none, OutcomeSkipped, ReasonTierUnavailable},
		{"revert to null tier", seat("available", "seat", &t1),
			Target{SeatKey: "S|A|1", SetTier: true, TierID: nil, Expect: &expectAvailT1}, none, OutcomeApplied, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Decide(tc.row, tc.t, tc.tierOK)
			if r.Outcome != tc.wantOutcome || r.Reason != tc.wantReason {
				t.Fatalf("Decide = %s/%s, want %s/%s", r.Outcome, r.Reason, tc.wantOutcome, tc.wantReason)
			}
			if r.Outcome == OutcomeApplied && r.From.Equal(r.To) {
				t.Fatalf("applied with from == to: %+v", r)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tier := uuid.New()
	ok := []BatchInput{
		{Kind: KindPaint, Changes: []Change{{SeatKey: "a", TierID: &tier}}},
		{Kind: KindClose, Changes: []Change{{SeatKey: "a", Status: strp(StatusUnavailable)}}},
		{Kind: KindOpen, Changes: []Change{{SeatKey: "a", Status: strp(StatusAvailable)}}},
		{Kind: KindApplyLayout, Changes: []Change{{SeatKey: "a", TierID: &tier, Status: strp(StatusUnavailable)}}},
		{Kind: KindClose, Changes: []Change{
			{SeatKey: "a", Status: strp(StatusUnavailable)}, {SeatKey: "a", Status: strp(StatusUnavailable)},
		}},
	}
	for i, in := range ok {
		if err := Validate(in); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	bad := map[string]BatchInput{
		"seat_batch.invalid_kind":       {Kind: KindRevert, Changes: []Change{{SeatKey: "a", TierID: &tier}}},
		"seat_batch.no_changes":         {Kind: KindPaint},
		"seat_batch.invalid_change":     {Kind: KindPaint, Changes: []Change{{SeatKey: "a", Status: strp(StatusAvailable)}}},
		"seat_batch.duplicate_seat_key": {Kind: KindApplyLayout, Changes: []Change{{SeatKey: "a", Status: strp(StatusAvailable)}, {SeatKey: "a", Status: strp(StatusUnavailable)}}},
	}
	for code, in := range bad {
		var ie *InputError
		if err := Validate(in); !errors.As(err, &ie) || ie.Code != code {
			t.Fatalf("%s: got %v", code, err)
		}
	}
	if err := Validate(BatchInput{Kind: KindApplyLayout, Changes: []Change{{SeatKey: "a", Status: strp("sold")}}}); err == nil {
		t.Fatal("status sold must be refused")
	}
	if err := Validate(BatchInput{Kind: KindPaint, NewTiers: []NewTier{{Name: "X"}}, Changes: []Change{{SeatKey: "a", TierID: &tier}}}); !errors.Is(err, ErrNewTiersUnsupported) {
		t.Fatalf("new tiers: %v", err)
	}
	many := make([]Change, MaxChanges+1)
	for i := range many {
		many[i] = Change{SeatKey: uuid.NewString(), TierID: &tier}
	}
	var ie *InputError
	if err := Validate(BatchInput{Kind: KindPaint, Changes: many}); !errors.As(err, &ie) || ie.Code != "seat_batch.too_many_changes" {
		t.Fatalf("too many: %v", err)
	}
}

func TestSeatProbe_EscapesTheKey(t *testing.T) {
	got := string(SeatProbe(`a"b\c`))
	if got != `[{"seat_key":"a\"b\\c"}]` {
		t.Fatalf("probe = %s", got)
	}
}

// TestKinds_MatchMigrationCheckConstraint keeps the Go kinds and the CHECK
// of session_seat_batches.kind (migration 0162) in step.
func TestKinds_MatchMigrationCheckConstraint(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", "migrations", "sql", "0162_session_seat_batches.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	m := regexp.MustCompile(`CHECK \(kind IN \(([^)]*)\)\)`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("kind CHECK not found in migration 0162")
	}
	var sqlKinds []string
	for _, part := range strings.Split(string(m[1]), ",") {
		sqlKinds = append(sqlKinds, strings.Trim(strings.TrimSpace(part), "'"))
	}
	if len(sqlKinds) != len(Kinds) {
		t.Fatalf("migration kinds %v, Go kinds %v", sqlKinds, Kinds)
	}
	for i, k := range Kinds {
		if string(k) != sqlKinds[i] {
			t.Fatalf("kind %d: Go %q, migration %q", i, k, sqlKinds[i])
		}
	}
}
