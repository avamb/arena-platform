package eventbot

// wizard.go — the "+ Event" wizard: the question chain of the event
// center (docs/plans/event_center_logic_for_bot_2026-09-28_ru.md §2) as a
// state machine over a Draft. Every transition is a pure function of the
// draft and the input, apart from the reference lookups (countries, cities,
// venues, promoters, channels) and the "+ new …" creations, which go
// through the RefIO interface so tests can fake them.
//
// The chain: event (name, age, promoter, poster) → dates (one or more:
// date, time, country → city → venue, places) → tickets (one category
// with an optional price schedule, or several sold all at once or in
// sequence) → extra (description, currency, channels, publish now?) →
// summary → save (wizard_io.go).

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// draftSchemaVersion is bumped when Draft changes shape; an older draft is
// discarded rather than half-understood.
const draftSchemaVersion = 1

// Steps of the wizard (Draft.Step).
const (
	stEvName        = "ev_name"
	stEvAge         = "ev_age"
	stEvPromoter    = "ev_promoter"
	stPromoterName  = "promoter_name"
	stPromoterLegal = "promoter_legal"
	stEvPoster      = "ev_poster"
	stSDate         = "s_date"
	stSTime         = "s_time"
	stSSame         = "s_same"
	stSCountry      = "s_country"
	stSCity         = "s_city"
	stCityName      = "c_name"
	stSVenue        = "s_venue"
	stVName         = "v_name"
	stVAddress      = "v_address"
	stVCapacity     = "v_capacity"
	stVTz           = "v_tz"
	stSCapacity     = "s_capacity"
	stSMore         = "s_more"
	stTMode         = "t_mode"
	stTName         = "t_name"
	stTPrice        = "t_price"
	stTChanges      = "t_changes"
	stTChangeDate   = "t_change_date"
	stTChangePrice  = "t_change_price"
	stTChangeMore   = "t_change_more"
	stTKind         = "t_kind"
	stTCatName      = "t_cat_name"
	stTCatPrice     = "t_cat_price"
	stTCatPlaces    = "t_cat_places"
	stTCatUntil     = "t_cat_until"
	stTCatLimit     = "t_cat_limit"
	stTCatMore      = "t_cat_more"
	stXDescription  = "x_description"
	stXCurrency     = "x_currency"
	stXChannels     = "x_channels"
	stXPublish      = "x_publish"
	stSummary       = "summary"
	stDone          = "done"
)

// Draft modes.
const (
	ModeCreate = "create"
	ModeEdit   = "edit"
)

// Ticket modes.
const (
	ModeSingle   = "single"   // one category, optional price schedule
	ModeParallel = "parallel" // several categories sold at once
	ModeSequence = "sequence" // several categories sold one after another
)

// AgeOptions are the age ratings the wizard offers (Bil24 order).
var AgeOptions = []string{"0+", "6+", "12+", "16+", "18+"}

// Draft is the wizard state, stored as bot_drafts.state.
type Draft struct {
	Version  int            `json:"version"`
	Mode     string         `json:"mode"` // ModeCreate or ModeEdit
	Step     string         `json:"step"`
	History  []string       `json:"history"`
	Event    DraftEvent     `json:"event"`
	Sessions []DraftSession `json:"sessions"`
	Cur      int            `json:"cur"` // session being edited
	Tickets  DraftTickets   `json:"tickets"`
	Currency string         `json:"currency"`
	Channels []RefItem      `json:"channels"`
	Publish  bool           `json:"publish"`
	Scratch  DraftScratch   `json:"scratch"`
	Saved    DraftSaved     `json:"saved"`
}

