package eventbot

// wizard_load.go — "Edit" and "Repeat as new": an existing event is read
// back from arena (never from what was typed last time — a price changed in
// the admin, a chain that already handed over, all of it is visible) and
// turned into a draft (logic doc §5). The same draft with the ids stripped
// and the dates cleared is the copy.

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// EditIO is what loading an event needs from arena-api.
type EditIO interface {
	GetEvent(ctx context.Context, jwt string, eventID uuid.UUID) (openapi.EventItem, error)
	ListSessions(ctx context.Context, jwt string, orgID, eventID uuid.UUID) ([]openapi.SessionItem, error)
	ListTiers(ctx context.Context, jwt string, orgID, eventID, sessionID uuid.UUID) ([]openapi.TicketTierItem, error)
	TierPriceSchedule(ctx context.Context, jwt string, orgID, eventID, sessionID, tierID uuid.UUID) ([]openapi.TierPriceWindow, error)
}

// ErrNotEditable is returned for an event the wizard cannot represent: a
// session with a seating plan (the bot only knows general admission) or an
// event with no sessions at all.
var ErrNotEditable = errors.New("eventbot: event cannot be edited from the bot")

// LoadEventDraft reads the event and builds a ModeEdit draft positioned at
// the edit card. Every date carries its own categories, read from that
// date's tiers with their own ids (two dates of one event may sell different
// categories); Draft.Tickets mirrors the first date. The returned notes are
// things the person should know before saving (e.g. that the dates carry
// different categories, which are then changed date by date).
func (w *Wizard) LoadEventDraft(ctx context.Context, io EditIO, ws WizSession, eventID uuid.UUID) (*Draft, []string, error) {
	ev, err := io.GetEvent(ctx, ws.JWT, eventID)
	if err != nil {
		return nil, nil, err
	}
	if ev.OrgId != ws.OrgID {
		return nil, nil, ErrNotEditable
	}
	sessions, err := io.ListSessions(ctx, ws.JWT, ws.OrgID, eventID)
	if err != nil {
		return nil, nil, err
	}
	live := sessions[:0:0]
	for _, s := range sessions {
		if string(s.Status) == "cancelled" {
			continue
		}
		if s.SeatingPlanVersionId != nil || string(s.AdmissionMode) == "seated" {
			return nil, nil, ErrNotEditable
		}
		live = append(live, s)
	}
	if len(live) == 0 {
		return nil, nil, ErrNotEditable
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].StartAt.Before(live[j].StartAt) })

	venues, err := w.refs.Venues(ctx, ws.JWT, ws.OrgID)
	if err != nil {
		return nil, nil, err
	}
	byVenue := map[string]VenueRef{}
	for _, v := range venues {
		byVenue[v.ID] = v
	}
	countries, err := w.refs.Countries(ctx, ws.JWT, ws.Locale)
	if err != nil {
		return nil, nil, err
	}
	byISO := map[string]RefItem{}
	for _, c := range countries {
		byISO[strings.ToUpper(c.ISO2)] = c
	}

	d := &Draft{Version: draftSchemaVersion, Mode: ModeEdit, Step: stEditMenu}
	d.Event = DraftEvent{
		EventID:      ev.Id.String(),
		UpdatedAt:    ev.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Name:         ev.Name,
		Age:          deref(ev.AgeRating),
		PromoterName: deref(ev.PromoterName),
		Description:  deref(ev.Description),
	}
	if ev.PromoterId != nil {
		d.Event.PromoterID = ev.PromoterId.String()
	}
	if ev.PosterMediaId != nil {
		d.Event.PosterMediaID = ev.PosterMediaId.String()
	}
	d.Publish = string(ev.Status) == "published"

	var notes []string
	for i, s := range live {
		v := byVenue[s.VenueId.String()]
		loc := time.UTC
		if v.Timezone != "" {
			if l, err := time.LoadLocation(v.Timezone); err == nil {
				loc = l
			}
		}
		start := s.StartAt.In(loc)
		ds := DraftSession{
			SessionID: s.Id.String(),
			Date:      start.Format("2006-01-02"), // allow:timeformat: calendar date of the draft, no clock
			Time:      start.Format("15:04"),      // allow:timeformat: wall-clock time of the draft
			VenueID:   v.ID,
			VenueName: v.Name,
			CityID:    v.CityID,
			Timezone:  v.Timezone,
			Capacity:  int(s.CapacityTotal),
		}
		if s.CapacityOverride != nil {
			ds.Capacity = int(*s.CapacityOverride)
		}
		if !s.SalesEndAt.IsZero() {
			ds.SalesEndMin = int(s.SalesEndAt.Sub(s.StartAt).Round(time.Minute) / time.Minute)
		}
		if s.DoorsOpenAt != nil {
			ds.DoorsMin = int(s.StartAt.Sub(*s.DoorsOpenAt).Round(time.Minute) / time.Minute)
		}
		if c, ok := byISO[strings.ToUpper(v.CountryISO2)]; ok {
			ds.CountryID, ds.CountryName, ds.CountryISO2, ds.Currency = c.ID, c.Name, c.ISO2, c.Currency
		}
		if i == 0 {
			d.Currency = s.Currency
		}
		tiers, err := io.ListTiers(ctx, ws.JWT, ws.OrgID, eventID, s.Id)
		if err != nil {
			return nil, nil, err
		}
		var own DraftTickets
		switch {
		case len(tiers) == 0 && i > 0:
			// A later date with no category yet: it sells nothing until one
			// is given to it, in the first date's mode.
			own = DraftTickets{Mode: d.Tickets.Mode}
		default:
			own, err = w.loadTickets(ctx, io, ws, eventID, s, tiers, loc)
			if err != nil {
				return nil, nil, err
			}
		}
		if i == 0 {
			d.Tickets = cloneTickets(own)
		}
		ds.Tickets = &own
		d.Sessions = append(d.Sessions, ds)
	}
	d.Cur = len(d.Sessions) - 1
	if d.ticketsDiffer() {
		notes = append(notes, w.texts.T(ws.Locale, "bot.wz.edit_tickets_differ", nil))
	}
	return d, notes, nil
}

