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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
)

// draftSchemaVersion is bumped when Draft changes shape; an older draft is
// discarded rather than half-understood.
const draftSchemaVersion = 4

// Steps of the wizard (Draft.Step).
const (
	stEvName        = "ev_name"
	stEvAge         = "ev_age"
	stEvPromoter    = "ev_promoter"
	stPromoterName  = "promoter_name"
	stPromoterLegal = "promoter_legal"
	stEvPoster      = "ev_poster"
	stEvPosterAsk   = "ev_poster_ask"
	stSDate         = "s_date"
	stSTime         = "s_time"
	stSSalesEnd     = "s_sales_end"
	stSSalesEndTime = "s_sales_end_time"
	stSDoors        = "s_doors"
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
	stSPlan         = "s_plan"       // a venue with seating plans: sell by a plan or without seats
	stSPlanPrice    = "s_plan_price" // the price of each category of the chosen plan
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
	stTCatLast      = "t_cat_last"
	stTCatUntil     = "t_cat_until"
	stTCatMore      = "t_cat_more"
	stTDate         = "t_date"    // edited event: which date's categories change
	stSTickets      = "s_tickets" // edited event, new date: same categories or its own
	stSConfirm      = "s_confirm" // edited event, new date: its summary before "one more date?"
	stXDescription  = "x_description"
	stXCurrency     = "x_currency"
	stXChannels     = "x_channels"
	stXPublish      = "x_publish"
	stSummary       = "summary"
	stEditMenu      = "edit_menu"
	stPreview       = "publish_preview" // edited event: what "Publish changes" sends
	stCancel        = "cancel_confirm"
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
	// Hints is what the poster said (wizard_hints.go): offered as buttons,
	// never applied on its own.
	Hints DraftHints `json:"hints,omitempty"`
	// Changed lists the parts of an edited event that differ from what was
	// loaded ("name", "description", "dates", "tickets", …). It is worked out
	// again after every answer (refreshChanged, wizard_undo.go), so an undone
	// change leaves it: the edit card marks these parts and offers "Publish
	// changes" only while there is something to publish.
	Changed []string `json:"changed,omitempty"`
	// Loaded is the edited event as it was read from arena: what an undo on
	// the card goes back to (wizard_undo.go). Nil for a new event.
	Loaded *DraftLoaded `json:"loaded,omitempty"`
	// Paused is set when the person opened the menu with the draft half
	// way (menu_keyboard.go): a typed text or a poster is then not taken as
	// an answer until a wizard button continues the draft.
	Paused bool `json:"paused,omitempty"`
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
	// DescriptionFormat is the format Description is in: "art" (as stored on
	// the site), "text" (typed in the bot, or an old stored text) or ""
	// (unknown: a draft saved before the format was kept). See
	// wizard_description.go.
	DescriptionFormat string `json:"description_format,omitempty"`
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
	// SalesEndMin is when ticket sales close, in minutes after the start
	// (0 = at the start); DoorsMin when the doors open, in minutes before
	// it (0 = not given). See wizard_sale_times.go.
	SalesEndMin int `json:"sales_end_min,omitempty"`
	DoorsMin    int `json:"doors_min,omitempty"`
	// Tickets are this date's own categories (wizard_date_tickets.go). Every
	// date of an edited event carries them, because two dates of one event
	// may sell different categories; a new event leaves them nil and every
	// date sells Draft.Tickets.
	Tickets *DraftTickets `json:"tickets,omitempty"`
	// Dirty marks an existing date whose categories were changed in this
	// edit: only such dates (and new ones) are sent again on save.
	Dirty bool `json:"dirty,omitempty"`
	// CopiedFrom names the date ("18.12.2026 18:00") whose categories a date
	// added to an edited event copied; "" when it has categories of its own.
	CopiedFrom string `json:"copied_from,omitempty"`
	// PlanVersionID is the seating plan version a date is sold by (wizard_seated.go);
	// "" for a date sold without seats. PlanName is only for the screens.
	PlanVersionID string `json:"plan_version_id,omitempty"`
	PlanName      string `json:"plan_name,omitempty"`
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
	// PlanIndex is the 1-based seating plan category a category of a seated
	// date prices (new dates only); GA marks a standing zone of the plan.
	PlanIndex int  `json:"plan_index,omitempty"`
	GA        bool `json:"ga,omitempty"`
}

// DraftPriceStep is one "from date → price" row of the single category.
type DraftPriceStep struct {
	From       string `json:"from"` // YYYY-MM-DD
	PriceMinor int64  `json:"price_minor"`
}

