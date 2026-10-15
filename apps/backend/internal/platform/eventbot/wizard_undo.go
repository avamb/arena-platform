package eventbot

// wizard_undo.go — taking back one change of an edited event (U-5b,
// 2026-10-15). The owner edited an event with two dates, added a third and
// could not see how to take the addition back: "Back to the event" only left
// the question, "Exit" offered to drop every change at once. Now the edit
// card lists each change with its own "↩ Undo" button, and the list is worked
// out from the draft itself against the event as it was loaded
// (Draft.Loaded), never kept by hand: an undone change simply stops being a
// difference.

import (
	"strconv"
	"strings"
)

// DraftLoaded is the edited event as LoadEventDraft read it.
type DraftLoaded struct {
	Event    DraftEvent `json:"event"`
	Currency string     `json:"currency"`
	// Dates holds each existing date's categories and places, by session id.
	Dates map[string]DraftLoadedDate `json:"dates,omitempty"`
}

// DraftLoadedDate is one existing date as it was loaded.
type DraftLoadedDate struct {
	Tickets  DraftTickets `json:"tickets"`
	Capacity int          `json:"capacity"`
}

// snapshotLoaded records the draft as it stands right after loading.
func snapshotLoaded(d *Draft) *DraftLoaded {
	l := &DraftLoaded{Event: d.Event, Currency: d.Currency, Dates: map[string]DraftLoadedDate{}}
	for i, s := range d.Sessions {
		if s.SessionID == "" {
			continue
		}
		l.Dates[s.SessionID] = DraftLoadedDate{Tickets: cloneTickets(*d.ticketsOf(i)), Capacity: s.Capacity}
	}
	return l
}

// editChange is one difference between the draft and the loaded event.
type editChange struct {
	Key   string // what the undo button names: "age", "date:3", "tickets:1", …
	Field string // the part of the card it belongs to: "age", "dates", "tickets", …
	Idx   int    // the date (0-based) of a "date:" or "tickets:" change
}

// eventFields are the event's own parts in the order the card lists them.
var eventFields = []string{"name", "description", "poster", "age", "promoter", "currency"}

// changes lists what the draft would change on the site: the event's own
// parts that differ from the loaded ones, the dates it adds, and the existing
// dates whose categories are not what was loaded.
func (d *Draft) changes() []editChange {
	if d.Mode != ModeEdit || d.Loaded == nil {
		return nil
	}
	var out []editChange
	for _, f := range eventFields {
		if d.fieldChanged(f) {
			out = append(out, editChange{Key: f, Field: f})
		}
	}
	for i, s := range d.Sessions {
		if s.SessionID == "" {
			// A date counts once it is finished: place chosen and categories
			// given. One still being entered is not a change yet.
			if s.VenueID != "" && s.Tickets != nil {
				out = append(out, editChange{Key: "date:" + strconv.Itoa(i+1), Field: "dates", Idx: i})
			}
			continue
		}
		l, ok := d.Loaded.Dates[s.SessionID]
		if !ok {
			continue // added and already saved by a save that stopped half way
		}
		if s.Dirty && !ticketsEqual(*d.ticketsOf(i), l.Tickets) {
			out = append(out, editChange{Key: "tickets:" + strconv.Itoa(i+1), Field: "tickets", Idx: i})
		}
	}
	return out
}

// fieldChanged reports whether one of the event's own parts differs from
// the loaded event.
func (d *Draft) fieldChanged(field string) bool {
	l := d.Loaded
	switch field {
	case "name":
		return d.Event.Name != l.Event.Name
	case "description":
		return d.Event.Description != l.Event.Description
	case "poster":
		return d.Event.PosterMediaID != l.Event.PosterMediaID
	case "age":
		return d.Event.Age != l.Event.Age
	case "promoter":
		return d.Event.PromoterID != l.Event.PromoterID
	case "currency":
		return d.Currency != l.Currency
	}
	return false
}

