// Package eventnote is the organizer's own text of an event that the buyer
// letters print (EC-31, task 7-1): a few paragraphs the organizer wants every
// buyer of the event to read ("come 30 minutes early", "the cloakroom is
// free").
//
// The text is stored in event_email_notes (migration 0165, 0168) after
// richtext.Normalize with the letter-text profile, as ART ("text Arena", a
// small HTML subset) with note_format 'art'. A row nobody has written since
// RT-02 is still plain text with note_format 'text'. Build is the ONE reader
// of the letters: it renders the stored text through richtext.Render for the
// format it was stored in, so a link can only reach a letter as an <a href>
// that the sanitiser has checked, and it prepares the plain-text part.
package eventnote

import (
	"html/template"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// Block is a note ready for the letter templates. The zero Block prints
// nothing: templates guard it with {{with .EventNote.HTML}}.
type Block struct {
	// HTML is the HTML part: safe markup produced by richtext.Render, with
	// the inline styles an e-mail client needs. It is template.HTML because
	// the sanitiser, not html/template, vouches for it.
	HTML template.HTML
	// Plain is the text part: the visible text of the note, no tags.
	Plain string
}

// Empty reports whether the block prints nothing.
func (b Block) Empty() bool { return b.HTML == "" }

// Inline styles of the elements of a letter note: the client of a mail reader
// ignores style sheets, so the spacing and the link colour are set on the
// element. The sanitiser's output has bare <p>, <ul>, <ol> and <a href="...">
// tags only (text is escaped, so a literal "<p>" cannot occur inside it).
var emailStyle = strings.NewReplacer(
	"<p>", `<p style="margin:0 0 8px 0;">`,
	"<ul>", `<ul style="margin:0 0 8px 0;padding-left:20px;">`,
	"<ol>", `<ol style="margin:0 0 8px 0;padding-left:20px;">`,
	"<a href=", `<a style="color:#1a73e8;" href=`,
)

// Build turns a stored note and the format it was stored in into a Block. A
// format that is not "art" (including the empty one of a job queued before
// the format was carried) means the old plain text. It never fails: the text
// was filtered when it was stored, and a text that cannot be read still shows
// its words (richtext.Render's fallback). A blank note gives the zero Block.
func Build(note, format string) Block {
	note = strings.TrimSpace(note)
	if note == "" {
		return Block{}
	}
	if format != richtext.FormatART {
		format = richtext.FormatText
	}
	html, _ := richtext.Render(note, format, richtext.ProfileEmailNote)
	if strings.TrimSpace(html) == "" {
		return Block{}
	}
	plain := note
	if format == richtext.FormatART {
		plain = richtext.PlainText(note)
	}
	return Block{HTML: template.HTML(emailStyle.Replace(html)), Plain: strings.TrimSpace(plain)} //nolint:gosec // richtext.Render output is sanitised
}

// BuildMessage turns the organizer's message about a moved or cancelled
// session and the format it was stored in (session_changes.message_format)
// into a Block, with the profile of that message (richtext.ProfileSessionMessage)
// and the same inline styles as Build. A format that is not "art" (including
// the empty one of a row written before migration 0169) means the old plain
// text. It never fails and a blank message gives the zero Block.
func BuildMessage(message, format string) Block {
	message = strings.TrimSpace(message)
	if message == "" {
		return Block{}
	}
	if format != richtext.FormatART {
		format = richtext.FormatText
	}
	html, _ := richtext.Render(message, format, richtext.ProfileSessionMessage)
	if strings.TrimSpace(html) == "" {
		return Block{}
	}
	plain := message
	if format == richtext.FormatART {
		plain = richtext.PlainText(message)
	}
	return Block{HTML: template.HTML(emailStyle.Replace(html)), Plain: strings.TrimSpace(plain)} //nolint:gosec // richtext.Render output is sanitised
}
