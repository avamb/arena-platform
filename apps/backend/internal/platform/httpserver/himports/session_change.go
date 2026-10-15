package himports

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
)

// applySessionChange runs sessionchange.Apply for an import that has just
// rewritten an existing session inside tx. before is the session read (and
// row-locked) BEFORE the UPDATE; plan.Request.ChangeMessage is the bundle's
// changeMessage. Refusals come back as importErrors, so the whole import
// rolls back and the session keeps its old date instead of moving without its
// buyers' letters.
//
// A dry run never journals and never queues a letter: it asks
// sessionchange.Preview the same question — would this change be refused? —
// and answers with the same errors, writing nothing.
func applySessionChange(ctx context.Context, tx pgx.Tx, plan importPlan, before sessionchange.Session) error {
	message := plan.Request.ChangeMessage
	if plan.Mode.DryRun {
		return previewSessionChange(ctx, tx, before, message)
	}
	_, err := sessionchange.Apply(ctx, tx, sessionchange.Input{
		SessionID: before.ID,
		Old:       before.State,
		Notice:    sessionchange.NoticeFor(ctx, message),
	})
	return sessionChangeError(err)
}

// previewSessionChange is the dry-run half of applySessionChange.
func previewSessionChange(ctx context.Context, tx pgx.Tx, before sessionchange.Session, message string) error {
	if _, err := sessionchange.NormalizeMessage(message); err != nil {
		return sessionChangeError(err)
	}
	after, err := sessionchange.Load(ctx, tx, before.ID, false)
	if err != nil {
		return fmt.Errorf("sessionchange: reload session: %w", err)
	}
	impact, err := sessionchange.Preview(ctx, tx, before, after.State)
	if err != nil {
		return err
	}
	switch impact.Blocked {
	case sessionchange.BlockedSiteRoute:
		return sessionChangeError(sessionchange.ErrSiteRoute)
	case sessionchange.BlockedContactMissing:
		return sessionChangeError(sessionchange.ErrContactMissing)
	}
	return nil
}

// sessionChangeError maps the sessionchange refusals onto the import's error
// envelope. Anything else is passed through.
func sessionChangeError(err error) error {
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
		return failImportAt(http.StatusUnprocessableEntity, "session.change_message_too_long", "/changeMessage",
			"changeMessage is too long")
	default:
		return err
	}
}
