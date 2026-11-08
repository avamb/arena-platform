// Package legacytickets files tickets that were sold in the Bil24 era and
// exist only in WordPress order meta as platform orders (Vino&Co cutover,
// owner decision 2026-11-08).
//
// ops/vino-cutover/wp-legacy-tickets-export.php exports the WooCommerce orders
// that carry a 7-digit Bil24 order number; Parse reads that JSON, Prepare turns
// one order into the live tickets to file, and Run plans (dry run) or writes
// them. Every imported order has source 'legacy_import' and is invisible to
// every delivery path: no outbox event, no webhook or MACS push, no letter, no
// Telegram sales notice, no billing usage. MACS already knows the barcodes from
// the Bil24 era, and the money was taken before the platform existed.
package legacytickets

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Export is the document wp-legacy-tickets-export.php prints.
type Export struct {
	GeneratedAt   string  `json:"generated_at"`
	Source        string  `json:"source"`
	OrdersScanned int     `json:"orders_scanned"`
	Actions       []int64 `json:"actions"` // the --actions filter, empty = every action
	Orders        []Order `json:"orders"`
}

// Order is one WooCommerce order of the Bil24 era.
type Order struct {
	WCOrderID    FlexString `json:"wc_order_id"`
	Status       string     `json:"status"`
	Bil24OrderID FlexString `json:"bil24_order_id"` // bil24_external_order_id
	CreatedAt    string     `json:"created_at"`     // UTC, RFC 3339
	PaidAt       string     `json:"paid_at"`
	Currency     string     `json:"currency"`
	Total        FlexString `json:"total"`
	Language     string     `json:"language"` // wpml_language
	Billing      Billing    `json:"billing"`
	// LiveIDs is lops_bil24_live_ids: the ticket ids not refunded. nil when
	// the order has no such meta (then every ticket of the order counts).
	LiveIDs      *[]FlexInt `json:"live_ids"`
	TicketIDList FlexString `json:"ticket_id_list"` // bil24_ticket_id_list, informative
	Items        []Item     `json:"items"`
	Tickets      []Ticket   `json:"tickets"` // bil24_tickets
}

// Billing is the buyer as WooCommerce knows them.
type Billing struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
}

// Item is one order line: the product is a platform event.
type Item struct {
	ItemID             FlexString `json:"item_id"`
	ProductID          FlexString `json:"product_id"`
	Name               string     `json:"name"`
	Quantity           FlexInt    `json:"quantity"`
	Bil24ActionID      FlexInt    `json:"bil24_action_id"`       // product meta _bil24_action_id = events.external_bil24_id
	Bil24ActionEventID FlexInt    `json:"bil24_action_event_id"` // item meta "Bil24 Action Event ID" = the Bil24 session
}

// Ticket is one object of the bil24_tickets meta, as the gateway answered it.
// Money is in MAJOR units of the order currency. The keys are the Bil24 wire
// spelling (seatId, totalPrice, actionEvent, ...): UnmarshalJSON reads them by
// name, so no camelCase struct tag enters the codebase (snake_case guardrail).
type Ticket struct {
	ID            FlexInt
	SeatID        FlexInt
	OrderID       FlexInt
	SeatLocation  FlexString
	Category      json.RawMessage
	Price         FlexString
	Discount      FlexString
	Charge        FlexString
	TotalPrice    FlexString
	Barcode       FlexString
	BarcodeFormat FlexString
	// ActionEventID is actionEvent.id: the Bil24 session.
	ActionEventID FlexInt
}

// UnmarshalJSON reads one Bil24 ticket object.
func (t *Ticket) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("bil24 ticket: %w", err)
	}
	read := func(key string, into json.Unmarshaler) error {
		raw, ok := m[key]
		if !ok {
			return nil
		}
		if err := into.UnmarshalJSON(raw); err != nil {
			return fmt.Errorf("bil24 ticket %s: %w", key, err)
		}
		return nil
	}
	for key, into := range map[string]json.Unmarshaler{
		"id": &t.ID, "seatId": &t.SeatID, "orderId": &t.OrderID, "seatLocation": &t.SeatLocation,
		"price": &t.Price, "discount": &t.Discount, "charge": &t.Charge, "totalPrice": &t.TotalPrice,
		"barcode": &t.Barcode, "barcodeFormat": &t.BarcodeFormat,
	} {
		if err := read(key, into); err != nil {
			return err
		}
	}
	t.Category = m["category"]
	if raw, ok := m["actionEvent"]; ok && strings.TrimSpace(string(raw)) != "null" {
		var ae map[string]json.RawMessage
		if err := json.Unmarshal(raw, &ae); err != nil {
			return fmt.Errorf("bil24 ticket actionEvent: %w", err)
		}
		if id, ok := ae["id"]; ok {
			if err := t.ActionEventID.UnmarshalJSON(id); err != nil {
				return fmt.Errorf("bil24 ticket actionEvent.id: %w", err)
			}
		}
	}
	return nil
}

