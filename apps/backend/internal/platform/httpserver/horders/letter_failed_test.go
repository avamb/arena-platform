package horders

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
)

// TestAnyLetterFailed_SameRuleAsTheList (review 2026-10-14 L1): the card's
// letter_failed forgives a change, cancellation or refund letter that a
// newer letter of the same kind replaced, as the orders list does.
func TestAnyLetterFailed_SameRuleAsTheList(t *testing.T) {
	t.Parallel()
	at := func(s string) *string { return &s }
	failedChange := emailstatus.LetterView{Kind: emailstatus.KindChange, Status: emailstatus.StatusFailed,
		SentAt: at("2026-10-13T10:00:00Z")}
	newerChange := emailstatus.LetterView{Kind: emailstatus.KindChange, Status: emailstatus.StatusDelivered,
		SentAt: at("2026-10-13T12:00:00Z")}
	olderRefund := emailstatus.LetterView{Kind: emailstatus.KindRefund, Status: emailstatus.StatusDelivered,
		SentAt: at("2026-10-12T09:00:00Z")}
	stuckTicket := emailstatus.LetterView{Kind: emailstatus.KindTicket, Status: emailstatus.StatusSent, Stuck: true,
		SentAt: at("2026-10-13T08:00:00Z")}
	failedTicket := emailstatus.LetterView{Kind: emailstatus.KindTicket, Status: emailstatus.StatusFailed}

	cases := []struct {
		name    string
		letters []emailstatus.LetterView
		want    bool
	}{
		{"failed change alone", []emailstatus.LetterView{failedChange}, true},
		{"failed change replaced by a newer change", []emailstatus.LetterView{failedChange, newerChange}, false},
		{"another kind does not replace it", []emailstatus.LetterView{olderRefund, failedChange}, true},
		{"stuck ticket letter", []emailstatus.LetterView{stuckTicket}, true},
		{"failed ticket letter is never forgiven", []emailstatus.LetterView{failedTicket, stuckTicket}, true},
		{"all fine", []emailstatus.LetterView{newerChange, olderRefund}, false},
	}
	for _, c := range cases {
		if got := anyLetterFailed(c.letters); got != c.want {
			t.Errorf("%s: anyLetterFailed = %v, want %v", c.name, got, c.want)
		}
	}
}
