package himports

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventnote"
)

// importEmailNote reads action.emailNote (EC-31, source=arena): set is false
// when the bundle leaves it out (the stored text stays), note is "" when the
// bundle asks to remove it. A text the filter refuses is 422
// import.invalid_email_note, found by the dry run as well.
func importEmailNote(a bil24compat.ImportSessionAction) (note string, set bool, err error) {
	if a.EmailNote == nil {
		return "", false, nil
	}
	clean, nErr := eventnote.Normalize(*a.EmailNote)
	if nErr != nil {
		return "", false, failImportAt(http.StatusUnprocessableEntity, "import.invalid_email_note",
			"/action/emailNote", "action.emailNote is not allowed: "+nErr.Error())
	}
	return clean, true, nil
}

// applyArenaEmailNote stores action.emailNote inside the import transaction:
// absent keeps the event's text, "" removes it, a string replaces it.
func applyArenaEmailNote(ctx context.Context, q *gen.Queries, plan importPlan, eventID uuid.UUID) error {
	note, set, err := importEmailNote(plan.Request.Action)
	if err != nil || !set {
		return err
	}
	if note == "" {
		if err := q.DeleteEventEmailNote(ctx, eventID, plan.OrgID); err != nil {
			return fmt.Errorf("clear event email note: %w", err)
		}
		return nil
	}
	if err := q.UpsertEventEmailNote(ctx, eventID, plan.OrgID, note); err != nil {
		return fmt.Errorf("set event email note: %w", err)
	}
	return nil
}