// CategoryName is the ticket's category: a plain string, or the name of an
// object.
func (t Ticket) CategoryName() string {
	raw := strings.TrimSpace(string(t.Category))
	if raw == "" || raw == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(t.Category, &s) == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(t.Category, &obj) == nil {
		return strings.TrimSpace(obj.Name)
	}
	return ""
}

// FlexString reads a JSON string, number or bool as text; null is "".
type FlexString string

// UnmarshalJSON accepts "x", 12, 12.5, true and null.
func (f *FlexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch {
	case s == "null":
		*f = ""
	case strings.HasPrefix(s, `"`):
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = FlexString(strings.TrimSpace(v))
	case strings.HasPrefix(s, "{") || strings.HasPrefix(s, "["):
		*f = FlexString(s)
	default:
		*f = FlexString(s)
	}
	return nil
}

// FlexInt reads a JSON number or numeric string as an integer; null and "" are 0.
type FlexInt int64

// UnmarshalJSON accepts 12, "12", null and "".
func (f *FlexInt) UnmarshalJSON(b []byte) error {
	var s FlexString
	if err := s.UnmarshalJSON(b); err != nil {
		return err
	}
	v := strings.TrimSpace(string(s))
	if v == "" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fmt.Errorf("not a whole number: %q", v)
	}
	*f = FlexInt(n)
	return nil
}

// Parse reads the exporter's JSON.
func Parse(r io.Reader) (Export, error) {
	var e Export
	if err := json.NewDecoder(r).Decode(&e); err != nil {
		return Export{}, fmt.Errorf("read legacy tickets json: %w", err)
	}
	return e, nil
}

// IsBil24OrderNumber reports whether n is a Bil24 order number of the Bil24
// era: 7 digits. A platform order number has 10 digits (>= 1e9).
func IsBil24OrderNumber(n int64) bool { return n >= 1_000_000 && n <= 9_999_999 }

// Skip and refusal codes. A skip is an order that is not this tool's business;
// a refusal is an order it should file but cannot, and the operator must look.
const (
	SkipNotBil24Era   = "not_bil24_era"   // no 7-digit Bil24 order number
	SkipStatus        = "status_skipped"  // not completed or processing
	SkipNoLiveTickets = "no_live_tickets" // every ticket was refunded
	SkipPastSession   = "past_session"    // --only-future and the session has started

	RefuseMissingData       = "missing_data"
	RefuseDuplicateInFile   = "duplicate_in_file"
	RefuseUnknownAction     = "unknown_action"
	RefuseUnknownSession    = "unknown_session"
	RefuseAmbiguousSession  = "ambiguous_session"
	RefuseMultipleSessions  = "multiple_sessions"
	RefuseCancelledSession  = "cancelled_session"
	RefuseNoGACategory      = "no_ga_category"
	RefuseAmbiguousCategory = "ambiguous_category"
	RefuseCurrency          = "currency_mismatch"
	RefuseMoney             = "money_mismatch"
	RefuseBarcodeTaken      = "barcode_taken"
	RefuseOrderNumberTaken  = "order_number_taken"
	RefuseTicketImported    = "ticket_already_imported"
	RefuseImportedDiffers   = "already_imported_differs"
	RefuseNoChannel         = "no_channel"
)

// Problem is why an order is skipped or refused.
type Problem struct {
	Code   string
	Detail string
}

func (p *Problem) Error() string { return p.Code + ": " + p.Detail }

// Skipped reports whether the code is a skip rather than a refusal.
func (p *Problem) Skipped() bool {
	switch p.Code {
	case SkipNotBil24Era, SkipStatus, SkipNoLiveTickets, SkipPastSession:
		return true
	}
	return false
}

