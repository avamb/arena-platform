package eventbot

// promo.go — the pure part of the promo-code screens (EC-11, spec 35 §6.2):
// what a typed code, percent, amount or limit means, the derived state of a
// code, and the state machine of the "new code" dialog. Nothing here talks to
// Telegram or to arena-api; promo_dialog.go carries the answers in and out.
//
// The creation dialog, in order:
//
//	code -> type -> (percent | amount [-> currency]) -> sessions
//	     -> (event -> pick)? -> total limit -> per-buyer limit -> expiry
//	     -> status -> summary
//
// Every step has a "Back" that returns to the one before it (Hist). The two
// limits and the expiry are skippable; the rest is required. The money rule is
// said where the amount is asked: a fixed discount comes off the ORDER once,
// not off every ticket, and an order carries one code.

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The dialog's steps. Creation steps begin with "c_".
const (
	pmStepList     = "list"
	pmStepCard     = "card"
	pmStepUsage    = "usage"
	pmStepDel      = "del"
	pmStepEdit     = "edit"   // the card's "Sessions" screen
	pmStepEditEv   = "e_ev"   // choosing the event whose sessions are edited
	pmStepEditPick = "e_pick" // the checkboxes of that event's sessions
	pmStepCode     = "c_code"
	pmStepType     = "c_type"
	pmStepPercent  = "c_percent"
	pmStepAmount   = "c_amount"
	pmStepCurrency = "c_cur"
	pmStepScope    = "c_scope"
	pmStepEvent    = "c_event"
	pmStepPick     = "c_pick"
	pmStepTotal    = "c_total"
	pmStepPer      = "c_per"
	pmStepExpiry   = "c_exp"
	pmStepStatus   = "c_status"
	pmStepConfirm  = "c_confirm"
)

const (
	// maxPromoCodeRunes is the longest code the bot accepts (spec 35 §6.2).
	maxPromoCodeRunes = 64
	// maxPromoLimit bounds a usage limit; a bigger one is a typo.
	maxPromoLimit = 1_000_000
	// maxPromoAmountMinor bounds a fixed discount (10 million major units).
	maxPromoAmountMinor = int64(10_000_000) * 100
)

// ─── parsing ──────────────────────────────────────────────────────────────────

// normalizePromoCode upper-cases what the person typed and checks it. The
// second result is the message key of the reason when the text is refused.
// Letters of any alphabet, digits, "-", "_" and "." are allowed: buyers type
// the code by hand into a site, so no spaces and nothing exotic.
func normalizePromoCode(raw string) (string, string) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return "", "bot.promo.err_code_empty"
	}
	if utf8.RuneCountInString(s) > maxPromoCodeRunes {
		return "", "bot.promo.err_code_long"
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
			return "", "bot.promo.err_code_chars"
		}
	}
	return s, ""
}

// parsePromoPercent reads "15", "15%" or "15 %": a whole number from 1 to 100.
func parsePromoPercent(raw string) (int64, bool) {
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "%"))
	n, ok := digitsValue(s)
	if !ok || n < 1 || n > 100 {
		return 0, false
	}
	return n, true
}

// parsePromoAmount reads a money amount in major units ("5", "5.5", "5,50")
// and returns minor units. At most two decimals; one separator only, so a
// thousands separator ("1,000") is refused instead of being misread.
func parsePromoAmount(raw string) (int64, bool) {
	s := strings.NewReplacer(" ", "", " ", "", " ", "").Replace(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, ",", ".")
	if strings.Count(s, ".") > 1 {
		return 0, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" && frac == "" {
		return 0, false
	}
	if len(frac) > 2 {
		return 0, false
	}
	w := int64(0)
	if whole != "" {
		v, ok := digitsValue(whole)
		if !ok || v > maxPromoAmountMinor/100 {
			return 0, false
		}
		w = v
	}
	f := int64(0)
	if frac != "" {
		v, ok := digitsValue(frac)
		if !ok {
			return 0, false
		}
		if len(frac) == 1 {
			v *= 10
		}
		f = v
	}
	minor := w*100 + f
	if minor < 1 || minor > maxPromoAmountMinor {
		return 0, false
	}
	return minor, true
}

