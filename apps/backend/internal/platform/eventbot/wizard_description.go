package eventbot

// wizard_description.go — the description of an event in the draft keeps the
// format it is stored in (spec 44, RT-02). The description of a loaded event is
// ART ("art"), or the old plain text with marks ("text", not converted yet),
// or unknown ("", an old draft). What the organizer types in the bot is plain
// text ("text"). The screens never show ART tags, and a description the
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

// setTypedDescription stores what the organizer typed: plain text, which the
// server reads as the old text format. The 4000-character limit of the
// description counts visible text, and a typed text has no markup, so its
// characters are its visible characters.
func (d *Draft) setTypedDescription(text string) {
	// The server refuses the bidi embedding marks U+202A to U+202E (a Hebrew
	// organizer pastes them without seeing them), so they are dropped here.
	text = strings.Map(func(r rune) rune {
		if r >= 0x202a && r <= 0x202e {
			return -1
		}
		return r
	}, text)
	if r := []rune(text); len(r) > 4000 {
		text = string(r[:4000])
	}
	d.Event.Description = text
	d.Event.DescriptionFormat = richtext.FormatText
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
//   - art that the organizer did change (only a typed text changes it, so this
//     is a new draft with ART in it): sent as it is, format "art".
//   - what the organizer typed: plain text, format "text" (the default, so no
//     format is named).
func (d *Draft) bundleDescription() (text, format string) {
	switch {
	case strings.TrimSpace(d.Event.Description) == "":
		return "", ""
	case d.Mode == ModeEdit && d.Loaded != nil && !d.descriptionChanged():
		return "", "" // untouched: never sent, whatever its format
	case d.Event.DescriptionFormat == richtext.FormatART:
		return d.Event.Description, richtext.FormatART
	}
	// Typed text: "text" is the wire default of source=arena, so it is left
	// implicit (and the bundles of the shared wizard golden files stay as
	// they are).
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