// Prepared is one order ready for the database: the live tickets and the
// buyer, in plain values.
type Prepared struct {
	WCOrderID    string
	Bil24OrderID int64
	Status       string
	CreatedAt    time.Time
	PaidAt       time.Time
	BuyerName    string
	Email        string
	Phone        string
	Locale       string // "" when the language is not one the platform speaks
	Currency     string // the WooCommerce currency, upper case, "" when unknown
	Tickets      []PreparedTicket
	// LiveIDsMissing is true when the order had no lops_bil24_live_ids: every
	// ticket was taken as live.
	LiveIDsMissing bool
	Refunded       int // tickets dropped because they are not live
	Warnings       []string
}

// PreparedTicket is one live ticket.
type PreparedTicket struct {
	Bil24TicketID int64
	Barcode       string
	Category      string
	ActionEventID int64 // the Bil24 session
	ActionID      int64 // the Bil24 action (event), from the order's items; 0 unknown
	Price         string
	Discount      string
	Charge        string
	Total         string
}

var barcodeRe = regexp.MustCompile(`^[0-9]{13}$`)

// supportedLocales are the buyer languages the platform's letters speak
// (templates.SupportedLocales).
var supportedLocales = map[string]bool{"cs": true, "de": true, "en": true, "es": true, "fr": true, "he": true, "ru": true}

// NormalizeLocale maps a WPML language code onto a platform buyer locale: the
// primary subtag, lower case, "" when unsupported. WPML's "iw" is Hebrew.
func NormalizeLocale(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	if l == "iw" {
		l = "he"
	}
	if supportedLocales[l] {
		return l
	}
	return ""
}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// Prepare checks one exported order and keeps its live tickets. It returns a
// skip Problem for an order that is not this tool's business, a refusal
// Problem when the order lacks what the platform needs.
func Prepare(o Order) (Prepared, *Problem) {
	p := Prepared{
		WCOrderID: strings.TrimSpace(string(o.WCOrderID)),
		Status:    strings.ToLower(strings.TrimSpace(o.Status)),
		Currency:  strings.ToUpper(strings.TrimSpace(o.Currency)),
		Locale:    NormalizeLocale(o.Language),
		Email:     strings.TrimSpace(o.Billing.Email),
		Phone:     strings.TrimSpace(o.Billing.Phone),
		BuyerName: strings.Join(strings.Fields(o.Billing.FirstName+" "+o.Billing.LastName), " "),
	}
	p.Status = strings.TrimPrefix(p.Status, "wc-")
	n, err := strconv.ParseInt(strings.TrimSpace(string(o.Bil24OrderID)), 10, 64)
	if err != nil || !IsBil24OrderNumber(n) {
		return p, &Problem{SkipNotBil24Era, fmt.Sprintf("bil24_external_order_id %q is not a 7-digit Bil24 order number", string(o.Bil24OrderID))}
	}
	p.Bil24OrderID = n
	if p.Status != "completed" && p.Status != "processing" {
		return p, &Problem{SkipStatus, "order status " + p.Status}
	}
	if p.WCOrderID == "" {
		return p, &Problem{RefuseMissingData, "no WooCommerce order id"}
	}
	if p.Email == "" {
		return p, &Problem{RefuseMissingData, "no billing e-mail (My tickets finds a buyer by e-mail)"}
	}
	created, okCreated := parseTime(o.CreatedAt)
	paid, okPaid := parseTime(o.PaidAt)
	switch {
	case okPaid:
	case okCreated:
		paid = created
		p.Warnings = append(p.Warnings, "no paid date, the order date is used")
	default:
		return p, &Problem{RefuseMissingData, "neither a paid date nor an order date"}
	}
	if !okCreated {
		created = paid
	}
	p.CreatedAt, p.PaidAt = created, paid

	// Which Bil24 action an item's session belongs to, for the tickets.
	actionByEvent := map[int64]int64{}
	actions := map[int64]bool{}
	for _, it := range o.Items {
		if it.Bil24ActionID > 0 {
			actions[int64(it.Bil24ActionID)] = true
			if it.Bil24ActionEventID > 0 {
				actionByEvent[int64(it.Bil24ActionEventID)] = int64(it.Bil24ActionID)
			}
		}
	}
	singleAction := int64(0)
	if len(actions) == 1 {
		for a := range actions {
			singleAction = a
		}
	}

	var live map[int64]bool
	if o.LiveIDs != nil {
		live = make(map[int64]bool, len(*o.LiveIDs))
		for _, id := range *o.LiveIDs {
			live[int64(id)] = true
		}
	} else {
		p.LiveIDsMissing = true
		p.Warnings = append(p.Warnings, "no lops_bil24_live_ids: every ticket of the order is taken as live")
	}
	seen := map[int64]bool{}
	for _, t := range o.Tickets {
		id := int64(t.ID)
		if id <= 0 {
			return p, &Problem{RefuseMissingData, "a ticket without a Bil24 ticket id"}
		}
		if seen[id] {
			return p, &Problem{RefuseDuplicateInFile, fmt.Sprintf("ticket %d is listed twice in the order", id)}
		}
		seen[id] = true
		if live != nil && !live[id] {
			p.Refunded++
			continue
		}
		code := strings.TrimSpace(string(t.Barcode))
		if !barcodeRe.MatchString(code) {
			return p, &Problem{RefuseMissingData, fmt.Sprintf("ticket %d: barcode %q is not 13 digits", id, code)}
		}
		ae := int64(t.ActionEventID)
		action := actionByEvent[ae]
		if action == 0 {
			action = singleAction
		}
		p.Tickets = append(p.Tickets, PreparedTicket{
			Bil24TicketID: id,
			Barcode:       code,
			Category:      t.CategoryName(),
			ActionEventID: ae,
			ActionID:      action,
			Price:         string(t.Price),
			Discount:      string(t.Discount),
			Charge:        string(t.Charge),
			Total:         string(t.TotalPrice),
		})
	}
	if len(p.Tickets) == 0 {
		return p, &Problem{SkipNoLiveTickets, fmt.Sprintf("%d ticket(s), none live", len(o.Tickets))}
	}
	sort.Slice(p.Tickets, func(i, j int) bool { return p.Tickets[i].Bil24TicketID < p.Tickets[j].Bil24TicketID })
	return p, nil
}