// DraftScratch holds half-entered sub-dialogs.
type DraftScratch struct {
	// CountryRegion is the region whose countries the country question shows;
	// empty shows the region buttons.
	CountryRegion string `json:"country_region,omitempty"`
	// ReturnToSummary is set by the summary's "edit …" buttons: the section
	// being re-entered ends at the summary instead of running on into the
	// next one.
	ReturnToSummary bool `json:"return_to_summary,omitempty"`
	// PrevCats are the categories before "edit tickets" re-entered them;
	// a re-entered category inherits the TierID of its namesake.
	PrevCats []DraftCategory `json:"prev_cats,omitempty"`
	// PrevSchedule is the single category's price changes before "Prices and
	// categories" re-entered them; leaving that screen restores both.
	PrevSchedule []DraftPriceStep `json:"prev_schedule,omitempty"`
	// PrevSessions is how many dates the event had when "+ Date" opened; a
	// half-entered date beyond them is dropped when the person goes back.
	PrevSessions int `json:"prev_sessions,omitempty"`
	// Edit names the part of an edited event being changed ("name", "age",
	// "promoter", "poster", "description", "currency", "dates", "tickets").
	Edit string `json:"edit,omitempty"`
	// CancelFrom is the step the cancel confirmation returns to.
	CancelFrom string `json:"cancel_from,omitempty"`
	// CalMonth is the month ("2026-11") the date calendar shows; "" means the
	// first month that has an allowed day. DatePending holds the dates a typed
	// answer could mean while the person confirms one of them (DateRaw is what
	// they typed, DateLimit the ticket count typed beside it).
	CalMonth    string         `json:"cal_month,omitempty"`
	DatePending []string       `json:"date_pending,omitempty"`
	DateRaw     string         `json:"date_raw,omitempty"`
	DateLimit   int            `json:"date_limit,omitempty"`
	Cat         DraftCategory  `json:"cat"`
	Step        DraftPriceStep `json:"step"`
	NewPromo    string         `json:"new_promoter_name"`
	NewCity     string         `json:"new_city_name"`
	NewVenue    VenueCreate    `json:"new_venue"`
	// TzHint is the zone a typed city name pointed at, offered as a button on
	// the zone question until the person confirms it.
	TzHint string `json:"tz_hint,omitempty"`
	// DupVenueID/DupVenueName name the venue a refused "+ new venue" already
	// is (same name in the organization): the venue question then offers it
	// as a button, so a refusal always has a way forward.
	DupVenueID   string `json:"dup_venue_id,omitempty"`
	DupVenueName string `json:"dup_venue_name,omitempty"`
	SameAsPrev   bool   `json:"same_as_prev"`
	// Work is the ticket part the ticket questions fill while an edited
	// event changes the categories of one date (WorkFor, 1-based) or of every
	// date (WorkAll); nil means the questions fill Draft.Tickets.
	Work    *DraftTickets `json:"work,omitempty"`
	WorkFor int           `json:"work_for,omitempty"`
	WorkAll bool          `json:"work_all,omitempty"`
	// Plans are the seating plans of the venue just chosen; SeatWork the
	// categories of a seated date being priced, SeatIdx the one asked now and
	// SeatFor the date (1-based) of an edited event being re-priced (0 = the
	// date being entered).
	Plans    []PlanRef     `json:"plans,omitempty"`
	SeatWork *DraftTickets `json:"seat_work,omitempty"`
	SeatIdx  int           `json:"seat_idx,omitempty"`
	SeatFor  int           `json:"seat_for,omitempty"`
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
	ID   string `json:"id"`
	Name string `json:"name"`
	// Slug is a promoter's public page address (migration 0117); empty for
	// every other kind of reference and for a promoter without a page.
	Slug     string `json:"slug,omitempty"`
	ISO2     string `json:"iso2,omitempty"`
	Currency string `json:"currency,omitempty"`
	// Region groups a country for the picker (migration 0127).
	Region string `json:"region,omitempty"`
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
	JWT   string
	OrgID uuid.UUID
	// OrgName is what the wizard calls the organization on screen — "Arena
	// Test Promotions", never "your organization": an organizer does not
	// know what an "organization" is, but recognizes their own name.
	OrgName  string
	Locale   string
	Defaults Defaults
	// Superadmin is the platform operator: arena-api gives them no mini app
	// sign-in code, so the bot draws no «Править в редакторе» button for them.
	// The wizard itself never reads it.
	Superadmin bool
}

// WizInput is one answer: a typed text, a button, or an accepted poster.
type WizInput struct {
	Text   string
	Data   string // callback payload without the "wz:" prefix
	Poster *PosterAccepted
	// Art is a description the organizer approved in the preview (RT-05):
	// ART that already went through the server's filter. It answers the
	// description question.
	Art string
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
	// URL, when set, makes a link button (Data is then unused): an ordinary
	// one, or a Telegram Mini App one when WebApp is true. Only the screens
	// that build their keyboard with sesKeyboard honour it.
	URL    string
	WebApp bool
}

// Screen is what the bot shows for the current step.
type Screen struct {
	Text    string
	Buttons [][]Button
}

// ErrCancelled is returned by Apply when the person cancelled the draft.
var ErrCancelled = errors.New("eventbot: wizard cancelled")

// ErrSavedExit is returned by Apply when the person left the wizard but
// chose to keep the draft: the caller stores it as it is and shows the menu.
var ErrSavedExit = errors.New("eventbot: wizard left, draft kept")