// refreshChanged works Changed out again from the draft. An existing date
// whose categories came back to what was loaded is no longer sent.
func (d *Draft) refreshChanged() {
	if d.Mode != ModeEdit || d.Loaded == nil {
		return
	}
	for i := range d.Sessions {
		s := &d.Sessions[i]
		if !s.Dirty || s.SessionID == "" || s.Tickets == nil {
			continue
		}
		if l, ok := d.Loaded.Dates[s.SessionID]; ok && ticketsEqual(*s.Tickets, l.Tickets) {
			s.Dirty, s.Capacity = false, l.Capacity
		}
	}
	var fields []string
	for _, c := range d.changes() {
		if !contains(fields, c.Field) {
			fields = append(fields, c.Field)
		}
	}
	d.Changed = fields
}

// ticketsEqual compares two ticket parts exactly, tier ids included.
func ticketsEqual(a, b DraftTickets) bool {
	if a.Mode != b.Mode || len(a.Categories) != len(b.Categories) || len(a.Schedule) != len(b.Schedule) {
		return false
	}
	for i := range a.Categories {
		if a.Categories[i] != b.Categories[i] {
			return false
		}
	}
	for i := range a.Schedule {
		if a.Schedule[i] != b.Schedule[i] {
			return false
		}
	}
	return true
}

// partlySaved reports whether a save stopped half way: some dates are on
// the site already. Undo is not offered then — taking a change back in the
// draft would not take it back on the site.
func (d *Draft) partlySaved() bool {
	for _, s := range d.Saved.Sessions {
		if s.Done {
			return true
		}
	}
	return false
}

// canUndo reports whether the card offers undo buttons.
func (d *Draft) canUndo() bool { return d.Mode == ModeEdit && d.Loaded != nil && !d.partlySaved() }

// canRemoveDate reports whether date i may be taken away: it is one the
// draft adds and nothing of it is on the site. After a save that stopped half
// way only the last date may go (the dates before it keep their numbers,
// which the save's idempotency keys are built from).
func (d *Draft) canRemoveDate(i int) bool {
	if i < 0 || i >= len(d.Sessions) || d.Sessions[i].SessionID != "" {
		return false
	}
	return !d.partlySaved() || i == len(d.Sessions)-1
}

// removeDate takes date i out of the draft.
func (d *Draft) removeDate(i int) {
	d.Sessions = append(d.Sessions[:i:i], d.Sessions[i+1:]...)
	if i < len(d.Saved.Sessions) {
		d.Saved.Sessions = append(d.Saved.Sessions[:i:i], d.Saved.Sessions[i+1:]...)
	}
	d.Cur = len(d.Sessions) - 1
	if d.Cur < 0 {
		d.Cur = 0
	}
}

// undo takes one change back. It reports whether anything changed: a key of
// an older card (the change is gone meanwhile) does nothing.
func (d *Draft) undo(key string) bool {
	if !d.canUndo() {
		return false
	}
	var found *editChange
	all := d.changes()
	for i := range all {
		if all[i].Key == key {
			found = &all[i]
			break
		}
	}
	if found == nil {
		return false
	}
	l := d.Loaded
	switch found.Field {
	case "name":
		d.Event.Name = l.Event.Name
	case "description":
		d.Event.Description = l.Event.Description
	case "poster":
		d.Event.PosterMediaID, d.Event.PosterW, d.Event.PosterH = l.Event.PosterMediaID, l.Event.PosterW, l.Event.PosterH
	case "age":
		d.Event.Age = l.Event.Age
	case "promoter":
		d.Event.PromoterID, d.Event.PromoterName = l.Event.PromoterID, l.Event.PromoterName
	case "currency":
		d.Currency = l.Currency
	case "dates":
		if !d.canRemoveDate(found.Idx) {
			return false
		}
		d.removeDate(found.Idx)
	case "tickets":
		s := &d.Sessions[found.Idx]
		ld := l.Dates[s.SessionID]
		t := cloneTickets(ld.Tickets)
		s.Tickets, s.Capacity, s.Dirty = &t, ld.Capacity, false
		if found.Idx == 0 {
			d.Tickets = cloneTickets(t)
		}
	default:
		return false
	}
	d.refreshChanged()
	return true
}

