package horders

import (
	"context"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// The CSV export of a session summary (httpserver/hexport) renders the very
// same per-category lines as the JSON route, so it reads them through the one
// assembly in summary_core.go instead of re-deriving them.

// SessionSummary is the JSON body of the session summary route.
type SessionSummary = sessionSummary

// SummaryTier is one category line of a summary.
type SummaryTier = summaryTier

// PlaceCounts are the places of a session or one category by status.
type PlaceCounts = placeCounts

// LoadSessionSummary reads every aggregate of one session, scoped to the
// organization, and folds them like HandleSessionSummary does. A session that
// is not this organization's answers pgx.ErrNoRows; any other error names the
// failing part for the log.
func LoadSessionSummary(ctx context.Context, q *gen.Queries, sessionID, orgID uuid.UUID) (SessionSummary, string, error) {
	header, err := q.GetSessionSummaryHeader(ctx, sessionID, orgID)
	if err != nil {
		return SessionSummary{}, "header", err
	}
	rows, part, err := queriesSummaryRows(ctx, q, []uuid.UUID{sessionID})
	if err != nil {
		return SessionSummary{}, part, err
	}
	return buildSessionSummary(header, rows), "", nil
}
