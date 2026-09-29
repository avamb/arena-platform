package himports

import (
	"context"
	"fmt"
	"strconv"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
)

// eventSlugMaxAttempts bounds the "-2", "-3", … search for a free slug.
const eventSlugMaxAttempts = 50

// assignEventSlug gives a freshly created event its public address:
// tickets.arenasoldout.com/{page}/{slug}. Until 2026-09-29 an import never
// set one, so an event created from the Telegram bot's wizard had no page
// at all — the hosted-page queries skip a NULL slug — and the bot's "your
// sales link" pointed at the organization's list only. The slug is the
// transliterated name (geoslug.Slugify, "event" when nothing survives),
// made unique among the organization's active events with a numeric
// suffix. An event that already carries a slug keeps it: the slug is the
// public address, and a rename must not move the page.
func assignEventSlug(ctx context.Context, q *gen.Queries, orgID, eventID uuid.UUID, name string) error {
	base := geoslug.Slugify(name)
	if base == "" {
		base = "event"
	}
	for i := 1; i <= eventSlugMaxAttempts; i++ {
		candidate := base
		if i > 1 {
			candidate = base + "-" + strconv.Itoa(i)
		}
		taken, err := q.EventSlugTaken(ctx, orgID, candidate)
		if err != nil {
			return fmt.Errorf("check event slug %q: %w", candidate, err)
		}
		if taken {
			continue
		}
		if _, err := q.SetEventSlugIfEmpty(ctx, eventID, orgID, candidate); err != nil {
			return fmt.Errorf("set event slug %q: %w", candidate, err)
		}
		return nil
	}
	return fmt.Errorf("no free slug for event %q after %d attempts", name, eventSlugMaxAttempts)
}
