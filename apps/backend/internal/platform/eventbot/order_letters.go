package eventbot

// order_letters.go — the order card's lines about the letters the buyer got
// (owner decision 2026-10-13: the organizer did not see whether the tickets
// reached the buyer). Every letter — the tickets, the refund — goes through
// ONE rendering, letterLine, from a letterStatus. Today the server knows
// queued / sent / failed; when the delivery events of the e-mail provider
// arrive (delivered, opened), only ticketLetterStatus needs to learn them:
// the states, their order and their texts are already here.

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
func (s letterStatus) failed() bool { return s.State == letterFailed || s.State == letterPartial }

// ticketLetterStatus folds the order's ticket letters.
func ticketLetterStatus(d openapi.OrderDetail) letterStatus {
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
	case "failed":
		st.State = letterFailed
	default:
		return letterStatus{}
	}
	return st
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