// undoLabel is the short name of a change on its undo button and in the
// "taken back" note: "Age", "new date 18.12.2026 18:00", "categories of
// 15.12.2026 20:00".
func (w *Wizard) undoLabel(loc string, d *Draft, key string) string {
	t := func(k string, data map[string]any) string { return w.texts.T(loc, k, data) }
	for _, c := range d.changes() {
		if c.Key != key {
			continue
		}
		switch c.Field {
		case "dates":
			return t("bot.wz.edit_undo_date", map[string]any{"When": dateLabel(d.Sessions[c.Idx])})
		case "tickets":
			return t("bot.wz.edit_undo_tickets", map[string]any{"When": dateLabel(d.Sessions[c.Idx])})
		}
		return t("bot.wz.edit_field_"+c.Field, nil)
	}
	return ""
}

// changeLine says one change in full, with what it was and what it becomes:
// "Age: 0+ → 4+", "New date 18.12.2026 18:00 · Kids hall, 12 places: …".
func (w *Wizard) changeLine(loc, orgName string, d *Draft, c editChange) string {
	t := func(k string, data map[string]any) string { return w.texts.T(loc, k, data) }
	l := d.Loaded
	field := func(was, now string) string {
		none := t("bot.wz.edit_chg_none", nil)
		if strings.TrimSpace(was) == "" {
			was = none
		}
		if strings.TrimSpace(now) == "" {
			now = none
		}
		return t("bot.wz.edit_chg_field", map[string]any{"What": t("bot.wz.edit_field_"+c.Field, nil), "Old": was, "New": now})
	}
	switch c.Field {
	case "name":
		return t("bot.wz.edit_chg_name", map[string]any{"Old": Esc(l.Event.Name), "New": Esc(d.Event.Name)})
	case "description":
		if strings.TrimSpace(d.Event.Description) == "" {
			return t("bot.wz.edit_chg_description_removed", nil)
		}
		return t("bot.wz.edit_chg_description", nil)
	case "poster":
		return t("bot.wz.edit_chg_poster", nil)
	case "age":
		return field(l.Event.Age, d.Event.Age)
	case "promoter":
		return field(Esc(w.promoterLabel(loc, orgName, l.Event)), Esc(w.promoterLabel(loc, orgName, d.Event)))
	case "currency":
		return field(l.Currency, d.Currency)
	case "dates":
		s := d.Sessions[c.Idx]
		return t("bot.wz.edit_chg_date", map[string]any{
			"When": dateLabel(s), "Venue": Esc(s.VenueName), "Capacity": s.Capacity,
			"List": w.ticketsText(loc, d.currencyOrGuess(), d.ticketsOf(c.Idx)),
		})
	case "tickets":
		return t("bot.wz.edit_chg_tickets", map[string]any{
			"When": dateLabel(d.Sessions[c.Idx]), "List": w.ticketsText(loc, d.currencyOrGuess(), d.ticketsOf(c.Idx)),
		})
	}
	return ""
}

// changeLines lists every change as bullet lines.
func (w *Wizard) changeLines(loc, orgName string, d *Draft) []string {
	var out []string
	for _, c := range d.changes() {
		out = append(out, "• "+w.changeLine(loc, orgName, d, c))
	}
	return out
}

// promoterLabel names who an event runs for: its promoter, else the
// organization itself by its own name.
func (w *Wizard) promoterLabel(loc, orgName string, e DraftEvent) string {
	if e.PromoterID != "" && e.PromoterName != "" {
		return e.PromoterName
	}
	if orgName != "" {
		return orgName
	}
	return w.texts.T(loc, "bot.wz.promoter_org", nil)
}
