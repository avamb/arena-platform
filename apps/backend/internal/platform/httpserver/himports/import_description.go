package himports

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventtext"
)

// importDescription reads action.description (spec 43, RT-02) and returns the
// ART to store: richtext.Normalize of the text in action.descriptionFormat
// ("text" by default). nil means the bundle carries no description (the stored
// one stays). A refused text or an unknown format is 422
// import.invalid_description, found by the dry run as well.
func importDescription(a bil24compat.ImportSessionAction) (*string, error) {
	format, ok := eventtext.ParseFormat(a.DescriptionFormat)
	if !ok {
		return nil, failImportAt(http.StatusUnprocessableEntity, "import.invalid_description",
			"/action/descriptionFormat", "action.descriptionFormat must be one of: text, art")
	}
	if optString(a.Description) == nil {
		return nil, nil
	}
	clean, err := eventtext.NormalizeDescription(a.Description, format)
	if err != nil {
		return nil, failImportAt(http.StatusUnprocessableEntity, "import.invalid_description",
			"/action/description", "action.description is not allowed: "+eventtext.Reason(err)+": "+err.Error())
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
		return failImportAt(http.StatusUnprocessableEntity, "import.invalid_description",
			"/action/description", "a stored description of the event cannot be converted: "+eventtext.Reason(ue.Err))
	}
	return err
}