// loadTickets reads one session's categories into a ticket part,
// recognising the three shapes the wizard writes (logic doc §5).
func (w *Wizard) loadTickets(ctx context.Context, io EditIO, ws WizSession, eventID uuid.UUID, s openapi.SessionItem, tiers []openapi.TicketTierItem, loc *time.Location) (DraftTickets, error) {
	if len(tiers) == 0 {
		return DraftTickets{}, ErrNotEditable
	}
	if len(tiers) == 1 {
		t := tiers[0]
		out := DraftTickets{Mode: ModeSingle, Categories: []DraftCategory{{TierID: t.Id.String(), Name: t.Name, PriceMinor: t.PriceAmount}}}
		windows, err := io.TierPriceSchedule(ctx, ws.JWT, ws.OrgID, eventID, s.Id, t.Id)
		if err != nil {
			return DraftTickets{}, err
		}
		sort.Slice(windows, func(i, j int) bool { return windows[i].ValidFrom.Before(windows[j].ValidFrom) })
		for _, win := range windows {
			out.Schedule = append(out.Schedule, DraftPriceStep{
				From:       win.ValidFrom.In(loc).Format("2006-01-02"), // allow:timeformat: calendar date of the draft
				PriceMinor: win.PriceAmount,
			})
		}
		return out, nil
	}
	chained := false
	for _, t := range tiers {
		if t.NextTierId != nil {
			chained = true
		}
	}
	if !chained {
		out := DraftTickets{Mode: ModeParallel}
		sort.SliceStable(tiers, func(i, j int) bool { return tiers[i].SortOrder < tiers[j].SortOrder })
		for _, t := range tiers {
			c := DraftCategory{TierID: t.Id.String(), Name: t.Name, PriceMinor: t.PriceAmount}
			if t.Capacity != nil {
				c.Places = int(*t.Capacity)
			}
			out.Categories = append(out.Categories, c)
		}
		return out, nil
	}
	// A chain: walk it from the head (the category nobody hands over to).
	byID := map[uuid.UUID]openapi.TicketTierItem{}
	pointed := map[uuid.UUID]bool{}
	for _, t := range tiers {
		byID[t.Id] = t
		if t.NextTierId != nil {
			pointed[*t.NextTierId] = true
		}
	}
	var head *openapi.TicketTierItem
	for i := range tiers {
		if !pointed[tiers[i].Id] {
			head = &tiers[i]
			break
		}
	}
	if head == nil {
		return DraftTickets{}, ErrNotEditable // a cycle: not something the wizard wrote
	}
	out := DraftTickets{Mode: ModeSequence}
	seen := map[uuid.UUID]bool{}
	for cur := head; cur != nil && !seen[cur.Id]; {
		seen[cur.Id] = true
		c := DraftCategory{TierID: cur.Id.String(), Name: cur.Name, PriceMinor: cur.PriceAmount}
		if cur.SellLimit != nil {
			c.SellLimit = int(*cur.SellLimit)
		}
		// "Sells until" is the day of the window's last second — unless the
		// window simply ends when the session starts, which is the default
		// every step gets and not a date anybody typed.
		if cur.NextTierId != nil && cur.SaleWindowEnd != nil && !cur.SaleWindowEnd.Equal(s.StartAt) {
			c.SellUntil = cur.SaleWindowEnd.Add(-time.Second).In(loc).Format("2006-01-02") // allow:timeformat: calendar date of the draft
		}
		out.Categories = append(out.Categories, c)
		if cur.NextTierId == nil {
			break
		}
		next, ok := byID[*cur.NextTierId]
		if !ok {
			break
		}
		cur = &next
	}
	return out, nil
}

// CopyDraft turns a loaded event into a fresh ModeCreate draft: the ids are
// stripped, the dates cleared, and the wizard resumes at the first date —
// "repeat as new", the organizer's most frequent operation.
func CopyDraft(src *Draft) *Draft {
	d := *src
	d.Mode = ModeCreate
	d.Step = stSDate
	d.History = nil
	d.Event.EventID, d.Event.UpdatedAt = "", ""
	d.Sessions = nil
	d.Cur = 0
	// A copy sells one set of categories on every date: the first date's.
	d.Tickets = withoutTierIDs(src.Tickets)
	d.Changed = nil
	d.Channels = nil
	d.Publish = true
	// The tickets and the rest are already there: once the dates are in, the
	// wizard shows the summary, where any part can still be changed.
	d.Scratch = DraftScratch{ReturnToSummary: true}
	d.Saved = DraftSaved{}
	return &d
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
