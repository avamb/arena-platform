package himports

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventtext"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// importDescription reads action.description (spec 44, RT-02) and returns the
// ART to store: richtext.Normalize of the text in action.descriptionFormat
// ("text" by default for source=arena, "art" for source=bil24, see
// importTextFormat). nil means the bundle carries no description (the stored
// one stays). A refused text or an unknown format is 422
// import.invalid_description, found by the dry run as well.
func importDescription(source string, a bil24compat.ImportSessionAction) (*string, error) {
	format, ok := importTextFormat(source, a.DescriptionFormat)
	if !ok {
		return nil, failImportAt(http.StatusUnprocessableEntity, "import.invalid_description",
			"/action/descriptionFormat", "action.descriptionFormat must be one of: text, art")
	}
	if optString(a.Description) == nil {
		return nil, nil
	}
	clean, err := eventtext.NormalizeDescription(a.Description, format)
	if err != nil {
		return nil, failImportReason(http.StatusUnprocessableEntity, "import.invalid_description",
			"/action/description", eventtext.Reason(err), "action.description is not allowed: "+eventtext.Reason(err)+": "+err.Error())
	}
	if clean == "" {
		return nil, nil
	}
	return &clean, nil
}

// upgradeEventDescriptions converts the event's other stored descriptions (its
// translations) to ART when the bundle is about to store ART into an event
// whose flag is still "text": the flag covers the translations too. Without a
// description in the bundle nothing is written and nothing changes.
func upgradeEventDescriptions(ctx context.Context, q *gen.Queries, orgID, eventID uuid.UUID, desc *string) error {
	if desc == nil {
		return nil
	}
	err := eventtext.UpgradeDescriptions(ctx, q, eventID, orgID, true)
	if ue, ok := eventtext.AsUpgradeError(err); ok {
		return failImportReason(http.StatusUnprocessableEntity, "import.invalid_description",
			"/action/description", eventtext.Reason(ue.Err), "a stored description of the event cannot be converted: "+eventtext.Reason(ue.Err))
	}
	return err
}

// importTextFormat reads a descriptionFormat / emailNoteFormat field of a
// bundle. An absent format means "text" (the old marks) for source=arena, but
// "art" for source=bil24: the WordPress sites that use that route send HTML,
// and reading it as plain text would show the tags as words. The text is
// sanitised either way; an explicit format always wins.
func importTextFormat(source, raw string) (string, bool) {
	if raw == "" && source == bil24compat.SourceBil24 {
		return richtext.FormatART, true
	}
	return eventtext.ParseFormat(raw)
}