// parsePromoLimit reads a usage limit: a whole number from 1 to maxPromoLimit.
func parsePromoLimit(raw string) (int32, bool) {
	s := strings.NewReplacer(" ", "", " ", "").Replace(strings.TrimSpace(raw))
	n, ok := digitsValue(s)
	if !ok || n < 1 || n > maxPromoLimit {
		return 0, false
	}
	return int32(n), true // #nosec G115 -- bounded above by maxPromoLimit
}

// parsePromoCurrency reads a three-letter currency code in any case.
func parsePromoCurrency(raw string) (string, bool) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if len(s) != 3 || strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return "", false
	}
	return s, true
}

// digitsValue is the value of a string of ASCII digits (at most 12 of them).
func digitsValue(s string) (int64, bool) {
	if s == "" || len(s) > 12 {
		return 0, false
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int64(r-'0')
	}
	return n, true
}

// ─── the state of a code ──────────────────────────────────────────────────────

// The derived states of a code (the API keeps only active and paused).
const (
	promoStActive    = "active"
	promoStPaused    = "paused"
	promoStExpired   = "expired"
	promoStExhausted = "exhausted"
	promoStScheduled = "scheduled"
)

// promoState is what a code is right now, as the buyer would meet it: paused
// by the organizer, past its date, not yet valid, used up, or working.
func promoState(it openapi.PromoCodeItem, now time.Time) string {
	switch {
	case string(it.Status) == promoStatusPaused:
		return promoStPaused
	case it.ValidUntil != nil && now.After(*it.ValidUntil):
		return promoStExpired
	case it.ValidFrom != nil && now.Before(*it.ValidFrom):
		return promoStScheduled
	case it.MaxUses != nil && it.Uses >= *it.MaxUses:
		return promoStExhausted
	}
	return promoStActive
}

// PromoChip is the one-glyph state of a code: ● working, ⏸ paused, ⌛ past its
// date, ✕ used up, ○ not yet valid.
func PromoChip(state string) string {
	switch state {
	case promoStActive:
		return "●"
	case promoStPaused:
		return "⏸"
	case promoStExpired:
		return "⌛"
	case promoStExhausted:
		return "✕"
	case promoStScheduled:
		return "○"
	}
	return "·"
}

// promoIsClub reports a club code: no session list, so it works on every
// session of the organization, future ones included.
func promoIsClub(it openapi.PromoCodeItem) bool { return len(it.AppliesToSessionIds) == 0 }

// promoEventFilter keeps the codes that work on the event: club codes and the
// ones naming at least one of its sessions. Order is kept.
func promoEventFilter(items []openapi.PromoCodeItem, sessionIDs map[uuid.UUID]bool) []openapi.PromoCodeItem {
	out := make([]openapi.PromoCodeItem, 0, len(items))
	for _, it := range items {
		if promoIsClub(it) {
			out = append(out, it)
			continue
		}
		for _, sid := range it.AppliesToSessionIds {
			if sessionIDs[sid] {
				out = append(out, it)
				break
			}
		}
	}
	return out
}

