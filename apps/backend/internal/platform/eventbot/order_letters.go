package eventbot

// order_letters.go — the order card's lines about the letters the buyer got
// (owner decision 2026-10-13: the organizer did not see whether the tickets
// reached the buyer). Every letter — the tickets, the refund — goes through
// ONE rendering, letterLine, from a letterStatus. The statuses come from the
// order's letters[] (the e-mail provider's delivery events: delivered,
// opened, delayed, bounced, stuck); the older delivery[] fold is kept only
// for an order card that carries no letters.

import (
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// Letter states, in the order a letter goes through them.
const (
	letterNone          = "none"           // nothing was ever queued (no address, e-mail off)
	letterOff           = "off"            // the person chose no letter (a refund)
	letterQueued        = "queued"         // waiting to be sent
	letterPartialQueued = "partial_queued" // some sent, the rest waiting
	letterSent          = "sent"           // handed to the mail service
	letterDelivered     = "delivered"      // the buyer's mail server took it
	letterOpened        = "opened"         // the buyer opened it
	letterPartial       = "partial"        // some sent, the rest failed
	letterFailed        = "failed"         // not sent
	letterDelayed       = "delayed"        // the buyer's server asked to try again later
	letterStuck         = "stuck"          // sent hours ago and still not delivered
	letterBounced       = "bounced"        // sent, but the buyer's server refused it
)

// letterFailedMark marks an orders-list row whose ticket letter did not go
// out (the legend bot.ord.legend explains it).
const letterFailedMark = "⚠️📧"

// letterRank orders the steps a sent letter goes through, so the card names
// the step EVERY ticket's letter reached.
var letterRank = map[string]int{letterSent: 1, letterDelivered: 2, letterOpened: 3}

// letterStatus is one letter as the card speaks about it.
type letterStatus struct {
	State string
	At    *time.Time // when the latest step happened
	To    string     // the address it went to
	Done  int        // tickets whose letter went out
	Total int        // tickets a letter was meant for
	Why   string     // the last failure, cut to a line
}

// failed reports a letter the organizer should act on.
func (s letterStatus) failed() bool {
	switch s.State {
	case letterFailed, letterPartial, letterBounced, letterStuck:
		return true
	}
	return false
}

// orderRowLetterMark reports whether an orders-list row gets letterFailedMark:
// the server's letter_failed (a letter failed, bounced or is stuck), or a
// ticket letter that never went out.
func orderRowLetterMark(o openapi.OrderListItem) bool {
	return o.LetterFailed || (o.DeliveryState != nil && *o.DeliveryState == letterFailed)
}

// ticketLetterStatus folds the order's ticket letters from letters[].
func ticketLetterStatus(d openapi.OrderDetail) letterStatus {
	st := letterStatus{State: letterNone, To: strings.TrimSpace(deref(d.BuyerEmail))}
	pending, failed, bounced, low := 0, 0, 0, 0
	delayed, stuck, seen := false, false, false
	for _, l := range d.Letters {
		if l.Kind != "ticket" {
			continue
		}
		seen = true
		n := len(l.TicketIds)
		if n == 0 {
			n = 1
		}
		st.Total += n
		if to := strings.TrimSpace(l.Recipient); to != "" {
			st.To = to
		}
		switch {
		case l.Status == letterQueued:
			pending += n
		case l.Status == letterFailed:
			if l.SentAt != nil {
				bounced += n
			} else {
				failed += n
			}
			if st.Why == "" {
				st.Why = cutRunes(strings.Join(strings.Fields(deref(l.FailureReason)), " "), 120)
			}
		case letterRank[l.Status] > 0 || l.Status == letterDelayed:
			st.Done += n
			rank := letterRank[l.Status]
			if l.Status == letterDelayed {
				rank, delayed = letterRank[letterSent], true
			}
			if low == 0 || rank < low {
				low = rank
			}
			stuck = stuck || l.Stuck
			if at := letterStepTime(l); at != nil && (st.At == nil || at.After(*st.At)) {
				st.At = at
			}
		}
	}
	if !seen {
		return ticketLetterStatusFromDelivery(d)
	}
	switch {
	case st.Total == 0:
		st.State = letterNone
	case st.Done == st.Total && stuck:
		st.State = letterStuck
	case st.Done == st.Total && delayed:
		st.State = letterDelayed
	case st.Done == st.Total:
		for state, rank := range letterRank {
			if rank == low {
				st.State = state
			}
		}
	case pending > 0 && st.Done > 0:
		st.State = letterPartialQueued
	case pending > 0:
		st.State = letterQueued
	case st.Done > 0:
		st.State = letterPartial
	case failed == 0 && bounced > 0:
		st.State = letterBounced
	default:
		st.State = letterFailed
	}
	return st
}

// letterStepTime is when a sent letter reached the step its status names.
func letterStepTime(l openapi.OrderLetter) *time.Time {
	switch l.Status {
	case letterOpened:
		if l.OpenedAt != nil {
			return l.OpenedAt
		}
	case letterDelivered:
		for _, t := range []*time.Time{l.DeliveredAt, l.ProxyOpenedAt, l.ClickedAt} {
			if t != nil {
				return t
			}
		}
	case letterDelayed:
		if l.DeferredAt != nil {
			return l.DeferredAt
		}
	}
	return l.SentAt
}

// ticketLetterStatusFromDelivery folds delivery[] (the delivery jobs): only
// queued, sent and failed are known there.
func ticketLetterStatusFromDelivery(d openapi.OrderDetail) letterStatus {
	st := letterStatus{State: letterNone, To: strings.TrimSpace(deref(d.BuyerEmail))}
	pending, failed, low := 0, 0, 0
	for _, e := range d.Delivery {
		s := e.Status
		if s == "processing" {
			s = letterQueued
		}
		if s == "pending" {
			s = letterQueued
		}
		switch {
		case letterRank[s] > 0:
			st.Total++
			st.Done++
			if low == 0 || letterRank[s] < low {
				low = letterRank[s]
			}
			if e.SentAt != nil && (st.At == nil || e.SentAt.After(*st.At)) {
				st.At = e.SentAt
			}
			if to := strings.TrimSpace(deref(e.RecipientEmail)); to != "" {
				st.To = to
			}
		case s == letterQueued:
			st.Total++
			pending++
		case s == letterFailed:
			st.Total++
			failed++
			if e.LastError != nil && st.Why == "" {
				st.Why = cutRunes(strings.Join(strings.Fields(*e.LastError), " "), 120)
			}
		}
	}
	switch {
	case st.Total == 0:
		st.State = letterNone
	case st.Done == st.Total:
		for state, rank := range letterRank {
			if rank == low {
				st.State = state
			}
		}
	case pending > 0 && st.Done > 0:
		st.State = letterPartialQueued
	case pending > 0:
		st.State = letterQueued
	case st.Done > 0:
		st.State = letterPartial
	default:
		st.State = letterFailed
	}
	return st
}

// refundLetterStatus is the refund letter of the order, or "" State when
// the order has no refund operation that could write to the buyer.
func refundLetterStatus(d openapi.OrderDetail) letterStatus {
	r := d.RefundLetter
	if r == nil {
		return letterStatus{}
	}
	st := letterStatus{At: r.At, To: strings.TrimSpace(deref(d.BuyerEmail)), Done: r.Sent, Total: r.Refunds}
	switch r.State {
	case "off":
		st.State = letterOff
	case "queued":
		st.State = letterQueued
	case "partial":
		st.State = letterPartialQueued
	case "sent":
		st.State = letterSent
		withLatestRefundLetter(&st, d.Letters)
	case "failed":
		st.State = letterFailed
	default:
		return letterStatus{}
	}
	return st
}

// withLatestRefundLetter moves a sent refund letter on to what the e-mail
// provider reported about the newest refund letter of the order: delivered,
// opened, delayed, bounced or stuck.
func withLatestRefundLetter(st *letterStatus, letters []openapi.OrderLetter) {
	var latest *openapi.OrderLetter
	for i := range letters {
		l := &letters[i]
		if l.Kind != "refund" || l.SentAt == nil {
			continue
		}
		if latest == nil || l.SentAt.After(*latest.SentAt) {
			latest = l
		}
	}
	if latest == nil {
		return
	}
	if to := strings.TrimSpace(latest.Recipient); to != "" {
		st.To = to
	}
	switch {
	case latest.Status == letterFailed:
		st.State = letterBounced
		st.At = latest.FailedAt
		st.Why = cutRunes(strings.Join(strings.Fields(deref(latest.FailureReason)), " "), 120)
	case latest.Stuck:
		st.State, st.At = letterStuck, latest.SentAt
	case latest.Status == letterDelayed || letterRank[latest.Status] > letterRank[letterSent]:
		st.State, st.At = latest.Status, letterStepTime(*latest)
	}
}

// relabelButton renames the button carrying data, or leaves kb as it is when
// no button carries it. The keyboard is copied, never changed in place.
func relabelButton(kb *models.InlineKeyboardMarkup, data, label string) *models.InlineKeyboardMarkup {
	if kb == nil {
		return nil
	}
	rows := make([][]models.InlineKeyboardButton, len(kb.InlineKeyboard))
	for i, row := range kb.InlineKeyboard {
		rows[i] = append([]models.InlineKeyboardButton(nil), row...)
		for j := range rows[i] {
			if rows[i][j].CallbackData == data {
				rows[i][j].Text = label
			}
		}
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// letterLine is one letter's line on the card. kind is "tickets" or
// "refund"; tz is the venue's zone for the time; private shows the address
// (never in a group chat). "" when there is nothing to say.
func (b *Bot) letterLine(loc, kind string, s letterStatus, tz string, private bool) string {
	if s.State == "" {
		return ""
	}
	to := ""
	if private {
		to = Esc(s.To)
	}
	when := ""
	if s.At != nil {
		when = Esc(FormatWhen(*s.At, tz))
	}
	return b.texts.T(loc, "bot.ord.letter_"+kind+"_"+s.State, map[string]any{
		"When": when, "To": to, "Done": s.Done, "Total": s.Total, "Why": Esc(s.Why),
	})
}
