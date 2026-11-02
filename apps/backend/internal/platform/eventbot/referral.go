package eventbot

// referral.go — the pure parts of the "Refer a friend" screens (spec 52
// section 5.2 and 7.1): reading a typed percent or a friend count, the steps of
// the turn-on dialog as a state machine, and the money lines of the summary.
// Nothing here talks to Telegram or to arena-api, so it is tested on its own
// (referral_test.go). The rules themselves (ranges, terms, the capability of
// the channels) are arena-api's; this file only keeps the bot from sending a
// value it can see is wrong.

import (
	"sort"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The steps of the dialog (bot_dialogs.step). The card, the end question and
// the rewards list take no typed text; the four questions do.
const (
	rfStepCard     = "rf_card"
	rfStepFriend   = "rf_friend"
	rfStepCashback = "rf_cashback"
	rfStepCap      = "rf_cap"
	rfStepMax      = "rf_max"
	rfStepRules    = "rf_rules"
	rfStepEnd      = "rf_end"
	rfStepRewards  = "rf_rewards"
)

// maxReferralFriends bounds the friends-per-code answer (the API takes any
// positive number; a typo with ten digits is not a limit anyone meant).
const maxReferralFriends = 100000

// referralDraft is the answers of the turn-on (or change) dialog so far.
type referralDraft struct {
	Step     string `json:"step"`
	Friend   int    `json:"friend,omitempty"`
	Cashback int    `json:"cashback,omitempty"`
	Cap      int    `json:"cap,omitempty"`
	Max      *int   `json:"max,omitempty"`
	// Editing is true when the event has a live programme: its values are
	// offered as "keep" buttons, and the terms were accepted already.
	Editing bool `json:"editing,omitempty"`
	Old     struct {
		Friend, Cashback, Cap int
		Max                   *int
	} `json:"old"`
}

// newReferralDraft starts the dialog. A live programme (active or paused)
// is edited; no programme, or an ended one, starts a new one.
func newReferralDraft(p *openapi.ReferralProgram) *referralDraft {
	d := &referralDraft{Step: rfStepFriend, Cap: 100}
	if p != nil && p.Status != referralStatusEnded {
		d.Editing = true
		d.Old.Friend, d.Old.Cashback, d.Old.Cap, d.Old.Max = p.FriendDiscountPct, p.CashbackPct, p.CashbackCapPct, p.MaxFriends
	}
	return d
}

// parsePercent reads a whole percent from 1 to 100: "15", " 15 % ", "15%".
func parsePercent(s string) (int, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 100 {
		return 0, false
	}
	return n, true
}

// parseFriends reads the number of friends one code serves.
func parseFriends(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > maxReferralFriends {
		return 0, false
	}
	return n, true
}

// Message keys the state machine answers with.
const (
	rfErrPercent = "bot.rfp.err_percent"
	rfErrFriends = "bot.rfp.err_friends"
	rfUseButtons = "bot.rfp.use_buttons"
)

// rfApply feeds a typed text or a button's data to the dialog. It returns the
// key of a message to show above the next screen ("" when the answer was taken
// and the dialog moved on). A text on a step that waits for a button is
// answered with a hint, never guessed at.
//
// data: "keep" keeps the stored value of an edited programme, "cap100" is the
// no-cap answer, "nomax" is no limit on friends.
func rfApply(d *referralDraft, text, data string) string {
	switch d.Step {
	case rfStepFriend, rfStepCashback:
		n, ok := rfPercentAnswer(d, text, data)
		if !ok {
			return rfErrPercent
		}
		if d.Step == rfStepFriend {
			d.Friend, d.Step = n, rfStepCashback
		} else {
			d.Cashback, d.Step = n, rfStepCap
		}
	case rfStepCap:
		n := 0
		ok := false
		switch {
		case data == "cap100":
			n, ok = 100, true
		case data == "keep" && d.Editing:
			n, ok = d.Old.Cap, true
		case data == "" && text != "":
			n, ok = parsePercent(text)
		}
		if !ok {
			return rfErrPercent
		}
		d.Cap, d.Step = n, rfStepMax
	case rfStepMax:
		switch {
		case data == "nomax":
			d.Max = nil
		case data == "keep" && d.Editing:
			d.Max = d.Old.Max
		case data == "" && text != "":
			n, ok := parseFriends(text)
			if !ok {
				return rfErrFriends
			}
			d.Max = &n
		default:
			return rfErrFriends
		}
		d.Step = rfStepRules
	case rfStepRules:
		if text != "" {
			return rfUseButtons
		}
	}
	return ""
}

// rfPercentAnswer reads the friend-discount and cashback answers.
func rfPercentAnswer(d *referralDraft, text, data string) (int, bool) {
	switch {
	case data == "keep" && d.Editing:
		if d.Step == rfStepFriend {
			return d.Old.Friend, d.Old.Friend > 0
		}
		return d.Old.Cashback, d.Old.Cashback > 0
	case data == "" && text != "":
		return parsePercent(text)
	}
	return 0, false
}

// rfTakesText reports whether a step takes typed text: the four questions
// read it as an answer, the rules screen answers it with a hint to use the
// button (a stray "yes" never turns the programme on).
func rfTakesText(step string) bool {
	switch step {
	case rfStepFriend, rfStepCashback, rfStepCap, rfStepMax, rfStepRules:
		return true
	}
	return false
}

// rfBack steps the dialog one question back; the first question has no
// "back" (the way out is "Cancel").
func rfBack(d *referralDraft) {
	switch d.Step {
	case rfStepCashback:
		d.Step = rfStepFriend
	case rfStepCap:
		d.Step = rfStepCashback
	case rfStepMax:
		d.Step = rfStepCap
	case rfStepRules:
		d.Step = rfStepMax
	}
}

// settings turns the finished draft into the PUT body. terms is true only for
// a start: an edited programme accepted them when it began.
func (d *referralDraft) settings() ReferralSettings {
	return ReferralSettings{
		FriendDiscountPct: d.Friend,
		CashbackPct:       d.Cashback,
		CashbackCapPct:    d.Cap,
		MaxFriends:        d.Max,
		AcceptTerms:       !d.Editing,
	}
}

// ─── summary ──────────────────────────────────────────────────────────────────

// referralMoneyRow is the money of one currency in the summary. Currencies are
// never added together.
type referralMoneyRow struct {
	Currency string
	Discount int64 // discount friends got
	Paid     int64 // cashback paid
	Way      int64 // cashback promised or sent, not yet confirmed
	Kept     int64 // cashback that stayed with the organizer
}

// referralMoneyRows folds the summary into one row per currency, sorted by
// currency; a currency with nothing in it is left out.
func referralMoneyRows(s openapi.ReferralSummary) []referralMoneyRow {
	rows := map[string]*referralMoneyRow{}
	row := func(cur string) *referralMoneyRow {
		cur = strings.ToUpper(strings.TrimSpace(cur))
		r := rows[cur]
		if r == nil {
			r = &referralMoneyRow{Currency: cur}
			rows[cur] = r
		}
		return r
	}
	for _, m := range s.DiscountGiven {
		row(m.Currency).Discount += m.AmountMinor
	}
	for _, c := range s.Cashback {
		r := row(c.Currency)
		r.Paid += c.PaidMinor
		r.Way += c.DueMinor + c.SentMinor
		r.Kept += c.CappedMinor + c.NotPossibleMinor + c.FailedMinor + c.CancelledMinor
	}
	var out []referralMoneyRow
	for _, r := range rows {
		if r.Discount != 0 || r.Paid != 0 || r.Way != 0 || r.Kept != 0 {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// rfFilters are the state chips of the rewards list, in the order shown.
var rfFilters = []string{"all", "due", "sent", "paid", "capped", "not_possible", "failed", "cancelled"}

// rfValidFilter reports whether data names a chip.
func rfValidFilter(f string) bool {
	for _, x := range rfFilters {
		if x == f {
			return true
		}
	}
	return false
}

// rfStateParam is the API's state filter for a chip ("" for all).
func rfStateParam(filter string) string {
	if filter == "all" {
		return ""
	}
	return filter
}

// rfCursorFor returns the cursor that fetches the given page of the rewards
// list. Cursors[i] fetches page i+2 (the next_cursor of page i+1); a page the
// state has no cursor for cannot be fetched.
func rfCursorFor(cursors []string, page int) (string, bool) {
	if page == 1 {
		return "", true
	}
	if page >= 2 && page-2 < len(cursors) {
		return cursors[page-2], true
	}
	return "", false
}

// rfKeepCursors returns the cursor stack after page was shown: everything
// below it, then the cursor of the page after it (when there is one).
func rfKeepCursors(cursors []string, page int, next string) []string {
	keep := page - 1
	if keep < 0 {
		keep = 0
	}
	if keep > len(cursors) {
		keep = len(cursors)
	}
	out := append([]string(nil), cursors[:keep]...)
	if next != "" {
		out = append(out, next)
	}
	return out
}