// DraftEvent is the event part of the draft.
type DraftEvent struct {
	// EventID and UpdatedAt are set when the draft edits an existing event
	// (ModeEdit): the arena id the bundle addresses, and the event's
	// updated_at at load time, so a change made elsewhere meanwhile is
	// noticed before the save.
	EventID       string `json:"event_id,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	Name          string `json:"name"`
	Age           string `json:"age"`
	PromoterID    string `json:"promoter_id"` // "" = the organization itself
	PromoterName  string `json:"promoter_name"`
	PosterMediaID string `json:"poster_media_id"`
	PosterW       int    `json:"poster_w"`
	PosterH       int    `json:"poster_h"`
	Description   string `json:"description"`
}

// DraftSession is one date of the event.
type DraftSession struct {
	// SessionID is the arena id of an existing session (ModeEdit); "" for a
	// date the draft adds. An existing date is re-sent by id and keeps the
	// key it was created under.
	SessionID   string `json:"session_id,omitempty"`
	Date        string `json:"date"` // YYYY-MM-DD
	Time        string `json:"time"` // HH:MM
	CountryID   string `json:"country_id"`
	CountryName string `json:"country_name"`
	CountryISO2 string `json:"country_iso2"`
	Currency    string `json:"currency"` // the country's currency, for the default
	CityID      string `json:"city_id"`
	CityName    string `json:"city_name"`
	VenueID     string `json:"venue_id"`
	VenueName   string `json:"venue_name"`
	Timezone    string `json:"timezone"`
	Capacity    int    `json:"capacity"`
}

// DraftTickets is the ticket part of the draft.
type DraftTickets struct {
	Mode       string          `json:"mode"`
	Categories []DraftCategory `json:"categories"`
	// Schedule is the single category's price changes (date → price).
	Schedule []DraftPriceStep `json:"schedule"`
}

// DraftCategory is one category.
type DraftCategory struct {
	// TierID is the arena id of an existing category (ModeEdit), carried
	// over by name when the categories are re-entered so a re-priced
	// category is updated rather than minted afresh.
	TierID     string `json:"tier_id,omitempty"`
	Name       string `json:"name"`
	PriceMinor int64  `json:"price_minor"`
	Places     int    `json:"places"`     // parallel: its own places
	SellUntil  string `json:"sell_until"` // sequence: YYYY-MM-DD inclusive, "" = none
	SellLimit  int    `json:"sell_limit"` // sequence: first N tickets, 0 = none
}

// DraftPriceStep is one "from date → price" row of the single category.
type DraftPriceStep struct {
	From       string `json:"from"` // YYYY-MM-DD
	PriceMinor int64  `json:"price_minor"`
}

// DraftScratch holds half-entered sub-dialogs.
type DraftScratch struct {
	// ReturnToSummary is set by the summary's "edit …" buttons: the section
	// being re-entered ends at the summary instead of running on into the
	// next one.
	ReturnToSummary bool `json:"return_to_summary,omitempty"`
	// PrevCats are the categories before "edit tickets" re-entered them;
	// a re-entered category inherits the TierID of its namesake.
	PrevCats   []DraftCategory `json:"prev_cats,omitempty"`
	Cat        DraftCategory   `json:"cat"`
	Step       DraftPriceStep  `json:"step"`
	NewPromo   string          `json:"new_promoter_name"`
	NewCity    string          `json:"new_city_name"`
	NewVenue   VenueCreate     `json:"new_venue"`
	SameAsPrev bool            `json:"same_as_prev"`
}

// DraftSaved is what the non-atomic save already wrote.
type DraftSaved struct {
	ActionID   int64          `json:"action_id"`
	EventID    string         `json:"event_id"`
	EventSlug  string         `json:"event_slug"`
	Sessions   []SavedSession `json:"sessions"`
	Warnings   []string       `json:"warnings"`
	LastReason string         `json:"last_reason"`
}

// SavedSession records one saved date. Done is what the retry looks at: an
// existing session (ModeEdit) is re-sent too, so its id alone proves nothing.
type SavedSession struct {
	ExternalRef string `json:"external_ref"`
	SessionID   string `json:"session_id"`
	Done        bool   `json:"done"`
}

// RefItem is a list entry of a reference (country, city, promoter, channel).
type RefItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ISO2     string `json:"iso2,omitempty"`
	Currency string `json:"currency,omitempty"`
}

// VenueRef is a venue of the organization.
type VenueRef struct {
	ID          string
	Name        string
	CityID      string
	CountryISO2 string
	Timezone    string
	Capacity    int
}

// VenueCreate is the "+ new venue" form.
type VenueCreate struct {
	CountryID   string `json:"country_id"`
	CountryISO2 string `json:"country_iso2"`
	CityID      string `json:"city_id"`
	Name        string `json:"name"`
	Address     string `json:"address"`
	Capacity    int    `json:"capacity"`
	Timezone    string `json:"timezone"`
}

// Defaults are the remembered answers (bot_telegram_links.defaults).
type Defaults struct {
	Age          string    `json:"age,omitempty"`
	PromoterID   string    `json:"promoter_id,omitempty"`
	PromoterName string    `json:"promoter_name,omitempty"`
	CountryID    string    `json:"country_id,omitempty"`
	CountryName  string    `json:"country_name,omitempty"`
	CountryISO2  string    `json:"country_iso2,omitempty"`
	CityID       string    `json:"city_id,omitempty"`
	CityName     string    `json:"city_name,omitempty"`
	VenueID      string    `json:"venue_id,omitempty"`
	VenueName    string    `json:"venue_name,omitempty"`
	Timezone     string    `json:"timezone,omitempty"`
	Capacity     int       `json:"capacity,omitempty"`
	Currency     string    `json:"currency,omitempty"`
	Channels     []RefItem `json:"channels,omitempty"`
}

// RefIO is what the wizard needs from arena-api between questions.
type RefIO interface {
	Countries(ctx context.Context, jwt, lang string) ([]RefItem, error)
	Cities(ctx context.Context, jwt, countryID, lang string) ([]RefItem, error)
	Venues(ctx context.Context, jwt string, orgID uuid.UUID) ([]VenueRef, error)
	Promoters(ctx context.Context, jwt string, orgID uuid.UUID) ([]RefItem, error)
	Channels(ctx context.Context, jwt string, orgID uuid.UUID) ([]RefItem, error)
	CreateCity(ctx context.Context, jwt string, orgID uuid.UUID, countryID, name, lang string) (RefItem, error)
	CreateVenue(ctx context.Context, jwt string, orgID uuid.UUID, v VenueCreate) (VenueRef, error)
	CreatePromoter(ctx context.Context, jwt string, orgID uuid.UUID, name, legalID string) (RefItem, error)
}

// WizSession is who is driving the wizard.
type WizSession struct {
	JWT      string
	OrgID    uuid.UUID
	Locale   string
	Defaults Defaults
}

// WizInput is one answer: a typed text, a button, or an accepted poster.
type WizInput struct {
	Text   string
	Data   string // callback payload without the "wz:" prefix
	Poster *PosterAccepted
}

// PosterAccepted is a poster the bot already validated and uploaded.
type PosterAccepted struct {
	MediaID string
	W, H    int
}

// Button is one inline button; Data is sent back with the "wz:" prefix.
type Button struct {
	Label string
	Data  string
}

// Screen is what the bot shows for the current step.
type Screen struct {
	Text    string
	Buttons [][]Button
}

// ErrCancelled is returned by Apply when the person cancelled the draft.
var ErrCancelled = errors.New("eventbot: wizard cancelled")

// Wizard renders and advances drafts.
type Wizard struct {
	texts *Texts
	refs  RefIO
	now   func() time.Time
}

// NewWizard builds a wizard over the given references.
func NewWizard(texts *Texts, refs RefIO) *Wizard {
	return &Wizard{texts: texts, refs: refs, now: func() time.Time { return time.Now().UTC() }}
}

// NewDraft starts a draft at the first question, seeded with the remembered
// defaults where the question has one.
func NewDraft() *Draft {
	return &Draft{Version: draftSchemaVersion, Mode: ModeCreate, Step: stEvName, Publish: true}
}

// ─── parsing helpers ──────────────────────────────────────────────────────────

// ParseDate accepts DD.MM.YYYY (also with / or -) and returns YYYY-MM-DD.
func ParseDate(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	for _, sep := range []string{"/", "-"} {
		s = strings.ReplaceAll(s, sep, ".")
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return "", false
	}
	d, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	y, err3 := strconv.Atoi(strings.TrimSpace(parts[2]))
	if err1 != nil || err2 != nil || err3 != nil {
		return "", false
	}
	if y < 100 {
		y += 2000
	}
	if y < 2000 || y > 2100 || m < 1 || m > 12 || d < 1 || d > 31 {
		return "", false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Day() != d || int(t.Month()) != m {
		return "", false // 31.02
	}
	return t.Format("2006-01-02"), true // allow:timeformat: calendar date of the draft, no clock
}

// ParseTime accepts HH:MM (also H.MM, HHMM) and returns HH:MM.
func ParseTime(raw string) (string, bool) {
	s := strings.TrimSpace(strings.ReplaceAll(raw, ".", ":"))
	if len(s) == 4 && !strings.Contains(s, ":") {
		s = s[:2] + ":" + s[2:]
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return "", false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", h, m), true
}

// ParsePriceMinor accepts "199", "99,90", "1 200", "1.200,50" and returns
// minor units. Negative and more than two decimals are refused.
func ParsePriceMinor(raw string) (int64, bool) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.TrimRight(s, "€$£₪Kč")
	if s == "" {
		return 0, false
	}
	// The LAST separator is the decimal one when it is followed by 1-2 digits.
	dec := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ',' || s[i] == '.' {
			if n := len(s) - i - 1; n >= 1 && n <= 2 {
				dec = i
			}
			break
		}
	}
	whole, frac := s, ""
	if dec >= 0 {
		whole, frac = s[:dec], s[dec+1:]
	}
	whole = strings.NewReplacer(".", "", ",", "").Replace(whole)
	if whole == "" {
		whole = "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w < 0 {
		return 0, false
	}
	f := int64(0)
	if frac != "" {
		if len(frac) == 1 {
			frac += "0"
		}
		v, err := strconv.ParseInt(frac, 10, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		f = v
	}
	return w*100 + f, true
}

// maxCount is the largest place count the wizard accepts (a stadium is
// ~100k; anything bigger is a typo).
const maxCount = 1_000_000

// ParseCount accepts a positive whole number ("80", "1 200") up to maxCount.
func ParseCount(raw string) (int, bool) {
	s := strings.NewReplacer(" ", "", " ", "", ".", "", ",", "").Replace(strings.TrimSpace(raw))
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > maxCount {
		return 0, false
	}
	return n, true
}

// DisplayDate renders YYYY-MM-DD as DD.MM.YYYY.
func DisplayDate(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return fmt.Sprintf("%02d.%02d.%d", t.Day(), int(t.Month()), t.Year())
}

// ─── transitions ──────────────────────────────────────────────────────────────

func (d *Draft) goTo(step string) {
	if d.Step != step {
		d.History = append(d.History, d.Step)
	}
	d.Step = step
}

// section moves to the first step of the next section — or back to the
// summary when the person came from there ("edit …" buttons), or when an
// edited event already has the part the next section would ask for.
func (d *Draft) section(step string) {
	if d.Scratch.ReturnToSummary {
		d.Scratch.ReturnToSummary = false
		d.goTo(stSummary)
		return
	}
	if d.Mode == ModeEdit && step == stTMode && d.Tickets.Mode != "" {
		d.goTo(stSummary)
		return
	}
	d.goTo(step)
}

// carryTierID gives a re-entered category the arena id of its namesake
// among the categories the edit started from (case-insensitive).
func (d *Draft) carryTierID(c *DraftCategory) {
	for _, p := range d.Scratch.PrevCats {
		if strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(c.Name)) {
			c.TierID = p.TierID
			return
		}
	}
}

func (d *Draft) back() bool {
	if len(d.History) == 0 {
		return false
	}
	d.Step = d.History[len(d.History)-1]
	d.History = d.History[:len(d.History)-1]
	return true
}

func (d *Draft) session() *DraftSession {
	for len(d.Sessions) <= d.Cur {
		d.Sessions = append(d.Sessions, DraftSession{})
	}
	return &d.Sessions[d.Cur]
}

// Apply feeds one answer to the draft. It returns a localized note to show
// above the next question (a validation message, or a confirmation such as
// "Date 1: …"), ErrCancelled when the person cancelled, or an error from
// the references.
func (w *Wizard) Apply(ctx context.Context, ws WizSession, d *Draft, in WizInput) (note string, err error) {
	loc := ws.Locale
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	text := strings.TrimSpace(in.Text)
	data := strings.TrimSpace(in.Data)

	switch data {
	case "cancel":
		return "", ErrCancelled
	case "back":
		if d.Step == stSummary {
			d.goTo(stXPublish)
			return "", nil
		}
		d.back()
		return "", nil
	}

	switch d.Step {
	case stEvName:
		if text == "" || len([]rune(text)) > 200 {
			return t("bot.wz.err_name", nil), nil
		}
		d.Event.Name = text
		d.goTo(stEvAge)

	case stEvAge:
		age := strings.TrimPrefix(data, "age:")
		if data == "keep" {
			age = ws.Defaults.Age
		}
		if !contains(AgeOptions, age) {
			return "", nil
		}
		d.Event.Age = age
		d.goTo(stEvPromoter)

	case stEvPromoter:
		switch {
		case data == "prom:org":
			d.Event.PromoterID, d.Event.PromoterName = "", ""
			d.goTo(stEvPoster)
		case data == "prom:new":
			d.goTo(stPromoterName)
		case data == "keep":
			d.Event.PromoterID, d.Event.PromoterName = ws.Defaults.PromoterID, ws.Defaults.PromoterName
			d.goTo(stEvPoster)
		case strings.HasPrefix(data, "prom:"):
			id := strings.TrimPrefix(data, "prom:")
			promoters, err := w.refs.Promoters(ctx, ws.JWT, ws.OrgID)
			if err != nil {
				return "", err
			}
			for _, p := range promoters {
				if p.ID == id {
					d.Event.PromoterID, d.Event.PromoterName = p.ID, p.Name
					d.goTo(stEvPoster)
					return "", nil
				}
			}
		}

	case stPromoterName:
		if text == "" {
			return t("bot.wz.err_promoter_name", nil), nil
		}
		promoters, err := w.refs.Promoters(ctx, ws.JWT, ws.OrgID)
		if err != nil {
			return "", err
		}
		for _, p := range promoters {
			if strings.EqualFold(strings.TrimSpace(p.Name), text) {
				d.back()
				return t("bot.wz.err_promoter_dup", nil), nil
			}
		}
		d.Scratch.NewPromo = text
		d.goTo(stPromoterLegal)

	case stPromoterLegal:
		legal := ""
		if data != "skip" {
			legal = text
		}
		p, err := w.refs.CreatePromoter(ctx, ws.JWT, ws.OrgID, d.Scratch.NewPromo, legal)
		if err != nil {
			if IsAPIError(err, 409) {
				d.History = nil
				d.Step = stEvPromoter
				return t("bot.wz.err_promoter_dup", nil), nil
			}
			return "", err
		}
		d.Event.PromoterID, d.Event.PromoterName = p.ID, p.Name
		d.Scratch.NewPromo = ""
		d.goTo(stEvPoster)

	case stEvPoster:
		switch {
		case in.Poster != nil:
			d.Event.PosterMediaID, d.Event.PosterW, d.Event.PosterH = in.Poster.MediaID, in.Poster.W, in.Poster.H
			note = t("bot.wz.poster_ok", map[string]any{"W": in.Poster.W, "H": in.Poster.H})
			d.section(stSDate)
		case data == "skip":
			d.section(stSDate)
		default:
			return "", nil
		}

	case stSDate:
		iso, ok := ParseDate(text)
		if !ok {
			return t("bot.wz.err_date", nil), nil
		}
		d.session().Date = iso
		d.goTo(stSTime)

	case stSTime:
		hhmm := "20:00"
		if data != "default" {
			v, ok := ParseTime(text)
			if !ok {
				return t("bot.wz.err_time", nil), nil
			}
			hhmm = v
		}
		d.session().Time = hhmm
		if d.Cur > 0 {
			d.goTo(stSSame)
		} else {
			d.goTo(stSCountry)
		}

	case stSSame:
		switch data {
		case "same":
			prev := d.Sessions[d.Cur-1]
			s := d.session()
			s.CountryID, s.CountryName, s.CountryISO2, s.Currency = prev.CountryID, prev.CountryName, prev.CountryISO2, prev.Currency
			s.CityID, s.CityName, s.VenueID, s.VenueName, s.Timezone = prev.CityID, prev.CityName, prev.VenueID, prev.VenueName, prev.Timezone
			s.Capacity = prev.Capacity
			if dup := d.duplicateSession(); dup {
				return t("bot.wz.err_duplicate_session", nil), nil
			}
			note = w.sessionAddedNote(loc, d)
			d.goTo(stSMore)
		case "other":
			d.goTo(stSCountry)
		}

	case stSCountry:
		var chosen *RefItem
		countries, err := w.refs.Countries(ctx, ws.JWT, loc)
		if err != nil {
			return "", err
		}
		id := strings.TrimPrefix(data, "country:")
		if data == "keep" {
			id = ws.Defaults.CountryID
		}
		for i := range countries {
			if countries[i].ID == id {
				chosen = &countries[i]
			}
		}
		if chosen == nil {
			return "", nil
		}
		s := d.session()
		s.CountryID, s.CountryName, s.CountryISO2, s.Currency = chosen.ID, chosen.Name, chosen.ISO2, chosen.Currency
		s.CityID, s.CityName, s.VenueID, s.VenueName, s.Timezone = "", "", "", "", ""
		d.goTo(stSCity)

	case stSCity:
		s := d.session()
		if data == "city:new" {
			d.goTo(stCityName)
			return "", nil
		}
		id := strings.TrimPrefix(data, "city:")
		if data == "keep" {
			id = ws.Defaults.CityID
		}
		cities, err := w.refs.Cities(ctx, ws.JWT, s.CountryID, loc)
		if err != nil {
			return "", err
		}
		for _, c := range cities {
			if c.ID == id {
				s.CityID, s.CityName = c.ID, c.Name
				s.VenueID, s.VenueName, s.Timezone = "", "", ""
				d.goTo(stSVenue)
				return "", nil
			}
		}

	case stCityName:
		if text == "" {
			return t("bot.wz.err_city_name", nil), nil
		}
		s := d.session()
		c, err := w.refs.CreateCity(ctx, ws.JWT, ws.OrgID, s.CountryID, text, loc)
		if err != nil {
			return "", err
		}
		s.CityID, s.CityName = c.ID, c.Name
		d.goTo(stSVenue)

	case stSVenue:
		s := d.session()
		if data == "venue:new" {
			d.Scratch.NewVenue = VenueCreate{CountryID: s.CountryID, CountryISO2: s.CountryISO2, CityID: s.CityID}
			d.goTo(stVName)
			return "", nil
		}
		id := strings.TrimPrefix(data, "venue:")
		if data == "keep" {
			id = ws.Defaults.VenueID
		}
		venues, err := w.refs.Venues(ctx, ws.JWT, ws.OrgID)
		if err != nil {
			return "", err
		}
		for _, v := range venues {
			if v.ID == id {
				s.VenueID, s.VenueName, s.Timezone = v.ID, v.Name, v.Timezone
				if s.Capacity == 0 {
					s.Capacity = v.Capacity
				}
				d.goTo(stSCapacity)
				return "", nil
			}
		}

	case stVName:
		if text == "" {
			return t("bot.wz.err_venue_name", nil), nil
		}
		d.Scratch.NewVenue.Name = text
		d.goTo(stVAddress)

	case stVAddress:
		if data != "skip" {
			d.Scratch.NewVenue.Address = text
		}
		d.goTo(stVCapacity)

	case stVCapacity:
		if data != "skip" {
			n, ok := ParseCount(text)
			if !ok {
				return t("bot.wz.err_capacity", nil), nil
			}
			d.Scratch.NewVenue.Capacity = n
		}
		if tz := w.guessTimezone(ctx, ws, d.Scratch.NewVenue.CountryISO2); tz != "" {
			d.Scratch.NewVenue.Timezone = tz
			return w.createVenue(ctx, ws, d)
		}
		d.goTo(stVTz)

	case stVTz:
		if _, err := time.LoadLocation(text); err != nil || text == "" {
			return t("bot.wz.err_venue_tz", nil), nil
		}
		d.Scratch.NewVenue.Timezone = text
		return w.createVenue(ctx, ws, d)

	case stSCapacity:
		s := d.session()
		if data == "keep" && s.Capacity > 0 {
			// keep the pre-filled value
		} else {
			n, ok := ParseCount(text)
			if !ok {
				return t("bot.wz.err_capacity", nil), nil
			}
			s.Capacity = n
		}
		if d.duplicateSession() {
			return t("bot.wz.err_duplicate_session", nil), nil
		}
		note = w.sessionAddedNote(loc, d)
		d.goTo(stSMore)

	case stSMore:
		switch data {
		case "more":
			d.Cur = len(d.Sessions)
			d.goTo(stSDate)
		case "next":
			d.section(stTMode)
		}

	case stTMode:
		switch data {
		case "mode:single":
			d.Tickets = DraftTickets{Mode: ModeSingle}
			d.goTo(stTName)
		case "mode:multi":
			d.Tickets = DraftTickets{}
			d.goTo(stTKind)
		}

	case stTName:
		name := text
		if data == "default" {
			name = t("bot.wz.t_name_default", nil)
		}
		if name == "" {
			return t("bot.wz.err_name", nil), nil
		}
		cat := DraftCategory{Name: name}
		if d.Mode == ModeEdit {
			if len(d.Scratch.PrevCats) == 1 {
				cat.TierID = d.Scratch.PrevCats[0].TierID // the single category, whatever it is called now
			} else {
				d.carryTierID(&cat)
			}
		}
		d.Tickets.Categories = []DraftCategory{cat}
		d.goTo(stTPrice)

	case stTPrice:
		p, ok := ParsePriceMinor(text)
		if !ok {
			return t("bot.wz.err_price", nil), nil
		}
		d.Tickets.Categories[0].PriceMinor = p
		d.goTo(stTChanges)

	case stTChanges:
		switch data {
		case "no":
			d.Tickets.Schedule = nil
			d.section(stXDescription)
		case "yes":
			d.goTo(stTChangeDate)
		}

	case stTChangeDate:
		iso, ok := ParseDate(text)
		if !ok {
			return t("bot.wz.err_date", nil), nil
		}
		if n := len(d.Tickets.Schedule); n > 0 && iso <= d.Tickets.Schedule[n-1].From {
			return t("bot.wz.err_change_date_order", nil), nil
		}
		d.Scratch.Step = DraftPriceStep{From: iso}
		d.goTo(stTChangePrice)

	case stTChangePrice:
		p, ok := ParsePriceMinor(text)
		if !ok {
			return t("bot.wz.err_price", nil), nil
		}
		d.Scratch.Step.PriceMinor = p
		d.Tickets.Schedule = append(d.Tickets.Schedule, d.Scratch.Step)
		d.Scratch.Step = DraftPriceStep{}
		d.goTo(stTChangeMore)

	case stTChangeMore:
		switch data {
		case "more":
			d.goTo(stTChangeDate)
		case "done":
			d.section(stXDescription)
		}

	case stTKind:
		switch data {
		case "kind:parallel":
			d.Tickets.Mode = ModeParallel
		case "kind:sequence":
			d.Tickets.Mode = ModeSequence
		default:
			return "", nil
		}
		d.Tickets.Categories = nil
		d.Scratch.Cat = DraftCategory{}
		d.goTo(stTCatName)

	case stTCatName:
		if text == "" {
			return t("bot.wz.err_name", nil), nil
		}
		for _, c := range d.Tickets.Categories {
			if strings.EqualFold(c.Name, text) {
				return t("bot.wz.err_cat_dup", map[string]any{"Name": Esc(text)}), nil
			}
		}
		d.Scratch.Cat = DraftCategory{Name: text}
		d.goTo(stTCatPrice)

	case stTCatPrice:
		p, ok := ParsePriceMinor(text)
		if !ok {
			return t("bot.wz.err_price", nil), nil
		}
		d.Scratch.Cat.PriceMinor = p
		if d.Tickets.Mode == ModeParallel {
			d.goTo(stTCatPlaces)
		} else {
			d.goTo(stTCatUntil)
		}

	case stTCatPlaces:
		n, ok := ParseCount(text)
		if !ok {
			return t("bot.wz.err_places", nil), nil
		}
		d.Scratch.Cat.Places = n
		d.carryTierID(&d.Scratch.Cat)
		d.Tickets.Categories = append(d.Tickets.Categories, d.Scratch.Cat)
		d.Scratch.Cat = DraftCategory{}
		d.goTo(stTCatMore)

	case stTCatUntil:
		if data != "skip" {
			iso, ok := ParseDate(text)
			if !ok {
				return t("bot.wz.err_date", nil), nil
			}
			if prev := lastUntil(d.Tickets.Categories); prev != "" && iso < prev {
				return t("bot.wz.err_until_order", map[string]any{"Name": Esc(d.Scratch.Cat.Name)}), nil
			}
			d.Scratch.Cat.SellUntil = iso
		}
		d.goTo(stTCatLimit)

	case stTCatLimit:
		if data != "skip" {
			n, ok := ParseCount(text)
			if !ok {
				return t("bot.wz.err_places", nil), nil
			}
			d.Scratch.Cat.SellLimit = n
		}
		if d.Scratch.Cat.SellUntil == "" && d.Scratch.Cat.SellLimit == 0 {
			d.Step = stTCatUntil
			return t("bot.wz.err_cat_need_end", map[string]any{"Name": Esc(d.Scratch.Cat.Name)}), nil
		}
		d.carryTierID(&d.Scratch.Cat)
		d.Tickets.Categories = append(d.Tickets.Categories, d.Scratch.Cat)
		d.Scratch.Cat = DraftCategory{}
		d.goTo(stTCatMore)

	case stTCatMore:
		switch data {
		case "more":
			d.goTo(stTCatName)
		case "done":
			if len(d.Tickets.Categories) < 2 {
				return t("bot.wz.err_min_two", nil), nil
			}
			if d.Tickets.Mode == ModeSequence {
				// The last category sells until the session starts: its own
				// end, if one was typed, is dropped (spec: the last one has none).
				last := &d.Tickets.Categories[len(d.Tickets.Categories)-1]
				last.SellUntil, last.SellLimit = "", 0
			}
			d.section(stXDescription)
		}

	case stXDescription:
		if data != "skip" {
			if len([]rune(text)) > 4000 {
				text = string([]rune(text)[:4000])
			}
			d.Event.Description = text
		}
		d.goTo(stXCurrency)

	case stXCurrency:
		cur := strings.ToUpper(text)
		if strings.HasPrefix(data, "cur:") {
			cur = strings.TrimPrefix(data, "cur:")
		}
		if len(cur) != 3 || strings.Trim(cur, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			return t("bot.wz.err_currency", nil), nil
		}
		d.Currency = cur
		if d.Mode == ModeEdit {
			// Where an existing event sells is not changed from the bot: the
			// bindings it has stay, none are added.
			d.goTo(stXPublish)
			return "", nil
		}
		channels, err := w.refs.Channels(ctx, ws.JWT, ws.OrgID)
		if err != nil {
			return "", err
		}
		switch {
		case len(channels) <= 1:
			d.Channels = channels
			d.goTo(stXPublish)
		case len(ws.Defaults.Channels) > 0 && subset(ws.Defaults.Channels, channels):
			d.Channels = ws.Defaults.Channels
			d.goTo(stXPublish)
		default:
			d.goTo(stXChannels)
		}

	case stXChannels:
		switch {
		case strings.HasPrefix(data, "ch:"):
			id := strings.TrimPrefix(data, "ch:")
			channels, err := w.refs.Channels(ctx, ws.JWT, ws.OrgID)
			if err != nil {
				return "", err
			}
			for i, c := range d.Channels {
				if c.ID == id {
					d.Channels = append(d.Channels[:i], d.Channels[i+1:]...)
					return "", nil
				}
			}
			for _, c := range channels {
				if c.ID == id {
					d.Channels = append(d.Channels, c)
				}
			}
		case data == "done":
			if len(d.Channels) == 0 {
				return t("bot.wz.err_channels_empty", nil), nil
			}
			d.goTo(stXPublish)
		}

	case stXPublish:
		switch data {
		case "pub:now":
			d.Publish = true
		case "pub:later":
			d.Publish = false
		default:
			return "", nil
		}
		d.goTo(stSummary)

	case stSummary:
		switch data {
		case "edit:event":
			d.History = nil
			d.Scratch.ReturnToSummary = true
			d.Step = stEvName
		case "edit:when":
			d.History = nil
			d.Scratch.ReturnToSummary = true
			if d.Mode == ModeEdit {
				// Existing dates stay as they are; the bot only adds new ones.
				d.Cur = len(d.Sessions)
				d.Step = stSDate
				break
			}
			d.Cur = 0
			d.Sessions = nil
			d.Step = stSDate
		case "edit:tickets":
			d.History = nil
			d.Scratch.ReturnToSummary = true
			if d.Mode == ModeEdit {
				// "One or several" and "all at once or in turn" are fixed once
				// the event exists; the categories are re-entered in place.
				d.Scratch.PrevCats = d.Tickets.Categories
				if d.Tickets.Mode == ModeSingle {
					d.Tickets.Categories = nil
					d.Tickets.Schedule = nil
					d.Step = stTName
				} else {
					d.Tickets.Categories = nil
					d.Scratch.Cat = DraftCategory{}
					d.Step = stTCatName
				}
				break
			}
			d.Step = stTMode
		case "edit:extra":
			d.History = nil
			d.Scratch.ReturnToSummary = true
			d.Step = stXDescription
		}
	}
	return note, nil
}

func (w *Wizard) createVenue(ctx context.Context, ws WizSession, d *Draft) (string, error) {
	v, err := w.refs.CreateVenue(ctx, ws.JWT, ws.OrgID, d.Scratch.NewVenue)
	if err != nil {
		if IsAPIError(err, 409) || IsAPIError(err, 422) || IsAPIError(err, 400) {
			d.History = nil
			d.Step = stSVenue
			return w.texts.T(ws.Locale, "bot.wz.venue_failed", map[string]any{"Reason": Esc(apiReason(err))}), nil
		}
		return "", err
	}
	s := d.session()
	s.VenueID, s.VenueName, s.Timezone = v.ID, v.Name, v.Timezone
	if s.Capacity == 0 {
		s.Capacity = v.Capacity
	}
	d.Scratch.NewVenue = VenueCreate{}
	d.goTo(stSCapacity)
	return "", nil
}

// guessTimezone picks the zone of a new venue: the zone of another venue
// of the organization in the same country, else a built-in default for
// single-zone countries, else "" (the wizard asks).
func (w *Wizard) guessTimezone(ctx context.Context, ws WizSession, iso2 string) string {
	if venues, err := w.refs.Venues(ctx, ws.JWT, ws.OrgID); err == nil {
		for _, v := range venues {
			if strings.EqualFold(v.CountryISO2, iso2) && v.Timezone != "" {
				return v.Timezone
			}
		}
	}
	return countryTimezones[strings.ToUpper(iso2)]
}

// countryTimezones covers the single-zone countries arena sells in; every
// other country is asked explicitly.
var countryTimezones = map[string]string{
	"CZ": "Europe/Prague", "SK": "Europe/Bratislava", "HU": "Europe/Budapest", "AT": "Europe/Vienna",
	"DE": "Europe/Berlin", "PL": "Europe/Warsaw", "IT": "Europe/Rome", "FR": "Europe/Paris",
	"NL": "Europe/Amsterdam", "BE": "Europe/Brussels", "CH": "Europe/Zurich", "GB": "Europe/London",
	"IE": "Europe/Dublin", "DK": "Europe/Copenhagen", "SE": "Europe/Stockholm", "NO": "Europe/Oslo",
	"FI": "Europe/Helsinki", "EE": "Europe/Tallinn", "LV": "Europe/Riga", "LT": "Europe/Vilnius",
	"IL": "Asia/Jerusalem", "CY": "Asia/Nicosia", "GR": "Europe/Athens", "BG": "Europe/Sofia",
	"RO": "Europe/Bucharest", "HR": "Europe/Zagreb", "SI": "Europe/Ljubljana", "RS": "Europe/Belgrade",
	"TR": "Europe/Istanbul", "GE": "Asia/Tbilisi", "AM": "Asia/Yerevan", "UA": "Europe/Kyiv",
	"MD": "Europe/Chisinau", "BY": "Europe/Minsk", "LU": "Europe/Luxembourg", "MT": "Europe/Malta",
	"AE": "Asia/Dubai", "ME": "Europe/Podgorica", "AL": "Europe/Tirane", "MK": "Europe/Skopje",
	"BA": "Europe/Sarajevo", "IS": "Atlantic/Reykjavik",
}

func (w *Wizard) sessionAddedNote(loc string, d *Draft) string {
	s := d.Sessions[d.Cur]
	return w.texts.T(loc, "bot.wz.session_added", map[string]any{
		"N": d.Cur + 1, "When": DisplayDate(s.Date) + " " + s.Time,
		"Venue": Esc(s.VenueName), "City": Esc(s.CityName), "Capacity": s.Capacity,
	})
}

func (d *Draft) duplicateSession() bool {
	cur := d.Sessions[d.Cur]
	for i, s := range d.Sessions {
		if i != d.Cur && s.Date == cur.Date && s.Time == cur.Time && s.VenueID == cur.VenueID {
			return true
		}
	}
	return false
}

func lastUntil(cats []DraftCategory) string {
	for i := len(cats) - 1; i >= 0; i-- {
		if cats[i].SellUntil != "" {
			return cats[i].SellUntil
		}
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// subset reports whether every remembered channel still exists.
func subset(remembered, all []RefItem) bool {
	for _, r := range remembered {
		found := false
		for _, c := range all {
			if c.ID == r.ID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func apiReason(err error) string {
	var ae *APIError
	if errors.As(err, &ae) && ae.Message != "" {
		return ae.Message
	}
	return err.Error()
}

// sortedByName sorts reference items for a keyboard.
func sortedByName(items []RefItem) []RefItem {
	out := append([]RefItem{}, items...)
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}
