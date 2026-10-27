package eventbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
)

// SaveIO is what the save needs from arena-api.
type SaveIO interface {
	MediaSignedURL(ctx context.Context, jwt, mediaID string) (string, error)
	ImportEventBundle(ctx context.Context, jwt string, orgID uuid.UUID, req bil24compat.ImportSessionRequest) (ImportResult, error)
}

// BuildBundle maps one date of the draft onto an event-bundle request
// (logic doc §4). actionID is the event's compat id from the first date's
// response (0 for the first date); posterURL is the poster's download URL
// ("" when there is no poster); externalRef is the date's idempotency key.
//
// The categories are the date's own (DraftSession.Tickets) when it has them,
// else the draft's. An existing date addresses only its own categories by
// arenaTierId — the import looks a tier id up among the categories of THAT
// session and refuses any other (422 import.invalid_category); a date the
// draft adds never carries a tier id at all.
func BuildBundle(d *Draft, idx int, actionID int64, posterURL, externalRef string) bil24compat.ImportSessionRequest {
	s := d.Sessions[idx]
	tk := cloneTickets(*d.ticketsOf(idx))
	if s.SessionID == "" {
		tk = withoutTierIDs(tk)
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil || s.Timezone == "" {
		loc = time.UTC
	}
	startOfDay := func(iso string) time.Time {
		t, _ := time.Parse("2006-01-02", iso)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	promoter := d.Event.PromoterID // "" = the organization itself
	descText, descFormat := d.bundleDescription()
	req := bil24compat.ImportSessionRequest{
		Source:      bil24compat.SourceArena,
		ExternalRef: externalRef,
		Action: bil24compat.ImportSessionAction{
			ActionID:          actionID,
			ArenaEventID:      d.Event.EventID,
			ActionName:        d.Event.Name,
			Age:               d.Event.Age,
			Description:       descText,
			DescriptionFormat: descFormat,
			BigPosterURL:      posterURL,
			PromoterID:        &promoter,
		},
		ActionEvent: bil24compat.ImportSessionActionEvent{
			ArenaSessionID: s.SessionID,
			Day:            DisplayDate(s.Date),
			Time:           s.Time,
			Currency:       d.currencyOrGuess(),
		},
		Venue:   bil24compat.ImportSessionVenue{ArenaVenueID: s.VenueID, VenueName: s.VenueName},
		Publish: d.Publish,
	}
	if s.SessionID != "" {
		// An existing date is addressed by id and keeps the key it was
		// created under; the wire refuses a second key for it.
		req.ExternalRef = ""
	}
	// Sales end and doors open (migration 0128): offsets from the start,
	// sent as instants in the venue's zone. A date the draft read back
	// without a doors time sends "" so a removed one is cleared.
	if day, err := time.ParseInLocation("2006-01-02 15:04", s.Date+" "+s.Time, loc); err == nil {
		sellEnd, doors := saleTimesOnWire(day, s)
		req.ActionEvent.SellEndTime = sellEnd
		if doors == nil && s.SessionID != "" {
			empty := ""
			doors = &empty
		}
		req.ActionEvent.DoorsOpenTime = doors
	}
	for _, c := range d.Channels {
		req.ChannelIDs = append(req.ChannelIDs, c.ID)
	}
	if s.seated() {
		// A date sold by a seating plan: the plan binds the places, the bundle
		// only prices the categories (S1b). Nothing else of the ticket part
		// applies: no chain, no schedule, no quantities.
		req.ActionEvent.ArenaSeatingPlanVersionID = s.PlanVersionID
		req.CategoryList = seatedCategories(tk)
		return req
	}
	minusOne := -1
	zero := int32(0)
	empty := []bil24compat.ImportPriceWindow{}
	switch tk.Mode {
	case ModeSingle:
		if len(tk.Categories) == 0 {
			break
		}
		c := tk.Categories[0]
		windows := []bil24compat.ImportPriceWindow{}
		for i, step := range tk.Schedule {
			w := bil24compat.ImportPriceWindow{ValidFrom: startOfDay(step.From).Format(time.RFC3339), Price: money.Major(step.PriceMinor)}
			if i+1 < len(tk.Schedule) {
				w.ValidTo = startOfDay(tk.Schedule[i+1].From).Format(time.RFC3339)
			}
			windows = append(windows, w)
		}
		req.CategoryList = []bil24compat.ImportSessionCategory{{
			ArenaTierID:       c.TierID,
			CategoryPriceName: c.Name,
			Price:             money.Major(c.PriceMinor),
			Availability:      i32(s.Capacity),
			NextCategoryIndex: &minusOne,
			PriceSchedule:     &windows,
		}}
	case ModeParallel:
		for _, c := range tk.Categories {
			req.CategoryList = append(req.CategoryList, bil24compat.ImportSessionCategory{
				ArenaTierID:       c.TierID,
				CategoryPriceName: c.Name,
				Price:             money.Major(c.PriceMinor),
				Availability:      i32(c.Places),
				NextCategoryIndex: &minusOne,
				PriceSchedule:     &empty,
			})
		}
	case ModeSequence:
		n := len(tk.Categories)
		for i, c := range tk.Categories {
			cat := bil24compat.ImportSessionCategory{
				ArenaTierID:       c.TierID,
				CategoryPriceName: c.Name,
				Price:             money.Major(c.PriceMinor),
				PriceSchedule:     &empty,
			}
			if i == 0 {
				cat.Availability = i32(s.Capacity)
			}
			if i+1 < n {
				next := i + 1
				cat.NextCategoryIndex = &next
				if c.SellLimit > 0 {
					limit := i32(c.SellLimit)
					cat.SellLimit = &limit
				} else {
					cat.SellLimit = &zero
				}
				if c.SellUntil != "" {
					// Inclusive day: the step ends when the next day begins.
					end := startOfDay(c.SellUntil).AddDate(0, 0, 1)
					cat.SellEndTime = end.Format(time.RFC3339)
					// The next category opens at that instant only when this
					// one has no count limit; with a limit it may open earlier.
					if c.SellLimit == 0 {
						req.CategoryList = append(req.CategoryList, cat)
						continue
					}
				}
			} else {
				cat.NextCategoryIndex = &minusOne
				cat.SellLimit = &zero
			}
			req.CategoryList = append(req.CategoryList, cat)
		}
		// Second pass: a category that follows a date-only step starts at
		// that step's end.
		for i := 1; i < len(req.CategoryList); i++ {
			prev := tk.Categories[i-1]
			if prev.SellUntil != "" && prev.SellLimit == 0 {
				req.CategoryList[i].SellStartTime = req.CategoryList[i-1].SellEndTime
			}
		}
	}
	return req
}

// seatedCategories prices the categories of a date sold by a seating plan. A
// new date names each plan category by its index; an existing one by the
// arena id of its category. A schedule and a chain are left as stored (nil), and
// only a standing zone carries a quantity: seats belong to the plan.
func seatedCategories(tk DraftTickets) []bil24compat.ImportSessionCategory {
	var out []bil24compat.ImportSessionCategory
	for _, c := range tk.Categories {
		cat := bil24compat.ImportSessionCategory{
			ArenaTierID:       c.TierID,
			CategoryPriceName: c.Name,
			Price:             money.Major(c.PriceMinor),
			Placement:         !c.GA,
		}
		if c.GA {
			cat.Availability = i32(c.Places)
		}
		if c.TierID == "" && c.PlanIndex > 0 {
			idx := c.PlanIndex
			cat.PlanCategoryIndex = &idx
		}
		out = append(out, cat)
	}
	return out
}

// i32 narrows a place count for the wire; ParseCount already caps counts
// at maxCount, so this can never overflow.
func i32(n int) int32 {
	if n > maxCount {
		return maxCount
	}
	if n < 0 {
		return 0
	}
	return int32(n) // #nosec G115 -- bounded above by maxCount
}

// SaveOutcome is what happened when the draft was saved.
type SaveOutcome struct {
	Done      bool
	FailedIdx int
	Reason    string
	Warnings  []string
}

// Save posts every date of the draft that is not saved yet, in order, and
// records progress in d.Saved so a retry sends only what is missing. The
// first date creates the event; the others join it through its actionId. An
// edited event (ModeEdit) is addressed by its arena ids instead, and only
// the dates sendPlan names are posted — each with its own categories.
func (w *Wizard) Save(ctx context.Context, io SaveIO, ws WizSession, d *Draft, draftID string) SaveOutcome {
	if len(d.Saved.Sessions) < len(d.Sessions) {
		d.Saved.Sessions = append(d.Saved.Sessions, make([]SavedSession, len(d.Sessions)-len(d.Saved.Sessions))...)
	}
	// A change of the event's own parts after a save that stopped half way
	// still needs a date to ride on (keepEventFieldsRiding).
	d.keepEventFieldsRiding()
	plan := d.sendPlan()
	posterURL := ""
	if d.Event.PosterMediaID != "" {
		u, err := io.MediaSignedURL(ctx, ws.JWT, d.Event.PosterMediaID)
		if err == nil {
			posterURL = u
		}
	}
	for i := range d.Sessions {
		if d.Saved.Sessions[i].Done || !plan[i] {
			continue
		}
		ref := fmt.Sprintf("tg:%s:%s:%d", ws.OrgID.String(), draftID, i+1)
		req := BuildBundle(d, i, d.Saved.ActionID, posterURL, ref)
		res, err := io.ImportEventBundle(ctx, ws.JWT, ws.OrgID, req)
		if err != nil {
			d.Saved.LastReason = w.saveReason(ws.Locale, err)
			// What did go out is now what the site has: the card lists only
			// what is still to send.
			d.refreshChanged()
			return SaveOutcome{FailedIdx: i, Reason: d.Saved.LastReason}
		}
		d.Saved.Sessions[i] = SavedSession{ExternalRef: req.ExternalRef, SessionID: res.SessionID, Done: true}
		if d.Saved.ActionID == 0 {
			d.Saved.ActionID = res.CompatIDs.ActionID
			d.Saved.EventID = res.EventID
		}
		if d.Mode == ModeEdit {
			// A date added to an existing event now has an id of its own; a
			// later retry re-sends it by that id, never under a second key.
			d.Sessions[i].SessionID = res.SessionID
			d.recordSaved(i, posterURL != "")
		}
		for _, wn := range res.Warnings {
			if text := w.warningText(ws.Locale, wn.Code); text != "" && !contains(d.Saved.Warnings, text) {
				d.Saved.Warnings = append(d.Saved.Warnings, text)
			}
		}
	}
	d.Saved.LastReason = ""
	return SaveOutcome{Done: true, Warnings: d.Saved.Warnings}
}

func (w *Wizard) saveReason(loc string, err error) string {
	if IsAPIError(err, http.StatusForbidden) {
		return w.texts.T(loc, "bot.wz.err_forbidden", nil)
	}
	var ae *APIError
	if errors.As(err, &ae) && ae.Code == "import.invalid_description" {
		// A refused description: the organizer reads a text of the bot, by the
		// reason, never the English message of the API.
		return w.texts.T(loc, descriptionRefusalKey(ae.Reason), nil)
	}
	if errors.As(err, &ae) && ae.Message != "" {
		return Esc(ae.Message)
	}
	return w.texts.T(loc, "bot.error_generic", nil)
}

// warningText maps an import warning code onto an organizer-facing line;
// codes that mean nothing to an operator ("" ) are dropped.
func (w *Wizard) warningText(loc, code string) string {
	switch strings.TrimPrefix(code, "import.") {
	case "channel_publication_skipped":
		return w.texts.T(loc, "bot.wz.warn_channel_publication_skipped", nil)
	case "hosted_page_enabled":
		return w.texts.T(loc, "bot.wz.warn_hosted_page_enabled", nil)
	case "venue_timezone_kept":
		return w.texts.T(loc, "bot.wz.warn_venue_timezone_kept", nil)
	case "category_sold_out":
		return w.texts.T(loc, "bot.wz.warn_category_sold_out", nil)
	case "publish_skipped":
		return w.texts.T(loc, "bot.wz.warn_publish_skipped", nil)
	case "poster_skipped":
		return w.texts.T(loc, "bot.wz.warn_poster_skipped", nil)
	case "tier_not_in_payload":
		return w.texts.T(loc, "bot.wz.warn_tier_not_in_payload", nil)
	}
	return ""
}

// EnsureChannels guards a NEW event that is about to be published against
// having no sales channel, which makes it "published" with no public page and
// no word to the organizer (a copy of an event starts with no channels, and
// with two or more to choose from and nothing remembered it went straight to
// the summary, 2026-10-07). A single channel is taken on its own; with several
// the draft is sent to the channel question and askChannels is true.
func (w *Wizard) EnsureChannels(ctx context.Context, ws WizSession, d *Draft) (askChannels bool, err error) {
	if d.Mode == ModeEdit || !d.Publish || len(d.Channels) > 0 {
		return false, nil
	}
	channels, err := w.refs.Channels(ctx, ws.JWT, ws.OrgID)
	if err != nil {
		return false, err
	}
	switch {
	case len(channels) == 1:
		d.Channels = channels
	case len(channels) > 1:
		d.History = nil
		d.Scratch.ReturnToSummary = false
		d.Step = stXChannels
		return true, nil
	}
	return false, nil
}

// RememberDefaults folds the draft's answers into the account's defaults.
func RememberDefaults(def Defaults, d *Draft) Defaults {
	def.Age = d.Event.Age
	def.PromoterID, def.PromoterName = d.Event.PromoterID, d.Event.PromoterName
	if len(d.Sessions) > 0 {
		s := d.Sessions[len(d.Sessions)-1]
		def.CountryID, def.CountryName, def.CountryISO2 = s.CountryID, s.CountryName, s.CountryISO2
		def.CityID, def.CityName = s.CityID, s.CityName
		def.VenueID, def.VenueName, def.Timezone, def.Capacity = s.VenueID, s.VenueName, s.Timezone, s.Capacity
	}
	def.Currency = d.Currency
	if len(d.Channels) > 0 {
		def.Channels = d.Channels
	}
	return def
}

// DecodeDefaults reads the stored defaults; an unreadable blob is empty.
func DecodeDefaults(raw json.RawMessage) Defaults {
	var def Defaults
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &def)
	}
	return def
}

func decodeJSON(raw []byte, out any) error {
	return json.Unmarshal(raw, out)
}
