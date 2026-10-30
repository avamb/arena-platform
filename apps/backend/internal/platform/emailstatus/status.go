// Package emailstatus records the buyer e-mails arena sends and folds the mail
// provider's webhook events into one delivery status per letter, so an
// organizer can see whether the ticket letter reached the buyer.
//
// A letter is born in the worker the moment it is handed to the SMTP relay
// (Begin, status "sending"), becomes "sent" when the relay accepted it
// (Accepted, with the provider's message id), and moves on as Brevo reports
// it: delivered, opened, failed. The status is never stored by "last event
// wins": every event type is kept once per letter with the first and last
// time the provider reported it, and Derive recomputes the status from the
// whole set. A duplicate or an event that arrives late therefore changes
// nothing it should not.
//
// "opened" deserves care. Brevo reports an open when the letter's tracking
// image is fetched. Apple Mail Privacy Protection fetches every image of
// every letter on delivery, through Apple's proxy, whether a human reads it
// or not; Brevo flags such fetches as proxy_open. A proxy open is therefore
// recorded as proof of DELIVERY (proxy_opened_at), never as reading. And an
// "opened" can still be a mail client that loads images automatically.
//
// The letters are also EVIDENCE for card disputes ("I never received the
// tickets"): see MinRetention.
package emailstatus

import (
	"strings"
	"time"
)

// MinRetention is how long letters, their events and their interactions are
// kept at the very least: 24 months, counted from the event date
// (RetentionAnchorSQL: the end of the letter's session when that is later
// than the letter, otherwise sent_at). Card networks let a buyer dispute a
// payment for up to 120-540 days after the purchase or the event, and the
// delivery chain of the ticket letter is part of arena's answer. Nothing may
// delete these rows earlier (tests/staticanalysis/email_letters_retention_test.go
// guards the code); the only deletions are letters the relay never accepted
// (status "sending").
const MinRetention = 2 * 365 * 24 * time.Hour

// Letter kinds (email_letters.kind).
const (
	KindTicket     = "ticket"
	KindInvitation = "invitation"
	KindChange     = "change"
	KindCancel     = "cancel"
	KindRefund     = "refund"
	// The letters of a merchandise shop order (migration 0179): they name
	// email_letters.shop_order_id, never order_id.
	KindShopReceived = "shop_received"
	KindShopShipped  = "shop_shipped"
	KindShopRefunded = "shop_refunded"
)

// Letter statuses as the API reports them. StatusQueued exists only in the
// order view (a ticket whose delivery job waits); StatusSending only in the
// table (the worker is handing the letter to the relay right now) and is
// never shown.
const (
	StatusQueued  = "queued"
	StatusSending = "sending"
	StatusSent    = "sent"
	// StatusDelayed: Brevo reported the message deferred (the recipient's
	// server asked it to retry later) and nothing has happened since. Not
	// final: Brevo keeps retrying.
	StatusDelayed   = "delayed"
	StatusDelivered = "delivered"
	StatusOpened    = "opened"
	StatusFailed    = "failed"
)

// Normalized provider events (email_letter_events.event).
const (
	EventSent         = "sent"
	EventDelivered    = "delivered"
	EventProxyOpen    = "proxy_open"
	EventOpened       = "opened"
	EventClick        = "click"
	EventDeferred     = "deferred"
	EventSoftBounce   = "soft_bounce"
	EventHardBounce   = "hard_bounce"
	EventInvalidEmail = "invalid_email"
	EventBlocked      = "blocked"
	EventError        = "error"
)

// Class says what an event means for the letter.
type Class int

const (
	// ClassUnknown is an event name arena does not know. It is acknowledged
	// and dropped (and counted), so the provider keeps the endpoint enabled.
	ClassUnknown Class = iota
	// ClassIgnored is a known event arena does not model (unsubscribed,
	// spam, list addition).
	ClassIgnored
	// ClassStep moves the letter forward: sent, delivered, proxy_open,
	// opened, click, deferred (a non-final delay).
	ClassStep
	// ClassFailure means the provider gave up on the message.
	ClassFailure
)

// eventAliases maps every spelling Brevo uses (webhook payloads in
// snake_case, its API in camelCase, its statistics in plurals) onto one name.
// "request" is the transactional event for "accepted for sending".
var eventAliases = map[string]string{
	"request":           EventSent,
	"requests":          EventSent,
	"sent":              EventSent,
	"delivered":         EventDelivered,
	"opened":            EventOpened,
	"open":              EventOpened,
	"unique_opened":     EventOpened,
	"first_opening":     EventOpened,
	"proxy_open":        EventProxyOpen,
	"unique_proxy_open": EventProxyOpen,
	"loaded_by_proxy":   EventProxyOpen,
	"soft_bounce":       EventSoftBounce,
	"soft_bounced":      EventSoftBounce,
	"hard_bounce":       EventHardBounce,
	"hard_bounced":      EventHardBounce,
	"invalid_email":     EventInvalidEmail,
	"invalid":           EventInvalidEmail,
	"blocked":           EventBlocked,
	"error":             EventError,
	"click":             EventClick,
	"clicks":            EventClick,
	"clicked":           EventClick,
	"deferred":          EventDeferred,
	"unsubscribed":      "unsubscribed",
	"unsubscribe":       "unsubscribed",
	"spam":              "spam",
	"complaint":         "spam",
	"list_addition":     "list_addition",
}

