// Package wpletters files the ticket-letter history of the WordPress era in
// the platform's email_letters table (Vino&Co cutover).
//
// WordPress sent its ticket letters through its own Brevo account and
// lampyris-ops recorded, on each WooCommerce order, when the letter went out
// and what Brevo reported afterwards. ops/vino-cutover/wp-letters-export.php
// exports that; Parse reads the JSON, Map turns one record into the row the
// event center shows, Import writes them.
//
// The rows carry provider "wp_brevo": that marks the old site as the sender,
// keeps them apart from letters arena sent itself, and the unique index
// (provider, provider_message_id) is what makes a second run a no-op.
package wpletters

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Provider is email_letters.provider of every imported row.
const Provider = "wp_brevo"

// KindTicket is the email_letters.kind of a ticket letter.
const KindTicket = "ticket"

// TemplateVersion marks the rows in email_letters.template_version.
const TemplateVersion = "wordpress-bil24-ticket-mailer"

// email_letters.status values (migration 0147).
const (
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusOpened    = "opened"
	StatusFailed    = "failed"
)

// Export is the document wp-letters-export.php prints.
type Export struct {
	GeneratedAt   string   `json:"generated_at"`
	Source        string   `json:"source"`
	OrdersScanned int      `json:"orders_scanned"`
	Letters       []Record `json:"letters"`
}

// Record is one WooCommerce order that had a letter or a status.
type Record struct {
	WCOrderID string `json:"wc_order_id"`
	// PlatformOrderSystemID is bil24_external_order_id, orders.system_id.
	PlatformOrderSystemID FlexString `json:"platform_order_system_id"`
	Recipient             string     `json:"recipient"`
	SentAt                string     `json:"sent_at"`
	MessageID             string     `json:"message_id"`
	Status                string     `json:"status"`
	StatusDetail          string     `json:"status_detail"`
	StatusTS              int64      `json:"status_ts"`
	Times                 Times      `json:"times"`
	Failed                bool       `json:"failed"`
	OrderDate             string     `json:"order_date"`
}

// Times are the first-seen unix times lampyris-ops keeps per step.
type Times struct {
	Sent      int64 `json:"sent"`
	Delivered int64 `json:"delivered"`
	Opened    int64 `json:"opened"`
}

// FlexString reads a JSON string or number as text.
type FlexString string

// UnmarshalJSON accepts "123", 123 and null.
func (f *FlexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = FlexString(strings.TrimSpace(v))
		return nil
	}
	*f = FlexString(s)
	return nil
}

// Parse reads the exporter's JSON.
func Parse(r io.Reader) (Export, error) {
	var e Export
	dec := json.NewDecoder(r)
	if err := dec.Decode(&e); err != nil {
		return Export{}, fmt.Errorf("read letters json: %w", err)
	}
	return e, nil
}

// SystemID is the platform order number, 0 when the record has none or it is
// not a positive whole number.
func (r Record) SystemID() int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(string(r.PlatformOrderSystemID)), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// Letter is one email_letters row, minus the order it belongs to.
type Letter struct {
	Recipient     string
	MessageID     string // "" -> provider_message_id NULL
	Status        string
	SentAt        time.Time
	DeliveredAt   *time.Time
	ProxyOpenedAt *time.Time
	OpenedAt      *time.Time
	FailedAt      *time.Time
	FailureEvent  *string
	FailureReason *string
}

var failureStates = map[string]bool{
	"soft_bounce": true, "hard_bounce": true, "invalid_email": true, "blocked": true, "error": true,
}

// Rank orders the statuses so a later run can tell progress from a stale copy.
func Rank(status string) int {
	switch status {
	case StatusSent:
		return 1
	case StatusDelivered:
		return 2
	case StatusOpened:
		return 3
	case StatusFailed:
		return 4
	}
	return 0
}

func unix(ts int64) *time.Time {
	if ts <= 0 {
		return nil
	}
	t := time.Unix(ts, 0).UTC()
	return &t
}

func parseISO(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

// cleanReason keeps a failure reason short and on one line.
func cleanReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300])
	}
	return s
}

// Map turns a record into the row to store. ok is false when the record
// cannot be a letter row (no recipient, or no time at all to date it by).
//
// Brevo states map as follows: sent -> sent, delivered -> delivered, opened
// and unique_opened -> opened, proxy_open -> delivered with proxy_opened_at
// (a mail proxy fetching the images is proof of delivery, not of reading,
// exactly as emailstatus.Derive treats it), the bounce family and a set
// failure flag -> failed. A record with no state but a send stamp is sent.
func Map(r Record) (Letter, bool) {
	rcp := strings.TrimSpace(r.Recipient)
	if rcp == "" {
		return Letter{}, false
	}
	sent := parseISO(r.SentAt)
	if sent == nil {
		sent = unix(r.Times.Sent)
	}
	if sent == nil {
		sent = parseISO(r.OrderDate)
	}
	if sent == nil {
		return Letter{}, false
	}
	l := Letter{
		Recipient:   rcp,
		MessageID:   strings.TrimSpace(r.MessageID),
		Status:      StatusSent,
		SentAt:      *sent,
		DeliveredAt: unix(r.Times.Delivered),
	}
	state := strings.ToLower(strings.TrimSpace(r.Status))
	switch {
	case failureStates[state] || r.Failed:
		l.Status = StatusFailed
		ev := state
		if !failureStates[ev] {
			ev = "error"
		}
		l.FailureEvent = &ev
		at := unix(r.StatusTS)
		if at == nil {
			at = sent
		}
		l.FailedAt = at
		if reason := cleanReason(r.StatusDetail); reason != "" {
			l.FailureReason = &reason
		}
	case state == "opened" || state == "unique_opened":
		l.Status = StatusOpened
		l.OpenedAt = unix(r.Times.Opened)
		if l.OpenedAt == nil {
			l.OpenedAt = unix(r.StatusTS)
		}
	case state == "proxy_open":
		l.Status = StatusDelivered
		l.ProxyOpenedAt = unix(r.Times.Opened)
		if l.ProxyOpenedAt == nil {
			l.ProxyOpenedAt = unix(r.StatusTS)
		}
	case state == "delivered":
		l.Status = StatusDelivered
	}
	// A letter that was opened or proxy-opened was delivered.
	if (l.OpenedAt != nil || l.ProxyOpenedAt != nil) && l.DeliveredAt == nil {
		d := l.OpenedAt
		if d == nil {
			d = l.ProxyOpenedAt
		}
		l.DeliveredAt = d
	}
	if l.Status == StatusDelivered && l.DeliveredAt == nil {
		l.DeliveredAt = unix(r.StatusTS)
	}
	return l, true
}
