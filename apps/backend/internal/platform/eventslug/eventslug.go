// Package eventslug owns the public page address of an event
// (tickets.arenasoldout.com/{page}/{slug}): how a first one is derived from
// the name, how an organizer-typed one is validated, and how a free one is
// found inside the organization. Import, publication, the backfill command
// and PATCH all go through it so the rule cannot drift.
package eventslug

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
)

// MaxLen is the longest slug an organizer may type.
const MaxLen = 120

// maxAttempts bounds the "-2", "-3", … search for a free slug.
const maxAttempts = 50

// reserved are page addresses taken by routes of the hosted page itself:
// GET /v1/public/pages/{org_slug}/terms is the organizer's terms of sale.
var reserved = map[string]bool{"terms": true}

var slugRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Normalize trims and lower-cases a typed slug and reports whether it is a
// valid one: Latin letters, digits and single hyphens, up to MaxLen, not a
// reserved page address.
func Normalize(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || len(s) > MaxLen || !slugRE.MatchString(s) || reserved[s] {
		return "", false
	}
	return s, true
}

// Free finds, reading only, the slug a new event called name would get.
func Free(ctx context.Context, q *gen.Queries, orgID uuid.UUID, name string) (string, error) {
	base := geoslug.Slugify(name)
	if base == "" {
		base = "event"
	}
	if len(base) > MaxLen-6 {
		base = strings.Trim(base[:MaxLen-6], "-")
	}
	for i := 1; i <= maxAttempts; i++ {
		candidate := base
		if i > 1 {
			candidate = base + "-" + strconv.Itoa(i)
		}
		if reserved[candidate] {
			continue
		}
		taken, err := q.EventSlugTaken(ctx, orgID, candidate)
		if err != nil {
			return "", fmt.Errorf("check event slug %q: %w", candidate, err)
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free slug for event %q after %d attempts", name, maxAttempts)
}

// Assign gives an event without a slug its first one. An event that already
// carries a slug keeps it: the slug is the public address, and a rename must
// not move the page. It reports the slug it set, "" when nothing changed.
func Assign(ctx context.Context, q *gen.Queries, orgID, eventID uuid.UUID, name string) (string, error) {
	candidate, err := Free(ctx, q, orgID, name)
	if err != nil {
		return "", err
	}
	changed, err := q.SetEventSlugIfEmpty(ctx, eventID, orgID, candidate)
	if err != nil {
		return "", fmt.Errorf("set event slug %q: %w", candidate, err)
	}
	if !changed {
		return "", nil
	}
	return candidate, nil
}