// Wizard renders and advances drafts.
type Wizard struct {
	texts *Texts
	refs  RefIO
	now   func() time.Time
	// posterHints is set when a poster reader is wired: the first question
	// then invites the poster and says where it goes.
	posterHints bool
}

// NewWizard builds a wizard over the given references.
func NewWizard(texts *Texts, refs RefIO) *Wizard {
	return &Wizard{texts: texts, refs: refs, now: func() time.Time { return time.Now().UTC() }}
}

// WithPosterHints tells the wizard that poster reading is offered. The bot
// always says true: the model key lives on arena-api and the bot cannot tell
// whether it is set; a poster read that the API cannot do is simply skipped.
func (w *Wizard) WithPosterHints(on bool) *Wizard {
	w.posterHints = on
	return w
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

// countShape is a whole number as a person types it: digits, or digits grouped
// by thousands ("1 200", "1,200"). It keeps "31.12.2026 1.1.2027" and "5 6 7"
// from being read as one big count.
var countShape = regexp.MustCompile(`^\d+$|^\d{1,3}([  ,]\d{3})+$`)

// ParseCategoryEnd reads the one answer that ends a category's price: a date
// (DD.MM.YYYY, inclusive), a ticket count, or both ("31.12.2026 50" — the
// price ends at whichever comes first).
func ParseCategoryEnd(raw string) (until string, limit int, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", 0, false
	}
	if iso, ok := ParseDate(s); ok {
		return iso, 0, true
	}
	if countShape.MatchString(s) {
		if n, ok := ParseCount(s); ok {
			return "", n, true
		}
	}
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == ';' })
	if len(fields) != 2 {
		return "", 0, false
	}
	for _, f := range fields {
		if iso, ok := ParseDate(f); ok && until == "" {
			until = iso
			continue
		}
		if n, ok := ParseCount(f); ok && limit == 0 && countShape.MatchString(f) {
			limit = n
			continue
		}
		return "", 0, false
	}
	return until, limit, until != "" && limit > 0
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
		d.clearDateDialog()
	}
	d.Step = step
}

// clearDateDialog forgets a half-done date question (calendar month, typed
// date waiting for a confirmation) when the person moves to another step.
func (d *Draft) clearDateDialog() {
	d.Scratch.CalMonth, d.Scratch.DatePending, d.Scratch.DateRaw, d.Scratch.DateLimit = "", nil, "", 0
}

// section moves to the first step of the next section — or back to the
// summary (the edit card, for an edited event) when the person came from
// there, or when an edited event already has the part the next section would
// ask for.
func (d *Draft) section(step string) {
	if d.Scratch.ReturnToSummary {
		d.Scratch.ReturnToSummary = false
		d.goHome()
		return
	}
	if d.Mode == ModeEdit && step == stTMode && d.Tickets.Mode != "" {
		d.goHome()
		return
	}
	d.goTo(step)
}

// singleFields are the parts of an edited event that are one question each:
// answering it returns straight to the edit card instead of walking on into
// the next question of the creation chain.
var singleFields = map[string]bool{
	"name": true, "age": true, "promoter": true, "poster": true, "description": true, "currency": true,
}

func (d *Draft) singleEdit() bool { return d.Mode == ModeEdit && singleFields[d.Scratch.Edit] }

// next moves to the next question of the chain, or — while one part of an
// edited event is being changed — back to the edit card.
func (d *Draft) next(step string) {
	if d.singleEdit() {
		d.goHome()
		return
	}
	d.goTo(step)
}

// nextSection is next for the steps that end a section.
func (d *Draft) nextSection(step string) {
	if d.singleEdit() {
		d.goHome()
		return
	}
	d.section(step)
}

// goHome shows the summary of a new event, or the edit card of an edited
// one (whose list of changes Apply works out again after every answer).
func (d *Draft) goHome() {
	if d.Mode != ModeEdit {
		d.goTo(stSummary)
		return
	}
	d.Scratch.Edit = ""
	d.Scratch.ReturnToSummary = false
	d.Scratch.PrevCats, d.Scratch.PrevSchedule, d.Scratch.PrevSessions = nil, nil, 0
	d.Scratch.Work, d.Scratch.WorkFor, d.Scratch.WorkAll = nil, 0, false
	d.History = nil
	d.Step = stEditMenu
}

// startEdit opens one part of an edited event from the edit card.
func (d *Draft) startEdit(field, step string) {
	d.Scratch = DraftScratch{Edit: field}
	d.History = nil
	d.Step = step
}

