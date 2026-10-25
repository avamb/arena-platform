package delivery

import (
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventnote"
)

// eventNoteBlock turns the stored note (event_email_notes, EC-31) and the
// format it is stored in into the block the templates print. A missing format
// (a job queued before the release) reads as the old plain text. The zero
// Block prints nothing.
func eventNoteBlock(note, format string) eventnote.Block {
	return eventnote.Build(note, format)
}

// ticketEventNote is the note the e-mail of the given template kind prints:
// the ticket letter only. An invitation is a gift and carries the organizer's
// words in its own way, a refund or a cancellation is not the place for them.
func ticketEventNote(kind, note, format string) eventnote.Block {
	if kind != templates.TemplateKindTicket {
		return eventnote.Block{}
	}
	return eventNoteBlock(note, format)
}

// changeEventNote is the note of a session-change letter. A cancellation
// ("moves" false) prints none: the event is not happening.
func changeEventNote(moves bool, note, format string) eventnote.Block {
	if !moves {
		return eventnote.Block{}
	}
	return eventNoteBlock(note, format)
}