var eventClass = map[string]Class{
	EventSent:         ClassStep,
	EventDelivered:    ClassStep,
	EventProxyOpen:    ClassStep,
	EventOpened:       ClassStep,
	EventClick:        ClassStep,
	EventDeferred:     ClassStep,
	EventSoftBounce:   ClassFailure,
	EventHardBounce:   ClassFailure,
	EventInvalidEmail: ClassFailure,
	EventBlocked:      ClassFailure,
	EventError:        ClassFailure,
	"unsubscribed":    ClassIgnored,
	"spam":            ClassIgnored,
	"list_addition":   ClassIgnored,
}

// Normalize maps a provider event name onto arena's name and its class. An
// unknown name comes back folded to snake_case with ClassUnknown.
func Normalize(raw string) (string, Class) {
	e := foldEventName(raw)
	if n, ok := eventAliases[e]; ok {
		return n, eventClass[n]
	}
	return e, ClassUnknown
}

// foldEventName lowercases a name and turns camelCase, dashes and spaces
// into snake_case ("uniqueOpened" -> "unique_opened").
func foldEventName(raw string) string {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	for i, r := range raw {
		switch {
		case r >= 'A' && r <= 'Z':
			if i > 0 && raw[i-1] >= 'a' && raw[i-1] <= 'z' {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
		case r == '-' || r == ' ':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// failureSeverity breaks a tie between two failures reported at the same
// instant: the harder verdict wins.
var failureSeverity = map[string]int{
	EventSoftBounce:   1,
	EventError:        2,
	EventBlocked:      3,
	EventInvalidEmail: 4,
	EventHardBounce:   5,
}

// EventTimes is one email_letter_events row as Derive needs it.
type EventTimes struct {
	Event  string
	First  time.Time
	Last   time.Time
	Reason string
}

// Derived is the letter state computed from its events.
type Derived struct {
	Status        string
	DeliveredAt   *time.Time
	ProxyOpenedAt *time.Time
	OpenedAt      *time.Time
	ClickedAt     *time.Time
	DeferredAt    *time.Time
	FailedAt      *time.Time
	FailureEvent  string
	FailureReason string
}

// Derive folds a letter's events into its status. It depends only on the
// SET of events and their times, never on the order they arrived in:
//
//   - an open (a human or a mail client fetched the letter) means opened;
//   - a failure the provider reported AFTER the last sign of delivery
//     (delivered, proxy_open) — or with no such sign at all — means failed:
//     a failure is final unless a later delivery overrides it (a soft bounce
//     that Brevo retried successfully);
//   - delivered, proxy_open or a click means delivered (a click alone is not
//     counted as reading: mail security scanners follow links too);
//   - a deferral (the recipient's server asked Brevo to retry) means
//     delayed, until anything above happens;
//   - otherwise the letter is sent (the relay accepted it).
func Derive(events []EventTimes) Derived {
	var d Derived
	var reachLast time.Time
	var fail *EventTimes
	for i := range events {
		e := &events[i]
		switch e.Event {
		case EventOpened:
			d.OpenedAt = earliest(d.OpenedAt, e.First)
			reachLast = latest(reachLast, e.Last)
		case EventDelivered:
			d.DeliveredAt = earliest(d.DeliveredAt, e.First)
			reachLast = latest(reachLast, e.Last)
		case EventProxyOpen:
			d.ProxyOpenedAt = earliest(d.ProxyOpenedAt, e.First)
			reachLast = latest(reachLast, e.Last)
		case EventClick:
			d.ClickedAt = earliest(d.ClickedAt, e.First)
			reachLast = latest(reachLast, e.Last)
		case EventDeferred:
			d.DeferredAt = earliest(d.DeferredAt, e.First)
		default:
			if eventClass[e.Event] != ClassFailure {
				continue
			}
			if fail == nil || e.Last.After(fail.Last) ||
				(e.Last.Equal(fail.Last) && failureSeverity[e.Event] > failureSeverity[fail.Event]) {
				fail = e
			}
		}
	}
	switch {
	case d.OpenedAt != nil:
		d.Status = StatusOpened
	case fail != nil && (reachLast.IsZero() || fail.Last.After(reachLast)):
		d.Status = StatusFailed
		at := fail.Last
		d.FailedAt = &at
		d.FailureEvent = fail.Event
		d.FailureReason = fail.Reason
	case d.DeliveredAt != nil || d.ProxyOpenedAt != nil || d.ClickedAt != nil:
		d.Status = StatusDelivered
	case d.DeferredAt != nil:
		d.Status = StatusDelayed
	default:
		d.Status = StatusSent
	}
	return d
}

func earliest(cur *time.Time, t time.Time) *time.Time {
	if cur == nil || t.Before(*cur) {
		v := t
		return &v
	}
	return cur
}

func latest(cur, t time.Time) time.Time {
	if t.After(cur) {
		return t
	}
	return cur
}
