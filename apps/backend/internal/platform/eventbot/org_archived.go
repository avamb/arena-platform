package eventbot

import (
	"strings"
)

// orgArchivedKeys lists the bot.org_* keys that tell a person their
// organization was archived by the platform; the locale test checks them in
// every supported catalog together with MessageKeys.
var orgArchivedKeys = []string{
	"bot.org_archived_note",
	"bot.org_all_archived",
}

func init() {
	MessageKeys = append(MessageKeys, orgArchivedKeys...)
}

// splitArchived separates the organizations a person can work in from the ones
// a platform superadmin archived (organizations.deleted_at). An archived
// organization is never the current one and never a chooser button: it is only
// named in a note.
func splitArchived(all []Membership) (active, archived []Membership) {
	active = make([]Membership, 0, len(all))
	for _, m := range all {
		if m.Archived {
			archived = append(archived, m)
			continue
		}
		active = append(active, m)
	}
	return active, archived
}

// archivedNames is the HTML-safe, comma-separated list of archived
// organization names for the bot.org_* notes.
func archivedNames(archived []Membership) string {
	names := make([]string, 0, len(archived))
	for _, m := range archived {
		names = append(names, "<b>"+Esc(truncate(m.OrgName, 48))+"</b>")
	}
	return strings.Join(names, ", ")
}

// archivedNote is the line appended under the menu and the chooser while a
// person still has working organizations next to archived ones. Empty when
// nothing is archived.
func (b *Bot) archivedNote(id *Identity) string {
	if id == nil || len(id.Archived) == 0 {
		return ""
	}
	return "\n\n" + b.texts.T(id.Locale(), "bot.org_archived_note", map[string]any{"List": archivedNames(id.Archived)})
}