// promoMergeSessions is the new session list when the sessions of ONE event
// are re-chosen: the sessions the code already had on other events stay, the
// event's own are replaced by the checked ones. An empty result is refused by
// the caller — an empty list would silently make the code a club code.
func promoMergeSessions(existing []uuid.UUID, eventSessions map[uuid.UUID]bool, checked []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(existing)+len(checked))
	seen := map[uuid.UUID]bool{}
	for _, id := range existing {
		if !eventSessions[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range checked {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ─── the picker (an event, then its sessions) ─────────────────────────────────

// promoOption is one choice in a list of buttons: an event or a session. Label
// is raw text, escaped where it is drawn.
type promoOption struct {
	ID       uuid.UUID `json:"id"`
	Label    string    `json:"label"`
	Currency string    `json:"cur,omitempty"`
	Tz       string    `json:"tz,omitempty"`
}

// promoPick is a page of checkboxes over a list of sessions.
type promoPick struct {
	Options []promoOption `json:"options"`
	Sel     []bool        `json:"sel"`
	Page    int           `json:"page"`
}

func newPromoPick(opts []promoOption, preset map[uuid.UUID]bool) *promoPick {
	p := &promoPick{Options: opts, Sel: make([]bool, len(opts)), Page: 1}
	for i, o := range opts {
		p.Sel[i] = preset[o.ID]
	}
	return p
}

func (p *promoPick) checked() []uuid.UUID {
	var out []uuid.UUID
	if p == nil {
		return out
	}
	for i, on := range p.Sel {
		if on && i < len(p.Options) {
			out = append(out, p.Options[i].ID)
		}
	}
	return out
}

func (p *promoPick) pages() int { return PagesFor(int64(len(p.Options)), listPageSize) }

// promoPicker is the two-screen chooser shared by the creation dialog and the
// "Sessions" edit of an existing code: the event's list (when no event is
// known yet), then that event's sessions as checkboxes.
type promoPicker struct {
	EventID   *uuid.UUID    `json:"event_id,omitempty"`
	EventName string        `json:"event_name,omitempty"`
	Events    []promoOption `json:"events,omitempty"`
	EvPage    int           `json:"ev_page,omitempty"`
	Pick      *promoPick    `json:"pick,omitempty"`
}

// Outcomes of a press on the picker.
const (
	pickStay    = ""      // nothing moved on: draw the same screen again
	pickEvent   = "event" // an event was chosen: the caller loads its sessions
	pickDone    = "done"  // "Done" with at least one session checked
	pickChevent = "chev"  // back to the list of events
)

// press applies one press of the picker. fixedCur is the code's currency when
// it is a fixed discount ("" for a percent code): a session sold in another
// currency can never take the code, so it cannot be checked. allowEmpty lets
// "Done" pass with nothing checked (the edit of a code that has sessions on
// other events: removing this event's own is a valid result). The second
// result is the message key of a refusal.
func (p *promoPicker) press(data, fixedCur string, allowEmpty bool) (outcome, errKey string) {
	kind, arg, _ := strings.Cut(data, ":")
	switch kind {
	case "ev":
		i, ok := ParseIndex(arg, len(p.Events))
		if !ok {
			return pickStay, ""
		}
		id := p.Events[i].ID
		p.EventID, p.EventName, p.Pick = &id, p.Events[i].Label, nil
		return pickEvent, ""
	case "evp":
		p.EvPage = ParsePage(arg)
		return pickStay, ""
	case "chev":
		p.EventID, p.EventName, p.Pick = nil, "", nil
		return pickChevent, ""
	}
	if p.Pick == nil {
		return pickStay, ""
	}
	switch kind {
	case "t":
		i, ok := ParseIndex(arg, len(p.Pick.Options))
		if !ok {
			return pickStay, ""
		}
		o := p.Pick.Options[i]
		if !p.Pick.Sel[i] && fixedCur != "" && o.Currency != "" && !strings.EqualFold(o.Currency, fixedCur) {
			return pickStay, "bot.promo.err_session_currency"
		}
		p.Pick.Sel[i] = !p.Pick.Sel[i]
	case "pg":
		p.Pick.Page = ParsePage(arg)
	case "ok":
		if !allowEmpty && len(p.Pick.checked()) == 0 {
			return pickStay, "bot.promo.err_pick_none"
		}
		return pickDone, ""
	}
	return pickStay, ""
}

// ─── the creation dialog ──────────────────────────────────────────────────────

// promoDraft is the code being created, plus where the dialog stands.
type promoDraft struct {
	Step     string   `json:"step"`
	Hist     []string `json:"hist,omitempty"`
	Code     string   `json:"code,omitempty"`
	Type     string   `json:"type,omitempty"` // percent | fixed_amount
	Value    int64    `json:"value,omitempty"`
	Currency string   `json:"currency,omitempty"`
	CurOpts  []string `json:"cur_opts,omitempty"`
	All      bool     `json:"all,omitempty"` // every session, including future ones
	promoPicker
	MaxUses  *int32 `json:"max_uses,omitempty"`
	PerBuyer *int32 `json:"per_buyer,omitempty"`
	Until    string `json:"until,omitempty"` // YYYY-MM-DD, the last day it works
	Status   string `json:"status,omitempty"`
	Cal      *Draft `json:"cal,omitempty"` // the wizard calendar's throwaway draft
	// Names are the upper-cased names of the organization's codes, read when
	// the dialog starts, for the duplicate check.
	Names []string `json:"names,omitempty"`
}

func newPromoDraft() *promoDraft { return &promoDraft{Step: pmStepCode} }

// goTo moves to a step and remembers where it came from.
func (d *promoDraft) goTo(step string) {
	if d.Step != step {
		d.Hist = append(d.Hist, d.Step)
	}
	d.Step = step
}

// back returns to the step before; false at the first step.
func (d *promoDraft) back() bool {
	if len(d.Hist) == 0 {
		return false
	}
	d.Step = d.Hist[len(d.Hist)-1]
	d.Hist = d.Hist[:len(d.Hist)-1]
	if d.Step == pmStepExpiry {
		d.Cal = nil
	}
	return true
}

// item is the draft as the API would print it, for the texts that describe a
// code (discount, limits).
func (d *promoDraft) item() openapi.PromoCodeItem {
	it := openapi.PromoCodeItem{
		Code: d.Code, DiscountType: openapi.PromoCodeItemDiscountType(d.Type), DiscountValue: d.Value,
		MaxUses: d.MaxUses, MaxUsesPerCustomer: d.PerBuyer,
	}
	if d.fixed() {
		cur := d.Currency
		it.Currency = &cur
	}
	return it
}

// existing is the duplicate-check set of promoApply.
func (d *promoDraft) existing() promoEnv {
	m := make(map[string]bool, len(d.Names))
	for _, n := range d.Names {
		m[n] = true
	}
	return promoEnv{Existing: m}
}

// fixed reports a fixed-amount discount.
func (d *promoDraft) fixed() bool { return d.Type == promoTypeFixed }

// promoEnv is what the pure steps need from the outside.
type promoEnv struct {
	// Existing holds the upper-cased names of the organization's codes.
	Existing map[string]bool
}

// promoApply advances the draft by one answer: a typed text, or the data of a
// button (without the "pm:c:" prefix). The result is the message key of the
// reason the answer was refused ("" = accepted; a button that means nothing at
// this step is ignored without a reason). The caller redraws the draft's step.
func promoApply(d *promoDraft, text, data string, env promoEnv) string {
	if data == "back" {
		d.back()
		return ""
	}
	text = strings.TrimSpace(text)
	switch d.Step {
	case pmStepCode:
		if text == "" {
			return ""
		}
		code, key := normalizePromoCode(text)
		if key != "" {
			return key
		}
		if env.Existing[code] {
			return "bot.promo.err_code_dup"
		}
		d.Code = code
		d.goTo(pmStepType)

	case pmStepType:
		switch data {
		case "pct":
			d.Type = promoTypePercent
			d.goTo(pmStepPercent)
		case "fix":
			d.Type = promoTypeFixed
			d.goTo(pmStepAmount)
		}

	case pmStepPercent:
		if text == "" {
			return ""
		}
		n, ok := parsePromoPercent(text)
		if !ok {
			return "bot.promo.err_percent"
		}
		d.Value = n
		d.goTo(pmStepScope)

	case pmStepAmount:
		if text == "" {
			return ""
		}
		minor, ok := parsePromoAmount(text)
		if !ok {
			return "bot.promo.err_amount"
		}
		d.Value = minor
		if len(d.CurOpts) == 1 {
			d.Currency = d.CurOpts[0]
			d.goTo(pmStepScope)
		} else {
			d.goTo(pmStepCurrency)
		}

	case pmStepCurrency:
		raw := text
		if strings.HasPrefix(data, "cur:") {
			raw = strings.TrimPrefix(data, "cur:")
		}
		if raw == "" {
			return ""
		}
		cur, ok := parsePromoCurrency(raw)
		if !ok {
			return "bot.promo.err_currency"
		}
		d.Currency = cur
		d.goTo(pmStepScope)

	case pmStepScope:
		switch data {
		case "all":
			d.All = true
			d.goTo(pmStepTotal)
		case "pick":
			d.All = false
			if d.EventID != nil {
				d.goTo(pmStepPick)
			} else {
				d.goTo(pmStepEvent)
			}
		}

	case pmStepEvent, pmStepPick:
		cur := ""
		if d.fixed() {
			cur = d.Currency
		}
		out, key := d.promoPicker.press(data, cur, false)
		switch out {
		case pickEvent:
			d.goTo(pmStepPick)
		case pickDone:
			d.goTo(pmStepTotal)
		case pickChevent:
			d.goTo(pmStepEvent)
		}
		return key

	case pmStepTotal:
		if data == "skip" {
			d.MaxUses = nil
			d.goTo(pmStepPer)
			return ""
		}
		if text == "" {
			return ""
		}
		n, ok := parsePromoLimit(text)
		if !ok {
			return "bot.promo.err_limit"
		}
		d.MaxUses = &n
		d.goTo(pmStepPer)

	case pmStepPer:
		if data == "skip" {
			d.PerBuyer = nil
			d.goTo(pmStepExpiry)
			return ""
		}
		if text == "" {
			return ""
		}
		n, ok := parsePromoLimit(text)
		if !ok {
			return "bot.promo.err_limit"
		}
		if d.MaxUses != nil && n > *d.MaxUses {
			return "bot.promo.err_per_gt_total"
		}
		d.PerBuyer = &n
		d.goTo(pmStepExpiry)

	case pmStepExpiry:
		switch {
		case data == "skip":
			d.Until = ""
			d.goTo(pmStepStatus)
		case strings.HasPrefix(data, "date:"):
			iso := strings.TrimPrefix(data, "date:")
			if _, err := time.Parse(isoLayout, iso); err != nil {
				return "bot.wz.err_date"
			}
			d.Until = iso
			d.goTo(pmStepStatus)
		}

	case pmStepStatus:
		switch data {
		case "st:active":
			d.Status = promoStatusActive
			d.goTo(pmStepConfirm)
		case "st:paused":
			d.Status = promoStatusPaused
			d.goTo(pmStepConfirm)
		}
	}
	return ""
}

// untilInstant is the moment a chosen last day ends: 23:59:59 UTC of that day.
// The day is a calendar date in the organizer's mind; UTC keeps it the same
// date the list prints back, whichever zone the venue is in.
func untilInstant(iso string) (time.Time, bool) {
	t, err := time.Parse(isoLayout, iso)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC), true
}

// create returns the request the finished draft stands for.
func (d *promoDraft) create() (PromoCreate, bool) {
	if d.Code == "" || d.Type == "" || d.Value <= 0 || d.Status == "" {
		return PromoCreate{}, false
	}
	in := PromoCreate{
		Code: d.Code, DiscountType: d.Type, DiscountValue: d.Value,
		MaxUses: d.MaxUses, MaxUsesPerCustomer: d.PerBuyer, Status: d.Status,
	}
	if d.fixed() {
		if d.Currency == "" {
			return PromoCreate{}, false
		}
		in.Currency = d.Currency
	}
	if !d.All {
		in.SessionIDs = d.Pick.checked()
		if len(in.SessionIDs) == 0 {
			return PromoCreate{}, false // an empty list would be a club code
		}
	}
	if d.Until != "" {
		t, ok := untilInstant(d.Until)
		if !ok {
			return PromoCreate{}, false
		}
		in.ValidUntil = &t
	}
	return in, true
}
