package eventtext

import "github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"

// DescriptionView is a stored event description as its readers need it
// (spec 44, RT-03a): the same text three ways.
type DescriptionView struct {
	// HTML is richtext.Render with the description profile: safe HTML for
	// buyers' pages and the gateway. Never empty for a text with content.
	HTML string
	// Text is richtext.PlainText of the rendered ART: for <title>, <meta>
	// and counters.
	Text string
	// Legacy is the old "**bold**, - list" form for forms and clients that
	// cannot show markup: richtext.ToLegacy of the ART, and the stored text
	// itself for a "text" row.
	Legacy string
}

// ViewDescription builds the readers' views of a stored description. format is
// the event's flag (description_format): anything but "art" is the old text.
// The flag of an event covers its translations, so a locale-resolved
// description is read with the event's flag. A text that cannot be converted
// still yields its escaped visible text (richtext.Render's contract).
func ViewDescription(stored, format string) DescriptionView {
	html, _ := richtext.Render(stored, format, richtext.ProfileDescription)
	v := DescriptionView{HTML: html, Text: richtext.PlainText(html)}
	if format == richtext.FormatART {
		v.Legacy = richtext.ToLegacy(stored)
	} else {
		v.Legacy = stored
	}
	return v
}
