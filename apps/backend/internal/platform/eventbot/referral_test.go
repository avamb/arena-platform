package eventbot

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func TestParsePercent(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int{"1": 1, "15": 15, " 15 ": 15, "15%": 15, "15 %": 15, "100": 100, " 100 % ": 100} {
		got, ok := parsePercent(in)
		if !ok || got != want {
			t.Errorf("parsePercent(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "0", "101", "-5", "15.5", "15,5", "десять", "1 5", "%", "1e1", "+5x"} {
		if got, ok := parsePercent(in); ok {
			t.Errorf("parsePercent(%q) = %d, true; want a refusal", in, got)
		}
	}
}

func TestParseFriends(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int{"1": 1, " 25 ": 25, "100000": 100000} {
		if got, ok := parseFriends(in); !ok || got != want {
			t.Errorf("parseFriends(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "0", "-1", "100001", "99999999999999999999", "5.5", "many"} {
		if got, ok := parseFriends(in); ok {
			t.Errorf("parseFriends(%q) = %d, true; want a refusal", in, got)
		}
	}
}

// A new programme: the four questions, then the rules screen, and the finished
// draft says the terms are accepted.
func TestRfApply_NewProgrammeWalk(t *testing.T) {
	t.Parallel()
	d := newReferralDraft(nil)
	if d.Editing || d.Step != rfStepFriend || d.Cap != 100 {
		t.Fatalf("new draft = %+v", d)
	}
	steps := []struct {
		text, data, wantStep string
	}{
		{"10", "", rfStepCashback},
		{"20 %", "", rfStepCap},
		{"", "cap100", rfStepMax},
		{"", "nomax", rfStepRules},
	}
	for _, s := range steps {
		if key := rfApply(d, s.text, s.data); key != "" {
			t.Fatalf("answer %q/%q refused with %s", s.text, s.data, key)
		}
		if d.Step != s.wantStep {
			t.Fatalf("after %q/%q the step is %s, want %s", s.text, s.data, d.Step, s.wantStep)
		}
	}
	in := d.settings()
	want := ReferralSettings{FriendDiscountPct: 10, CashbackPct: 20, CashbackCapPct: 100, MaxFriends: nil, AcceptTerms: true}
	if !reflect.DeepEqual(in, want) {
		t.Errorf("settings = %+v, want %+v", in, want)
	}
}

func TestRfApply_RefusesBadAnswersAndStaysOnTheStep(t *testing.T) {
	t.Parallel()
	d := newReferralDraft(nil)
	for _, bad := range []string{"0", "101", "ten", "5.5", "-3"} {
		if key := rfApply(d, bad, ""); key != rfErrPercent {
			t.Errorf("friend %q -> %q, want %s", bad, key, rfErrPercent)
		}
		if d.Step != rfStepFriend {
			t.Fatalf("the step moved to %s after a refused answer", d.Step)
		}
	}
	// A button that does not belong to the step is not an answer.
	if key := rfApply(d, "", "nomax"); key != rfErrPercent || d.Step != rfStepFriend {
		t.Errorf("nomax on the friend step: key %q step %s", key, d.Step)
	}
	if key := rfApply(d, "", "keep"); key != rfErrPercent || d.Step != rfStepFriend {
		t.Errorf("keep without a programme: key %q step %s", key, d.Step)
	}
	d.Step = rfStepMax
	for _, bad := range []string{"0", "-1", "x", "100001", ""} {
		if key := rfApply(d, bad, ""); key != rfErrFriends || d.Step != rfStepMax {
			t.Errorf("max %q -> %q step %s", bad, key, d.Step)
		}
	}
	if key := rfApply(d, "12", ""); key != "" || d.Max == nil || *d.Max != 12 || d.Step != rfStepRules {
		t.Errorf("max 12 -> key %q max %v step %s", key, d.Max, d.Step)
	}
	// Text on the rules screen is answered with a hint and does not turn on.
	if key := rfApply(d, "yes", ""); key != rfUseButtons || d.Step != rfStepRules {
		t.Errorf("text on the rules screen: key %q step %s", key, d.Step)
	}
}

// Editing a live programme: "keep" takes the stored value, the terms are not
// asked again, an ended programme starts afresh.
func TestRfApply_EditKeepsStoredValues(t *testing.T) {
	t.Parallel()
	five := 5
	live := &openapi.ReferralProgram{Status: referralStatusPaused, FriendDiscountPct: 10, CashbackPct: 20, CashbackCapPct: 60, MaxFriends: &five}
	d := newReferralDraft(live)
	if !d.Editing || d.Old.Friend != 10 || d.Old.Cap != 60 || d.Old.Max == nil || *d.Old.Max != 5 {
		t.Fatalf("edit draft = %+v", d)
	}
	for _, step := range []string{rfStepCashback, rfStepCap, rfStepMax, rfStepRules} {
		if key := rfApply(d, "", "keep"); key != "" {
			t.Fatalf("keep refused with %s", key)
		}
		if d.Step != step {
			t.Fatalf("step %s, want %s", d.Step, step)
		}
	}
	got := d.settings()
	if got.FriendDiscountPct != 10 || got.CashbackPct != 20 || got.CashbackCapPct != 60 || got.MaxFriends == nil || *got.MaxFriends != 5 || got.AcceptTerms {
		t.Errorf("edited settings = %+v (terms must not be sent again)", got)
	}
	ended := &openapi.ReferralProgram{Status: referralStatusEnded, FriendDiscountPct: 10, CashbackPct: 20, CashbackCapPct: 100}
	if nd := newReferralDraft(ended); nd.Editing {
		t.Errorf("an ended programme must start a new one: %+v", nd)
	}
}

func TestRfBack(t *testing.T) {
	t.Parallel()
	d := &referralDraft{Step: rfStepRules}
	for _, want := range []string{rfStepMax, rfStepCap, rfStepCashback, rfStepFriend, rfStepFriend} {
		rfBack(d)
		if d.Step != want {
			t.Fatalf("back -> %s, want %s", d.Step, want)
		}
	}
}

func TestRfTakesText(t *testing.T) {
	t.Parallel()
	for step, want := range map[string]bool{
		rfStepFriend: true, rfStepCashback: true, rfStepCap: true, rfStepMax: true,
		rfStepRules: true, rfStepCard: false, rfStepEnd: false, rfStepRewards: false,
	} {
		if rfTakesText(step) != want {
			t.Errorf("rfTakesText(%s) = %v, want %v", step, !want, want)
		}
	}
}

// Money is shown per currency and never added across currencies.
func TestReferralMoneyRows_PerCurrencyNeverMixed(t *testing.T) {
	t.Parallel()
	s := openapi.ReferralSummary{
		DiscountGiven: []openapi.ReferralMoney{{Currency: "eur", AmountMinor: 500}, {Currency: "CZK", AmountMinor: 9000}},
		Cashback: []openapi.ReferralCashbackTotals{
			{Currency: "EUR", PaidMinor: 300, DueMinor: 100, SentMinor: 50, CappedMinor: 20, NotPossibleMinor: 10, FailedMinor: 5, CancelledMinor: 1},
			{Currency: "USD"}, // all zero: left out
		},
	}
	got := referralMoneyRows(s)
	want := []referralMoneyRow{
		{Currency: "CZK", Discount: 9000},
		{Currency: "EUR", Discount: 500, Paid: 300, Way: 150, Kept: 36},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v, want %+v", got, want)
	}
	if rows := referralMoneyRows(openapi.ReferralSummary{}); len(rows) != 0 {
		t.Errorf("an empty summary gave rows: %+v", rows)
	}
}

// The cursor stack: Cursors[i] fetches page i+2.
func TestRewardCursorStack(t *testing.T) {
	t.Parallel()
	var cur []string
	if c, ok := rfCursorFor(cur, 1); !ok || c != "" {
		t.Fatalf("page 1 = %q, %v", c, ok)
	}
	if _, ok := rfCursorFor(cur, 2); ok {
		t.Fatal("page 2 must not be reachable without a cursor")
	}
	cur = rfKeepCursors(cur, 1, "c2") // page 1 shown, next page has a cursor
	if c, ok := rfCursorFor(cur, 2); !ok || c != "c2" {
		t.Fatalf("page 2 = %q, %v", c, ok)
	}
	cur = rfKeepCursors(cur, 2, "c3")
	if !reflect.DeepEqual(cur, []string{"c2", "c3"}) {
		t.Fatalf("stack after page 2 = %v", cur)
	}
	// Back to page 1 forgets what lies beyond; the next page is found again.
	cur = rfKeepCursors(cur, 1, "c2b")
	if !reflect.DeepEqual(cur, []string{"c2b"}) {
		t.Fatalf("stack after going back = %v", cur)
	}
	// The last page has no next cursor.
	cur = rfKeepCursors(cur, 2, "")
	if !reflect.DeepEqual(cur, []string{"c2b"}) {
		t.Fatalf("stack on the last page = %v", cur)
	}
	if _, ok := rfCursorFor(cur, 3); ok {
		t.Fatal("page 3 must not be reachable past the last page")
	}
}

func TestRfFilters(t *testing.T) {
	t.Parallel()
	if !rfValidFilter("all") || !rfValidFilter("not_possible") || rfValidFilter("bogus") || rfValidFilter("") {
		t.Error("rfValidFilter is wrong")
	}
	if rfStateParam("all") != "" || rfStateParam("paid") != "paid" {
		t.Error("rfStateParam is wrong")
	}
}

// The event card's button is for the owner, the manager and the operator; the
// agent and a person with no organization get none.
func TestReferralEventRow_RolesThatSeeIt(t *testing.T) {
	t.Parallel()
	b := &Bot{}
	event := uuid.New()
	for role, want := range map[string]bool{membershipRoleOwner: true, membershipRoleManager: true, "agent": false} {
		row := b.referralEventRow("ru", &Identity{Current: &Membership{Role: role}}, event)
		if (row != nil) != want {
			t.Errorf("role %s: button present = %v, want %v", role, row != nil, want)
		}
		if row != nil && row[0].CallbackData != "rf:o:"+event.String() {
			t.Errorf("button data = %q", row[0].CallbackData)
		}
	}
	if b.referralEventRow("ru", &Identity{}, event) != nil || b.referralEventRow("ru", nil, event) != nil {
		t.Error("a person with no organization must get no button")
	}
	if row := b.referralEventRow("ru", &Identity{Superadmin: true, Current: &Membership{Role: "agent"}}, event); row == nil {
		t.Error("the platform operator must get the button")
	}
}
