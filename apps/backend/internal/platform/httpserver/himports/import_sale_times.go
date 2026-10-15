package himports

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// applyImportSaleTimes writes the bundle's session-level sales end
// (actionEvent.sellEndTime, already parsed into plan.SaleWindowEnd) and
// doors-open time (actionEvent.doorsOpenTime) onto the session, migration
// 0128. An omitted sellEndTime keeps what the session has — the start on a
// new session (the 0128 trigger), the organizer's choice on a repeat
// import. An omitted doorsOpenTime keeps the stored value, "" clears it.
// Runs after the insert / update, in the import's transaction.
func applyImportSaleTimes(ctx context.Context, q *gen.Queries, plan importPlan, sessionID, eventID uuid.UUID, startAt time.Time) error {
	doors, setDoors, err := importDoorsOpen(plan.Request.ActionEvent, startAt)
	if err != nil {
		return err
	}
	if plan.SaleWindowEnd == nil && !setDoors {
		return nil
	}
	if _, err := q.SetSessionSaleTimes(ctx, sessionID, eventID, plan.SaleWindowEnd, doors, setDoors); err != nil {
		return fmt.Errorf("set session sale times: %w", err)
	}
	return nil
}

// importDoorsOpen reads actionEvent.doorsOpenTime (see ParseDoorsOpen) and
// refuses one that does not parse or lies after the session start. A zero
// startAt skips the second check (the start is not known yet).
func importDoorsOpen(e bil24compat.ImportSessionActionEvent, startAt time.Time) (*time.Time, bool, error) {
	doors, setDoors, err := e.ParseDoorsOpen()
	if err != nil {
		return nil, false, failImportAt(http.StatusUnprocessableEntity, "import.invalid_doors_open_time",
			"/actionEvent/doorsOpenTime", err.Error())
	}
	if doors != nil && !startAt.IsZero() && doors.After(startAt) {
		return nil, false, failImportAt(http.StatusUnprocessableEntity, "import.invalid_doors_open_time",
			"/actionEvent/doorsOpenTime", "actionEvent.doorsOpenTime must not be after the session start")
	}
	return doors, setDoors, nil
}
