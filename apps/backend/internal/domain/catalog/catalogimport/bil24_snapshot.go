// Package catalogimport defines the data shapes used by the arena-bil24-import
// one-shot operator tool (feature #386).
//
// This package contains ONLY pure Go types and validation logic. It has no
// dependency on the database, HTTP server, or any other runtime component.
// It is intentionally kept thin so both the importer binary and its tests can
// share the same type definitions without pulling in unrelated dependencies.
//
// The arena-bil24-import binary (cmd/arena-bil24-import) is the only consumer
// of this package. It must NOT be imported by the api-server, worker, or any
// other long-running binary — see the BUILD ISOLATION note in the command's
// package comment.
package catalogimport

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// DefaultEventDuration is the fallback duration added to StartsAt when a
// Bil24 snapshot row does not supply an explicit EndsAt value.
const DefaultEventDuration = 3 * time.Hour

// Bil24SnapshotEvent represents a single event row read from the Bil24
// export source (JSON file or API response). All fields map directly to the
// arena_new events table columns that the importer will populate.
//
// Required fields: ExternalBil24ID, Title, StartsAt.
// Optional fields: EndsAt (defaults to StartsAt + 3h), VenueName,
// Description, PosterURL, PriceTiers.
type Bil24SnapshotEvent struct {
	// ExternalBil24ID is the source Bil24 event identifier. Must be unique
	// within the snapshot; used as the idempotency key (ON CONFLICT).
	ExternalBil24ID string `json:"external_bil24_id"`

	// Title is the event display name mapped to events.name.
	Title string `json:"title"`

	// StartsAt is the event start time (RFC 3339 / time.Time JSON).
	StartsAt time.Time `json:"starts_at"`

	// EndsAt is the optional end time. If nil or zero, the importer will
	// default to StartsAt + DefaultEventDuration.
	EndsAt *time.Time `json:"ends_at,omitempty"`

	// VenueName is the human-readable venue name from Bil24. It is
	// appended to the description as a note since arena_new venue_id
	// resolution is not performed during the snapshot import.
	VenueName string `json:"venue_name,omitempty"`

	// Description is the long-form event description. If VenueName is
	// also set, it is appended to the description in parentheses.
	Description string `json:"description,omitempty"`

	// PosterURL is the public URL for the event poster image, stored in
	// events.image_url.
	PosterURL string `json:"poster_url,omitempty"`

	// PriceTiers holds optional pricing information from Bil24. The importer
	// logs them in the summary but does NOT create ticket_tier rows in this
	// release — arena_new pricing is managed through the native catalog API.
	PriceTiers []PriceTier `json:"price_tiers,omitempty"`

	// DescriptionFormat says how Description is written: "art" (HTML, the
	// default, because Bil24 descriptions are HTML) or "text" (the old plain
	// text with marks). It is set by the importer's --description-format
	// flag, not read from the snapshot.
	DescriptionFormat string `json:"-"`
}

// PriceTier is an optional price tier attached to a Bil24 snapshot event.
// It is surfaced in the importer's dry-run output and summary log for
// operator reference.
type PriceTier struct {
	// Name is the tier label (e.g. "Стандарт", "VIP").
	Name string `json:"name"`

	// PriceKopeks is the price in the smallest currency unit (kopeks / cents).
	PriceKopeks int64 `json:"price_kopeks"`
}

// Validate checks that the snapshot event has all required fields populated
// and that the time constraints are coherent. Returns a descriptive error
// if the row should be rejected; nil if the row is safe to import.
//
// Batch semantics: a validation failure on one row MUST NOT abort the rest
// of the batch — the caller is responsible for collecting errors per-row and
// continuing.
func (e *Bil24SnapshotEvent) Validate() error {
	var errs []string

	if strings.TrimSpace(e.ExternalBil24ID) == "" {
		errs = append(errs, "external_bil24_id is required")
	}
	if strings.TrimSpace(e.Title) == "" {
		errs = append(errs, "title is required")
	}
	if e.StartsAt.IsZero() {
		errs = append(errs, "starts_at is required")
	}

	// If EndsAt is explicitly provided it must be after StartsAt.
	if e.EndsAt != nil && !e.EndsAt.IsZero() {
		if !e.EndsAt.After(e.StartsAt) {
			errs = append(errs, fmt.Sprintf(
				"ends_at (%s) must be after starts_at (%s)",
				e.EndsAt.Format(time.RFC3339),
				e.StartsAt.Format(time.RFC3339),
			))
		}
	}

	// The description is stored as ART (spec 44, RT-02): a row whose text the
	// filter refuses is reported with the others and not imported.
	if _, err := e.DescriptionART(); err != nil {
		errs = append(errs, "description: "+err.Error())
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// ResolvedEndsAt returns the effective end time: EndsAt if supplied and
// non-zero, otherwise StartsAt + DefaultEventDuration.
func (e *Bil24SnapshotEvent) ResolvedEndsAt() time.Time {
	if e.EndsAt != nil && !e.EndsAt.IsZero() {
		return *e.EndsAt
	}
	return e.StartsAt.Add(DefaultEventDuration)
}

// Description format values of the importer.
const (
	// DescriptionFormatART reads the description as HTML (the default).
	DescriptionFormatART = richtext.FormatART
	// DescriptionFormatText reads it as the old plain text with marks.
	DescriptionFormatText = richtext.FormatText
)

// ParseDescriptionFormat checks the value of --description-format: "text" or
// "art" (an empty value is "art").
func ParseDescriptionFormat(s string) (string, error) {
	switch s {
	case "", DescriptionFormatART:
		return DescriptionFormatART, nil
	case DescriptionFormatText:
		return DescriptionFormatText, nil
	}
	return "", fmt.Errorf("description format %q: must be text or art", s)
}

// DescriptionART is ResolvedDescription passed through richtext.Normalize in
// the event's DescriptionFormat (art unless set to text): the text to store in
// events.description together with the format flag "art". It is the single
// write path of the importer (spec 44, RT-02).
func (e *Bil24SnapshotEvent) DescriptionART() (string, error) {
	format, err := ParseDescriptionFormat(e.DescriptionFormat)
	if err != nil {
		return "", err
	}
	return richtext.Normalize(e.ResolvedDescription(), format, richtext.ProfileDescription)
}

// ResolvedDescription merges Description and VenueName into the single
// text value written to events.description. If VenueName is set it is
// appended as "(Venue: <name>)" so that venue context is preserved in the
// catalog even though no venue_id FK resolution is performed.
func (e *Bil24SnapshotEvent) ResolvedDescription() string {
	desc := strings.TrimSpace(e.Description)
	venue := strings.TrimSpace(e.VenueName)
	if e.DescriptionFormat != DescriptionFormatText {
		// The description is HTML: the venue name is a text of the page, so
		// a "<" in it must not become a tag.
		venue = html.EscapeString(venue)
	}
	if venue == "" {
		return desc
	}
	if desc == "" {
		return fmt.Sprintf("(Venue: %s)", venue)
	}
	return fmt.Sprintf("%s\n\n(Venue: %s)", desc, venue)
}
