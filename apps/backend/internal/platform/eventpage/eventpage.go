// Package eventpage reads an organizer's OWN event page and answers what it
// says about the event: name, dates, venue, description, ticket categories
// and a picture (spec 08_architecture/59).
//
// The sources are taken in order of trust: a schema.org Event in JSON-LD,
// then Open Graph and the plain meta tags, and only when those leave the
// dates or the prices missing, a text model reading the visible text (the
// caller decides whether to spend that call). Every value is a SUGGESTION:
// the wizards offer it as a button and nothing reaches an event without the
// organizer's press.
//
// The page is fetched only through internal/platform/egress (the one guard
// for outbound requests whose address comes from a user) and pages of known
// ticketing platforms are refused before their body is read: their terms
// forbid automated collection.
package eventpage

// Facts is what a page says about an event. The shape extends
// posterread.Facts: Date and Time repeat the first entry of Dates, so a
// client that understands a poster reading understands a page reading too.
type Facts struct {
	Name        string     `json:"name"`
	Date        string     `json:"date"` // YYYY-MM-DD of the first date
	Time        string     `json:"time"` // HH:MM of the first date
	VenueName   string     `json:"venue_name"`
	City        string     `json:"city"`
	Country     string     `json:"country"` // ISO 3166-1 alpha-2, "" when the page names none or not by code
	Street      string     `json:"street"`
	Age         string     `json:"age"` // "0+", "6+", "12+", "16+", "18+"
	Description string     `json:"description"`
	Dates       []DateFact `json:"dates"`
	Categories  []Category `json:"categories"`
	Image       *Image     `json:"image"`
}

// DateFact is one date of the event as the page writes it: the wall clock
// date and time, and the UTC offset when the page gave one.
type DateFact struct {
	Date      string `json:"date"`       // YYYY-MM-DD
	Time      string `json:"time"`       // HH:MM, "" when the page gives only the day
	Doors     string `json:"doors"`      // HH:MM of the same day, "" when absent
	UTCOffset string `json:"utc_offset"` // "+01:00", "Z" for UTC, "" when the page wrote none
}

// Category is one ticket price the page lists.
type Category struct {
	Name       string `json:"name"`
	PriceMinor int64  `json:"price_minor"`
	Currency   string `json:"currency"` // ISO 4217, "" when the page names none
}

// Image is the page's picture, stored as a media object of the organization
// and attached to nothing until the organizer chooses it as the poster.
type Image struct {
	MediaID string `json:"media_id"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	// FitsPoster says the picture passes the poster rules of the Telegram
	// bot (4:5 or square, at least 1000 px wide).
	FitsPoster bool `json:"fits_poster"`
}

// ImageCandidate is one picture address of the page and where it was named.
type ImageCandidate struct {
	URL    string
	Source Source
}

// Source names where a value came from.
type Source string

const (
	SourceJSONLD    Source = "json_ld"
	SourceOpenGraph Source = "open_graph"
	SourceModel     Source = "model"
)

// Warning is a fact about the reading the organizer should know, with a
// stable code the clients translate.
type Warning struct {
	Code  string `json:"code"`
	Count int    `json:"count,omitempty"`
	// Values are short page values the text needs (an other date, a price
	// range); never a URL or an address.
	Values []string `json:"values,omitempty"`
}

// Warning codes.
const (
	WarnDatesSkipped     = "event_page.some_dates_skipped"      // more than MaxDates future dates
	WarnPastDatesSkipped = "event_page.past_dates_skipped"      // dates already over
	WarnCancelledSkipped = "event_page.cancelled_dates_skipped" // EventCancelled / EventPostponed
	WarnOnline           = "event_page.online_event"            // VirtualLocation
	WarnPriceRangeOnly   = "event_page.price_range_only"        // AggregateOffer with only low/high
	WarnReadByModel      = "event_page.read_by_model"           // a model filled some values: check them
	WarnModelUnavailable = "event_page.model_unavailable"       // the model would have helped but is off or failed
	WarnModelQuota       = "event_page.model_quota"             // the organization used its model readings for today
	WarnImageNotPoster   = "event_page.image_not_poster"        // the picture does not fit the poster rules
	WarnImageUnavailable = "event_page.image_unavailable"       // the picture could not be downloaded
	WarnNoDates          = "event_page.no_dates"                // the page has no future date
	WarnNoPrices         = "event_page.no_prices"               // the page lists no price
	WarnJavaScriptPage   = "event_page.javascript_page"         // almost no text: the page is drawn by a script
)

// Limits of one reading.
const (
	// MaxDates is how many future dates a reading offers.
	MaxDates = 10
	// MaxCategories is how many ticket categories a reading offers.
	MaxCategories = 10
	// MaxDescriptionRunes is the visible limit of an event description.
	MaxDescriptionRunes = 4000
	// MaxNameRunes is the limit of an event name in the wizards.
	MaxNameRunes = 200
)

// Result is one reading of a page.
type Result struct {
	Facts    Facts             `json:"facts"`
	Sources  map[string]Source `json:"sources"`
	Language string            `json:"language"`
	Warnings []Warning         `json:"warnings"`

	// ImageURLs are the page's picture candidates, best first (JSON-LD,
	// then og:image), absolute. Not part of the answer.
	ImageURLs []ImageCandidate `json:"-"`
	// Text is the page's visible text for the model, already cut.
	Text string `json:"-"`
}

// HasDates reports whether the reading found at least one future date.
func (r Result) HasDates() bool { return len(r.Facts.Dates) > 0 }

// HasPrices reports whether the reading found at least one price.
func (r Result) HasPrices() bool { return len(r.Facts.Categories) > 0 }

// Empty reports whether nothing usable was found: no name and no date.
func (r Result) Empty() bool { return r.Facts.Name == "" && len(r.Facts.Dates) == 0 }

// NeedsModel reports whether the text model should read the page (owner
// decision 03.11: only when the structured sources leave no date or no
// price) and whether there is text for it to read.
func (r Result) NeedsModel() bool {
	return (!r.HasDates() || !r.HasPrices()) && len([]rune(r.Text)) >= minModelTextRunes
}

// minModelTextRunes is the least visible text worth a model call.
const minModelTextRunes = 40

func (r *Result) warn(w Warning) {
	for _, have := range r.Warnings {
		if have.Code == w.Code {
			return
		}
	}
	r.Warnings = append(r.Warnings, w)
}

func (r *Result) setSource(field string, s Source) {
	if r.Sources == nil {
		r.Sources = map[string]Source{}
	}
	r.Sources[field] = s
}
