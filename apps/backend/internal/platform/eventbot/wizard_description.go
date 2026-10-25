package eventbot

// wizard_description.go — the description of an event in the draft keeps the
// format it is stored in (spec 44, RT-02). The description of a loaded event is
// ART ("art"), or the old plain text with marks ("text", not converted yet),
// or unknown ("", an old draft). What the organizer types in the bot is shown
// back formatted (textfmt_dialog.go, RT-05) and enters the draft as ART; a
// description that comes without that loop (a poster hint) enters through
// richtext.FromLegacy. The screens never show ART tags, and a description the
// organizer did not touch goes back to the server as it was read.

import (
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// previewRunes is how much of the description the edit screen shows.
const previewRunes = 300

// descriptionFormatOf reads the format flag of a loaded event; "" when the
// server did not say.
func descriptionFormatOf(f *openapi.EventItemDescriptionFormat) string {
	if f == nil {
		return ""
	}
	switch string(*f) {
	case richtext.FormatART:
		return richtext.FormatART
	case richtext.FormatText:
		return richtext.FormatText
	}
	return ""
}

// eventDescriptionBase is the CANONICAL description of a loaded event: the text
// stored on the events row. The list and get routes resolve `description` per
// locale, so it may be a translation; `description_base` is the raw one. A
// server that predates the field sends neither it nor `description_translated`,
// and `description` is then the only text there is. A base of null with the
// translated flag set means the event has no canonical description.
func eventDescriptionBase(ev openapi.EventItem) string {
	if ev.DescriptionBase != nil {
		return *ev.DescriptionBase
	}
	if ev.DescriptionTranslated != nil && *ev.DescriptionTranslated {
		return ""
	}
	return deref(ev.Description)
}

// descriptionPreview is the description as an HTML line of a Telegram screen.
// ART is shown formatted (ToTelegramHTML) while its VISIBLE text fits limit
// characters; a longer one is cut by its visible text, as plain text, so a tag
// is never cut in half. The old text is escaped and cut like any typed text.
func descriptionPreview(e DraftEvent, limit int) string {
	if e.DescriptionFormat != richtext.FormatART {
		return Esc(truncate(e.Description, limit))
	}
	if richtext.VisibleLen(e.Description) <= limit {
		return richtext.ToTelegramHTML(e.Description)
	}
	return Esc(truncate(richtext.PlainText(e.Description), limit))
}

// stripBidiEmbedding drops the bidi embedding marks U+202A to U+202E. The
// server refuses them, and a Hebrew organizer pastes them without seeing them.
func stripBidiEmbedding(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= 0x202a && r <= 0x202e {
			return -1
		}
		return r
	}, text)
}

// setTypedDescription stores a description that came as plain text without a
// preview: a hint read off the poster, or an answer given where the model is
// not asked. It enters the draft the way the server would read it
// (richtext.FromLegacy through Normalize), as ART marked "art", so a draft
// never holds the old notation (spec 44, RT-05). The 4000-character limit of
// the description counts VISIBLE text. A text the converter cannot take at
// all stays plain text marked "text", and the server then judges it.
func (d *Draft) setTypedDescription(text string) {
	text = stripBidiEmbedding(text)
	art, err := richtext.Normalize(text, richtext.FormatText, richtext.ProfileDescription)
	if err != nil {
		// Too long: cut as before, then convert again.
		if r := []rune(text); len(r) > richtext.ProfileDescription.MaxVisible {
			text = string(r[:richtext.ProfileDescription.MaxVisible])
		}
		art, err = richtext.Normalize(text, richtext.FormatText, richtext.ProfileDescription)
	}
	if err != nil {
		d.Event.Description = text
		d.Event.DescriptionFormat = richtext.FormatText
		return
	}
	if art == "" {
		d.Event.Description, d.Event.DescriptionFormat = "", ""
		return
	}
	d.Event.Description = art
	d.Event.DescriptionFormat = richtext.FormatART
}

// setApprovedDescription stores a description the organizer approved in the
// preview: ART that already went through the filter.
func (d *Draft) setApprovedDescription(art string) {
	d.Event.Description = art
	d.Event.DescriptionFormat = richtext.FormatART
}

// descriptionChanged reports whether the draft's description differs from the
// loaded event's (text or format).
func (d *Draft) descriptionChanged() bool {
	l := d.Loaded
	return d.Event.Description != l.Event.Description || d.Event.DescriptionFormat != l.Event.DescriptionFormat
}

// bundleDescription is the description and format an event-bundle carries.
//   - an edit that did not touch the description (ANY format): NOT sent, the
//     server keeps what it has. The bot loads the description without a
//     locale, so the server may have shown a translation (translation, en,
//     base): re-sending it would write that translation over the base text.
//     Re-sending an old text under a guessed format could also turn tags into
//     words.
//   - ART (what the organizer approved in the preview, or typed text turned
//     into ART): sent as it is, format "art".
//   - plain text in the old notation (only a draft the converter could not
//     take): format "text", the wire default, so no format is named.
func (d *Draft) bundleDescription() (text, format string) {
	switch {
	case strings.TrimSpace(d.Event.Description) == "":
		return "", ""
	case d.Mode == ModeEdit && d.Loaded != nil && !d.descriptionChanged():
		return "", "" // untouched: never sent, whatever its format
	case d.Event.DescriptionFormat == richtext.FormatART:
		return d.Event.Description, richtext.FormatART
	}
	return d.Event.Description, ""
}

// descriptionRefusalKey is the bot text for a description the server refused
// (422 import.invalid_description), by error.details.reason. A reason this
// version does not know (or none) gets the general text.
func descriptionRefusalKey(reason string) string {
	switch richtext.Reason(reason) {
	case richtext.ReasonTooLong, richtext.ReasonHTMLTooLong:
		return "bot.wz.err_description_too_long"
	case richtext.ReasonControlChars, richtext.ReasonBadEncoding:
		return "bot.wz.err_description_chars"
	}
	return "bot.wz.err_description"
}