// Money is one ticket's amounts in MINOR units.
type Money struct {
	Price, Discount, Charge, Total int64
}

// ParseMajor converts a major-unit decimal ("250", "18.9", "") into minor
// units of the given scale (100 for ILS/EUR), exactly: no float on the way.
func ParseMajor(s string, scale int64) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	digits := len(strconv.FormatInt(scale, 10)) - 1
	if scale <= 1 {
		digits = 0
	}
	if len(frac) > digits {
		// Only zeros may be cut: "250.000" is fine, "18.905" is not.
		if strings.Trim(frac[digits:], "0") != "" {
			return 0, fmt.Errorf("amount %q has more decimals than the currency", s)
		}
		frac = frac[:digits]
	}
	for len(frac) < digits {
		frac += "0"
	}
	if whole == "" {
		whole = "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("amount %q is not a number", s)
	}
	var f int64
	if digits > 0 {
		if f, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return 0, fmt.Errorf("amount %q is not a number", s)
		}
	}
	v := w*maxScale(scale) + f
	if neg {
		v = -v
	}
	return v, nil
}

func maxScale(scale int64) int64 {
	if scale <= 1 {
		return 1
	}
	return scale
}

// TicketMoney converts one ticket's amounts and checks that
// total = price - discount + charge. A missing totalPrice is computed.
func TicketMoney(t PreparedTicket, scale int64) (Money, error) {
	var m Money
	var err error
	if m.Price, err = ParseMajor(t.Price, scale); err != nil {
		return m, err
	}
	if m.Discount, err = ParseMajor(t.Discount, scale); err != nil {
		return m, err
	}
	if m.Charge, err = ParseMajor(t.Charge, scale); err != nil {
		return m, err
	}
	want := m.Price - m.Discount + m.Charge
	if strings.TrimSpace(t.Total) == "" {
		m.Total = want
	} else if m.Total, err = ParseMajor(t.Total, scale); err != nil {
		return m, err
	}
	if m.Price < 0 || m.Discount < 0 || m.Charge < 0 || m.Total < 0 {
		return m, fmt.Errorf("ticket %d has a negative amount", t.Bil24TicketID)
	}
	if m.Total != want {
		return m, fmt.Errorf("ticket %d: totalPrice %s is not price %s - discount %s + charge %s",
			t.Bil24TicketID, t.Total, t.Price, t.Discount, t.Charge)
	}
	return m, nil
}