// cancelEdit leaves the part being edited without applying it: categories
// being re-entered are dropped (they live in Scratch.Work until the last
// question, so the date keeps the ones it had), a half-entered date is
// dropped.
func (d *Draft) cancelEdit() {
	switch d.Scratch.Edit {
	case "dates":
		for n := len(d.Sessions); n > d.Scratch.PrevSessions; n-- {
			last := d.Sessions[n-1]
			if last.VenueID != "" && last.Capacity > 0 && last.Tickets != nil {
				break // a finished date (place and categories chosen) stays
			}
			d.Sessions = d.Sessions[:n-1]
		}
		d.Cur = len(d.Sessions) - 1
		if d.Cur < 0 {
			d.Cur = 0
		}
	}
	d.Scratch = DraftScratch{}
	d.History = nil
	d.Step = stEditMenu
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
	d.clearDateDialog()
	return true
}

// defaultTime is the start time offered as "keep …": the time of the date
// before this one (a second date of the same event, or one added to an event
// that already has dates, almost always starts at the same hour), 20:00 when
// there is nothing to copy.
func (d *Draft) defaultTime() string {
	if d.Cur > 0 && d.Cur <= len(d.Sessions) {
		if prev := d.Sessions[d.Cur-1].Time; prev != "" {
			return prev
		}
	}
	return "20:00"
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
// apply is the state machine proper; Apply (wizard_ack.go) wraps it with
// the per-step confirmation line.
func (w *Wizard) apply(ctx context.Context, ws WizSession, d *Draft, in WizInput) (note string, err error) {
	loc := ws.Locale
	t := func(key string, data map[string]any) string { return w.texts.T(loc, key, data) }
	text := strings.TrimSpace(in.Text)
	data := strings.TrimSpace(in.Data)

	// "From the poster": the button stands for the value the person would
	// have typed (or the choice they would have pressed).
	if data == hintButtonData {
		_, hintText, hintData := d.hintValue()
		if hintText == "" && hintData == "" {
			return "", nil
		}
		text, data = hintText, hintData
		// A date read off the poster is shown on the button in full, so pressing
		// it is the confirmation; it is still held to the allowed range.
		if isDateStep(d.Step) {
			if iso, ok := ParseDate(text); ok {
				text, data = "", pickCallback+iso
			}
		}
	}

	switch data {
	case "cancel":
		// One tap must never wipe an hour of answers: ask first, and keep the
		// draft unless the person says otherwise.
		if d.Step != stCancel {
			d.Scratch.CancelFrom = d.Step
			d.Step = stCancel
		}
		return "", nil
	case "cancel:no", "cancel:keep":
		if d.Step == stCancel {
			d.Step = d.Scratch.CancelFrom
			d.Scratch.CancelFrom = ""
			if data == "cancel:keep" {
				return "", ErrSavedExit
			}
		}
		return "", nil
	case "cancel:drop":
		// Only the confirmation screen's own button deletes: a button of an
		// older message (the draft has moved on since) redraws the current
		// question and deletes nothing.
		if d.Step != stCancel {
			return "", nil
		}
		return "", ErrCancelled
	case "e:home":
		if d.Mode == ModeEdit && d.Step != stEditMenu {
			d.cancelEdit()
		}
		return "", nil
	case "back":
		if d.Step == stSummary {
			d.goTo(stXPublish)
			return "", nil
		}
		if d.Step == stEditMenu || d.Step == stCancel {
			return "", nil
		}
		if d.Step == stPreview {
			d.Step = stEditMenu
			return "", nil
		}
		d.back()
		return "", nil
	}

	// The date questions share one way of answering: a calendar of buttons, or
	// a typed date that is confirmed before it counts (wizard_dates.go).
	if isDateStep(d.Step) {
		dateText, handled, dateNote := w.dateInput(loc, d, text, data)
		if handled {
			return dateNote, nil
		}
		text = dateText
		if strings.HasPrefix(data, pickCallback) || strings.HasPrefix(data, confirmPrefix) {
			data = ""
		}
	}

	switch d.Step {
	case stEvName:
		if data == "poster" && w.posterHints && d.Mode == ModeCreate {
			d.goTo(stEvPosterAsk)
			return "", nil
		}
		if in.Poster != nil {
			// The poster came first: keep it, stay on the name question —
			// its hints now sit on the questions that follow.
			d.Event.PosterMediaID, d.Event.PosterW, d.Event.PosterH = in.Poster.MediaID, in.Poster.W, in.Poster.H
			return t("bot.wz.poster_ok", map[string]any{"W": in.Poster.W, "H": in.Poster.H}), nil
		}
		if text == "" || len([]rune(text)) > 200 {
			return t("bot.wz.err_name", nil), nil
		}
		d.Event.Name = text
		d.next(stEvAge)

	case stEvPosterAsk:
		if in.Poster == nil {
			return t("bot.wz.err_poster_first", nil), nil
		}
		// The poster is in: back to the name question, whose hints now read
		// the poster's answer.
		d.Event.PosterMediaID, d.Event.PosterW, d.Event.PosterH = in.Poster.MediaID, in.Poster.W, in.Poster.H
		d.back()
		return t("bot.wz.poster_ok", map[string]any{"W": in.Poster.W, "H": in.Poster.H}), nil

	case stEvAge:
		age := strings.TrimPrefix(data, "age:")
		if data == "keep" {
			age = ws.Defaults.Age
		}
		if !contains(AgeOptions, age) {
			return "", nil
		}
		d.Event.Age = age
		d.next(stEvPromoter)

	case stEvPromoter:
		switch {
		case data == "prom:org":
			d.Event.PromoterID, d.Event.PromoterName = "", ""
			d.next(stEvPoster)
		case data == "prom:new":
			d.goTo(stPromoterName)
		case data == "keep":
			// The remembered promoter belongs to the Telegram account, not to
			// the organization: it is kept only when this organization has it.
			promoters, err := w.refs.Promoters(ctx, ws.JWT, ws.OrgID)
			if err != nil {
				return "", err
			}
			for _, p := range promoters {
				if p.ID == ws.Defaults.PromoterID {
					d.Event.PromoterID, d.Event.PromoterName = p.ID, p.Name
					d.next(stEvPoster)
					return "", nil
				}
			}
		case strings.HasPrefix(data, "prom:"):
			id := strings.TrimPrefix(data, "prom:")
			promoters, err := w.refs.Promoters(ctx, ws.JWT, ws.OrgID)
			if err != nil {
				return "", err
			}
			for _, p := range promoters {
				if p.ID == id {
					d.Event.PromoterID, d.Event.PromoterName = p.ID, p.Name
					d.next(stEvPoster)
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
		d.next(stEvPoster)

	case stEvPoster:
		switch {
		case in.Poster != nil:
			d.Event.PosterMediaID, d.Event.PosterW, d.Event.PosterH = in.Poster.MediaID, in.Poster.W, in.Poster.H
			note = t("bot.wz.poster_ok", map[string]any{"W": in.Poster.W, "H": in.Poster.H})
			d.nextSection(stSDate)
		case data == "keep" && d.Event.PosterMediaID != "":
			d.nextSection(stSDate)
		case data == "skip":
			d.nextSection(stSDate)
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
		hhmm := d.defaultTime()
		if data != "default" {
			v, ok := ParseTime(text)
			if !ok {
				return t("bot.wz.err_time", nil), nil
			}
			hhmm = v
		}
		d.session().Time = hhmm
		d.goTo(stSSalesEnd)

	case stSSalesEnd:
		s := d.session()
		if n, ok := parseOffsetData("se:", data); ok {
			s.SalesEndMin = n
			d.goTo(stSDoors)
			break
		}
		if data == "other" {
			d.goTo(stSSalesEndTime)
			break
		}
		if text != "" {
			off, ok := salesEndOffset(s.Time, text)
			if !ok {
				return t("bot.wz.err_sales_end", nil), nil
			}
			s.SalesEndMin = off
			d.goTo(stSDoors)
		}

	case stSSalesEndTime:
		s := d.session()
		off, ok := salesEndOffset(s.Time, text)
		if !ok {
			return t("bot.wz.err_sales_end", nil), nil
		}
		s.SalesEndMin = off
		d.goTo(stSDoors)

	case stSDoors:
		s := d.session()
		switch n, ok := parseOffsetData("dr:", data); {
		case ok:
			s.DoorsMin = n
		case data == "none":
			s.DoorsMin = 0
		case text != "":
			before, ok := doorsOffset(s.Time, text)
			if !ok {
				return t("bot.wz.err_doors", nil), nil
			}
			s.DoorsMin = before
		default:
			return "", nil
		}
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
			d.goTo(d.afterSessionStep())
		case "other":
			d.goTo(stSCountry)
		}

	case stSCountry:
		var chosen *RefItem
		countries, err := w.refs.Countries(ctx, ws.JWT, loc)
		if err != nil {
			return "", err
		}
		if rg, ok := strings.CutPrefix(data, "region:"); ok {
			// A region button opens that region's countries; an empty one is
			// the way back to the region list.
			d.Scratch.CountryRegion = ""
			if validRegion(countries, rg) {
				d.Scratch.CountryRegion = rg
			}
			return "", nil
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
		d.Scratch.CountryRegion = ""
		d.goTo(stSCity)

	case stSCity:
		s := d.session()
		if data == "city:new" {
			d.goTo(stCityName)
			return "", nil
		}
		if idx, ok := strings.CutPrefix(data, "city:g:"); ok {
			// One of the country's biggest cities, offered from the embedded
			// data: it is found or added by name like a typed one.
			n, err := strconv.Atoi(idx)
			top := geotz.TopCities(s.CountryISO2)
			if err != nil || n < 0 || n >= len(top) {
				return "", nil
			}
			c, err := w.refs.CreateCity(ctx, ws.JWT, ws.OrgID, s.CountryID, top[n], loc)
			if err != nil {
				return "", err
			}
			s.CityID, s.CityName = c.ID, c.Name
			s.VenueID, s.VenueName, s.Timezone = "", "", ""
			d.goTo(stSVenue)
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
		// A city typed straight onto the question: the list holds only the
		// cities somebody already used, so most organizers' city is not in it.
		// A name that matches a listed one picks it; any other is added (the
		// API finds an existing city of that name before it creates one).
		if name := strings.TrimSpace(text); name != "" && data == "" {
			want := geotz.Normalize(name)
			for _, c := range cities {
				if geotz.Normalize(c.Name) == want {
					s.CityID, s.CityName = c.ID, c.Name
					s.VenueID, s.VenueName, s.Timezone = "", "", ""
					d.goTo(stSVenue)
					return "", nil
				}
			}
			c, err := w.refs.CreateCity(ctx, ws.JWT, ws.OrgID, s.CountryID, name, loc)
			if err != nil {
				return "", err
			}
			s.CityID, s.CityName = c.ID, c.Name
			s.VenueID, s.VenueName, s.Timezone = "", "", ""
			d.goTo(stSVenue)
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
		d.Scratch.DupVenueID, d.Scratch.DupVenueName = "", ""
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
				if s.seated() {
					// Back to the venue question and a venue chosen again: the
					// plan of the first one does not belong to this one.
					s.PlanVersionID, s.PlanName, s.Tickets = "", "", nil
				}
				if w.offerPlans(ctx, ws, d) {
					return "", nil
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
		// One question about places, not two: the number is this session's
		// capacity and is remembered as the new venue's usual size.
		n, ok := ParseCount(text)
		if !ok {
			return t("bot.wz.err_capacity", nil), nil
		}
		d.Scratch.NewVenue.Capacity = n
		d.session().Capacity = n
		if tz := w.guessTimezone(ctx, ws, d.Scratch.NewVenue.CountryISO2); tz != "" {
			d.Scratch.NewVenue.Timezone = tz
			return w.createVenue(ctx, ws, d)
		}
		d.goTo(stVTz)

	case stVTz:
		// A pressed button carries a zone the bot itself proposed; typed text
		// is an IANA name or a city name, which is only ever turned into a
		// button to confirm (a wrong zone would shift every time a buyer
		// reads).
		zone := strings.TrimPrefix(data, "tz:")
		if !strings.HasPrefix(data, "tz:") {
			zone = strings.TrimSpace(text)
			if _, err := time.LoadLocation(zone); err != nil || zone == "" {
				if hint := geotz.ForCity(d.Scratch.NewVenue.CountryISO2, text); hint != "" {
					d.Scratch.TzHint = hint
					return t("bot.wz.tz_suggest_note", map[string]any{"City": Esc(strings.TrimSpace(text)), "Zone": hint}), nil
				}
				return t("bot.wz.err_venue_tz", nil), nil
			}
		}
		if _, err := time.LoadLocation(zone); err != nil || zone == "" {
			return t("bot.wz.err_venue_tz", nil), nil
		}
		d.Scratch.NewVenue.Timezone = zone
		d.Scratch.TzHint = ""
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
		return w.finishSession(loc, d)

	case stSMore:
		switch data {
		case "more":
			d.Cur = len(d.Sessions)
			d.goTo(stSDate)
		case "next":
			if d.Mode != ModeEdit && d.allDatesOwnTickets() {
				// Every date is sold by a seating plan and was priced with it:
				// there is nothing left for the shared ticket questions.
				d.Tickets = cloneTickets(*d.Sessions[0].Tickets)
				d.section(stXDescription)
				break
			}
			d.section(stTMode)
		}

	case stSPlan, stSPlanPrice:
		return w.applySeated(loc, d, text, data)

	case stTMode:
		switch data {
		case "mode:single":
			*d.tk() = DraftTickets{Mode: ModeSingle}
			d.goTo(stTName)
		case "mode:multi":
			*d.tk() = DraftTickets{}
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
		d.tk().Categories = []DraftCategory{cat}
		d.goTo(stTPrice)

	case stTPrice:
		p, ok := ParsePriceMinor(text)
		if !ok {
			return t("bot.wz.err_price", nil), nil
		}
		d.tk().Categories[0].PriceMinor = p
		d.goTo(stTChanges)

	case stTChanges:
		switch data {
		case "no":
			d.tk().Schedule = nil
			d.ticketsDone()
		case "yes":
			d.goTo(stTChangeDate)
		}

	case stTChangeDate:
		iso, ok := ParseDate(text)
		if !ok {
			return t("bot.wz.err_date", nil), nil
		}
		if sched := d.tk().Schedule; len(sched) > 0 && iso <= sched[len(sched)-1].From {
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
		d.tk().Schedule = append(d.tk().Schedule, d.Scratch.Step)
		d.Scratch.Step = DraftPriceStep{}
		d.goTo(stTChangeMore)

	case stTChangeMore:
		switch data {
		case "more":
			d.goTo(stTChangeDate)
		case "done":
			d.ticketsDone()
		}

	case stTKind:
		switch data {
		case "kind:parallel":
			d.tk().Mode = ModeParallel
		case "kind:sequence":
			d.tk().Mode = ModeSequence
		default:
			return "", nil
		}
		d.tk().Categories = nil
		d.Scratch.Cat = DraftCategory{}
		d.goTo(stTCatName)

	case stTCatName:
		if text == "" {
			return t("bot.wz.err_name", nil), nil
		}
		for _, c := range d.tk().Categories {
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
		switch {
		case d.tk().Mode == ModeParallel:
			d.goTo(stTCatPlaces)
		case len(d.tk().Categories) == 0:
			d.goTo(stTCatUntil) // the first one cannot be the last
		default:
			d.goTo(stTCatLast)
		}

	case stTCatLast:
		switch data {
		case "last":
			// The last category sells until the session starts: it has no end.
			d.Scratch.Cat.SellUntil, d.Scratch.Cat.SellLimit = "", 0
			d.carryTierID(&d.Scratch.Cat)
			d.tk().Categories = append(d.tk().Categories, d.Scratch.Cat)
			d.Scratch.Cat = DraftCategory{}
			d.ticketsDone()
		case "next":
			d.goTo(stTCatUntil)
		}

	case stTCatPlaces:
		n, ok := ParseCount(text)
		if !ok {
			return t("bot.wz.err_places", nil), nil
		}
		d.Scratch.Cat.Places = n
		d.carryTierID(&d.Scratch.Cat)
		d.tk().Categories = append(d.tk().Categories, d.Scratch.Cat)
		d.Scratch.Cat = DraftCategory{}
		d.goTo(stTCatMore)

	case stTCatUntil:
		until, limit, ok := ParseCategoryEnd(text)
		if !ok {
			return t("bot.wz.err_cat_end", nil), nil
		}
		if prev := lastUntil(d.tk().Categories); until != "" && prev != "" && until < prev {
			return t("bot.wz.err_until_order", map[string]any{"Name": Esc(d.Scratch.Cat.Name)}), nil
		}
		d.Scratch.Cat.SellUntil, d.Scratch.Cat.SellLimit = until, limit
		d.carryTierID(&d.Scratch.Cat)
		d.tk().Categories = append(d.tk().Categories, d.Scratch.Cat)
		d.Scratch.Cat = DraftCategory{}
		d.goTo(stTCatName) // it ends, so another follows

	case stTCatMore:
		switch data {
		case "more":
			d.goTo(stTCatName)
		case "done":
			tk := d.tk()
			if len(tk.Categories) < 2 {
				return t("bot.wz.err_min_two", nil), nil
			}
			if tk.Mode == ModeSequence {
				// The last category sells until the session starts: its own
				// end, if one was typed, is dropped (spec: the last one has none).
				last := &tk.Categories[len(tk.Categories)-1]
				last.SellUntil, last.SellLimit = "", 0
			}
			d.ticketsDone()
		}

	case stXDescription:
		switch {
		case in.Art != "":
			d.setApprovedDescription(in.Art)
		case data == "clear":
			d.Event.Description, d.Event.DescriptionFormat = "", ""
		case data != "skip":
			d.setTypedDescription(text)
		}
		d.next(stXCurrency)

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
			d.next(stXPublish)
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

	case stEditMenu:
		if key, ok := strings.CutPrefix(data, "e:undo:"); ok {
			// One change is taken back: the part returns to what was loaded.
			what := w.undoLabel(loc, d, key)
			if d.undo(key) {
				return t("bot.wz.edit_undone", map[string]any{"What": what}), nil
			}
			return "", nil
		}
		switch data {
		case "publish":
			// "Publish changes" first shows what will go to the site; only
			// the preview's own button saves (wizard_bot.go).
			if len(d.changes()) > 0 || d.unsent() {
				d.History = nil
				d.Step = stPreview
			}
		case "e:name":
			d.startEdit("name", stEvName)
		case "e:desc":
			d.startEdit("description", stXDescription)
		case "e:poster":
			d.startEdit("poster", stEvPoster)
		case "e:age":
			d.startEdit("age", stEvAge)
		case "e:promoter":
			d.startEdit("promoter", stEvPromoter)
		case "e:currency":
			d.startEdit("currency", stXCurrency)
		case "e:date":
			// Existing dates stay as they are; the bot only adds new ones.
			d.startEdit("dates", stSDate)
			d.Scratch.ReturnToSummary = true
			d.Scratch.PrevSessions = len(d.Sessions)
			d.Cur = len(d.Sessions)
		case "e:tickets":
			d.openTicketsEdit()
		}

	case stTDate:
		// Which date's categories change: one date, or every date at once.
		if data == "td:all" {
			if d.anySeated() {
				break // a date sold by a plan is re-priced on its own
			}
			d.goTo(d.startTickets(0, *d.ticketsOf(0), true))
			break
		}
		if raw, ok := strings.CutPrefix(data, "td:"); ok {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > len(d.Sessions) {
				return "", nil
			}
			if d.Sessions[n-1].seated() {
				d.startSeatPrices(n)
				break
			}
			d.goTo(d.startTickets(n, *d.ticketsOf(n - 1), true))
		}

	case stSTickets:
		// A date added to an edited event: a copy of the categories of the
		// date before it (as new categories of this date), or its own.
		if d.Cur < 1 || d.Cur >= len(d.Sessions) {
			return "", nil
		}
		prev := d.ticketsOf(d.Cur - 1)
		switch data {
		case "st:same":
			if d.Sessions[d.Cur-1].seated() {
				return "", nil // the categories of a plan belong to its plan
			}
			// The copy keeps nothing that would end after the new date: a
			// price change or a stage end later than it is dropped or moved
			// onto the date, as the "own categories" questions would demand.
			copied := withoutTierIDs(*prev)
			if n := fitTicketsToDate(&copied, d.Sessions[d.Cur].Date); n > 0 {
				note = t("bot.wz.s_tickets_trimmed", map[string]any{"Count": n, "Date": DisplayDate(d.Sessions[d.Cur].Date)})
			}
			d.setDateTickets(d.Cur, copied)
			d.Sessions[d.Cur].CopiedFrom = dateLabel(d.Sessions[d.Cur-1])
			d.goTo(stSConfirm)
		case "st:own":
			d.goTo(d.startTickets(d.Cur+1, *prev, false))
		}

	case stSConfirm:
		// The new date, summed up: its own categories, other places, away
		// with it, or on to "one more date?".
		if d.Cur < 0 || d.Cur >= len(d.Sessions) {
			return "", nil
		}
		s := &d.Sessions[d.Cur]
		switch data {
		case "sc:next":
			d.goTo(stSMore)
		case "sc:own":
			if s.seated() {
				d.startSeatPrices(d.Cur + 1) // a date sold by a plan changes its prices only
				break
			}
			d.goTo(d.startTickets(d.Cur+1, *d.ticketsOf(d.Cur), false))
		case "sc:cap":
			if d.ticketsOf(d.Cur).Mode != ModeParallel {
				d.goTo(stSCapacity)
			}
		case "sc:drop":
			if !d.canRemoveDate(d.Cur) {
				return "", nil
			}
			when := dateLabel(*s)
			d.removeDate(d.Cur)
			d.History = nil
			d.clearDateDialog()
			d.Step = stSMore
			return t("bot.wz.s_removed", map[string]any{"When": when}), nil
		}

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
				d.openTicketsEdit()
				break
			}
			if d.anySeated() {
				d.Scratch.Edit = "tickets"
				d.Step = stTDate
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
		if IsAPIError(err, 409) && APIErrorCode(err) == "venue.duplicate" {
			d.History = nil
			d.Step = stSVenue
			name := d.Scratch.NewVenue.Name
			d.Scratch.NewVenue = VenueCreate{}
			if venues, lerr := w.refs.Venues(ctx, ws.JWT, ws.OrgID); lerr == nil {
				for _, v := range venues {
					if strings.EqualFold(strings.TrimSpace(v.Name), strings.TrimSpace(name)) {
						d.Scratch.DupVenueID, d.Scratch.DupVenueName = v.ID, v.Name
						break
					}
				}
			}
			return w.texts.T(ws.Locale, "bot.wz.venue_duplicate", map[string]any{"Name": Esc(name)}), nil
		}
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
	return w.finishSession(ws.Locale, d)
}

// finishSession closes the date being entered: a repeat of an existing date
// sends the person back to the date question, anything else is confirmed and
// "one more date?" follows.
func (w *Wizard) finishSession(loc string, d *Draft) (string, error) {
	if d.duplicateSession() {
		d.History = nil
		d.Step = stSDate
		return w.texts.T(loc, "bot.wz.err_duplicate_session", nil), nil
	}
	note := w.sessionAddedNote(loc, d)
	d.goTo(d.afterSessionStep())
	return note, nil
}

// guessTimezone picks the zone of a new venue: the zone of another venue
// of the organization in the same country, else a built-in default for
// single-zone countries, else "" (the wizard asks).
func (w *Wizard) guessTimezone(ctx context.Context, ws WizSession, iso2 string) string {
	// A country with several zones is never guessed from another venue: the
	// organization's New York venue says nothing about its next one in Los
	// Angeles. The zone question asks, with the city's own zone as a button.
	if len(geotz.ZonesFor(iso2)) > 1 {
		return ""
	}
	if venues, err := w.refs.Venues(ctx, ws.JWT, ws.OrgID); err == nil {
		for _, v := range venues {
			if strings.EqualFold(v.CountryISO2, iso2) && v.Timezone != "" {
				return v.Timezone
			}
		}
	}
	// The single-zone countries arena sells in live in geotz (shared with the
	// Bil24 import and migration 0118's backfill); every other country is
	// asked explicitly.
	return geotz.ForCountry(iso2)
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
// hasRef reports whether the list holds an item with this id.
func hasRef(list []RefItem, id string) bool {
	for _, r := range list {
		if r.ID == id {
			return true
		}
	}
	return false
}

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
