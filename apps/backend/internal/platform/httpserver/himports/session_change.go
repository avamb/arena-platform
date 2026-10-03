package himports

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
)

// applySessionChange runs sessionchange.Apply for an import that has just
// rewritten an existing session inside tx. before is the session's state read
// (and row-locked) BEFORE the UPDATE; message is the bundle's changeMessage.
// Refusals come back as importErrors, so the whole import rolls back and the
// session keeps its old date instead of moving without its buyers' letters.
func applySessionChange(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID, before sessionchange.State, message string) error {
	_, err := sessionchange.Apply(ctx, tx, sessionchange.Input{
		SessionID: sessionID,
		Old:       before,
		Notice:    sessionchange.NoticeFor(ctx, message),
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sessionchange.ErrContactMissing):
		return failImport(http.StatusUnprocessableEntity, "organization.contact_missing",
			"buyers have bought tickets for this session, so the organizer's contact e-mail must be set before it can be moved")
	case errors.Is(err, sessionchange.ErrSiteRoute):
		return failImport(http.StatusUnprocessableEntity, "session.change_site_unsupported",
			"tickets for this session were sold through a website that cannot be notified of a change yet, so it cannot be moved here")
	case errors.Is(err, sessionchange.ErrMessageTooLong):
		return failImport(http.StatusUnprocessableEntity, "session.change_message_too_long",
			"changeMessage is too long")
	default:
		return err
	}
}
