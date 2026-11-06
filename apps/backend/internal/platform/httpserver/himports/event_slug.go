package himports

import (
	"context"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventslug"
)

// assignEventSlug gives a freshly created event its public address:
// tickets.arenasoldout.com/{page}/{slug}. Until 2026-09-29 an import never
// set one, so an event created from the Telegram bot's wizard had no page
// at all — the hosted-page queries skip a NULL slug. The rule (transliterated
// name, numeric suffix, an event that already has a slug keeps it) lives in
// the eventslug package, shared with publication and the backfill command.
func assignEventSlug(ctx context.Context, q *gen.Queries, orgID, eventID uuid.UUID, name string) error {
	_, err := eventslug.Assign(ctx, q, orgID, eventID, name)
	return err
}

// freeEventSlug finds, reading only, the slug a new event called name would
// get. The dry run reports it as a default without assigning it.
func freeEventSlug(ctx context.Context, q *gen.Queries, orgID uuid.UUID, name string) (string, error) {
	return eventslug.Free(ctx, q, orgID, name)
}
