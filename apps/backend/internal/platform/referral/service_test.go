package referral

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

func TestResultOf(t *testing.T) {
	for _, tc := range []struct {
		name       string
		refs       []refundView
		wantState  string
		wantReason string
		wantAmount int64
	}{
		{"succeeded", []refundView{{State: refunds.StateSucceeded, Amount: 500}}, StatePaid, "", 500},
		{"pending with provider id", []refundView{{State: refunds.StateProviderPending, Amount: 500, HasProviderRef: true}}, StateSent, "", 500},
		{"pending, unknown outcome", []refundView{{State: refunds.StateProviderPending, Amount: 500}}, StateSent, "", 500},
		{"declined by the provider", []refundView{{State: refunds.StateFailed, Amount: 500, FailureCode: "card_closed"}}, StateNotPossible, "card_closed", 0},
		{"failed without a code", []refundView{{State: refunds.StateFailed, Amount: 500}}, StateNotPossible, "provider_declined", 0},
		{"manual review", []refundView{{State: refunds.StateManualReview, Amount: 500}}, StateNotPossible, "manual_review", 0},
		{"rejected", []refundView{{State: refunds.StateRejected, Amount: 500}}, StateNotPossible, "provider_declined", 0},
		{"two tickets, both succeeded", []refundView{{State: refunds.StateSucceeded, Amount: 300}, {State: refunds.StateSucceeded, Amount: 200}}, StatePaid, "", 500},
		{"one succeeded, one declined keeps what was returned", []refundView{{State: refunds.StateSucceeded, Amount: 300}, {State: refunds.StateFailed, Amount: 200, FailureCode: "x"}}, StatePaid, "partial", 300},
		{"one pending wins over a decline", []refundView{{State: refunds.StateProviderPending, Amount: 300}, {State: refunds.StateFailed, Amount: 200}}, StateSent, "", 300},
	} {
		state, reason, amount := resultOf(tc.refs)
		if state != tc.wantState || reason != tc.wantReason || amount != tc.wantAmount {
			t.Errorf("%s: resultOf = (%s, %q, %d); want (%s, %q, %d)", tc.name, state, reason, amount, tc.wantState, tc.wantReason, tc.wantAmount)
		}
	}
}

func TestFriendOrderOutcome(t *testing.T) {
	if friendOrderOutcome("paid") != nil {
		t.Error("a paid friend order pays the cashback")
	}
	for _, st := range []string{"refunded", "partially_refunded"} {
		oc := friendOrderOutcome(st)
		if oc == nil || oc.state != StateCancelled || oc.reason != "friend_refunded" {
			t.Errorf("%s: outcome = %+v; want cancelled friend_refunded", st, oc)
		}
	}
	for _, st := range []string{"pending_payment", "manual_review"} {
		if oc := friendOrderOutcome(st); oc == nil || !oc.retry {
			t.Errorf("%s: outcome = %+v; want a retry", st, oc)
		}
	}
	for _, st := range []string{"cancelled", "expired", "abandoned"} {
		oc := friendOrderOutcome(st)
		if oc == nil || oc.state != StateCancelled || oc.reason != "friend_not_paid" {
			t.Errorf("%s: outcome = %+v; want cancelled friend_not_paid", st, oc)
		}
	}
}

// The reward states and the refund origin of the migration and of Go must stay
// equal (AGENTS.md: a CHECK list and its Go mirror drift silently).
func TestMigrationMirrorsTheGoConstants(t *testing.T) {
	matches, err := filepath.Glob("../../migrations/sql/*_referral_program.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("referral migration not found: %v %v", matches, err)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	i := strings.Index(sql, "CHECK (state IN")
	if i < 0 {
		t.Fatal("reward state CHECK not found in the migration")
	}
	rest := sql[i:]
	rest = rest[:strings.Index(rest, "),")]
	var states []string
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(rest, -1) {
		states = append(states, m[1])
	}
	want := []string{StateCancelled, StateCapped, StateDue, StateFailed, StateNotPossible, StatePaid, StateSent}
	slices.Sort(states)
	slices.Sort(want)
	if !slices.Equal(states, want) {
		t.Errorf("reward states: migration %v, Go %v", states, want)
	}
	if !strings.Contains(sql, "'"+refunds.OriginReferralCashback+"'") {
		t.Errorf("the migration does not allow refunds.origin %q", refunds.OriginReferralCashback)
	}
}
