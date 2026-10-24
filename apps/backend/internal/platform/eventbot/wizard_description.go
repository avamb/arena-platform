package eventbot

// wizard_description.go — the description of an event in the draft keeps the
// format it is stored in (spec 43, RT-02). The description of a loaded event is
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
//   - art: sent back as it is, with format "art", so it comes back byte for byte.
//   - an edit that did not touch the description (a stored text of the old
//     kind or of an unknown kind): NOT sent, the server keeps what it has.
//     Re-sending it under a guessed format could turn tags into words.
//   - what the organizer typed: plain text, format "text" (the default, so no
//     format is named).
func (d *Draft) bundleDescription() (text, format string) {
	switch {
	case strings.TrimSpace(d.Event.Description) == "":
		return "", ""
	case d.Event.DescriptionFormat == richtext.FormatART:
		return d.Event.Description, richtext.FormatART
	case d.Mode == ModeEdit && d.Loaded != nil && !d.descriptionChanged():
		return "", ""
	}
	// Typed text: "text" is the wire default of source=arena, so it is left
	// implicit (and the bundles of the shared wizard golden files stay as
	// they are).
	return d.Event.Description, ""
}
