// presentation.go — render-time resolution of the values a ticket e-mail
// and PDF actually show the buyer (feature: "empty ticket PDF", found on
// the first live client 2026-09-20).
//
// delivery.Payload has carried EventName / SessionStart / SessionTZ /
// VenueName / VenueCity / TierName / HolderName since feature #141 as
// "presentation hints baked into the job at enqueue time". Nothing in
// production ever set them: htickets/delivery_enqueue.go builds
// {TicketID, Locale} plus the seat fields, the complimentary path adds
// only Template, and the admin resend sends {TicketID, Locale}. The PDF
// therefore fell back to defaultStr(p.EventName, "Arena Event") and
// printed blank Venue / Category / Holder rows, with the session time in
// UTC because SessionTZ was empty too.
//
// Rather than teaching all three enqueuers the same joins across
// events/sessions/venues/tiers/orders, the worker resolves them here from
// the ticket's own rows — exactly like it already resolves the recipient
// address (step 5) and the EAN-13 credential (step 8b) at render time.
// One code path, and a job that sat in the queue across an event rename
// renders the current values rather than stale ones.
//
// A payload hint still WINS: only fields the enqueuer left empty are
// filled, so existing callers and tests that do set them are unaffected.
//
// Resolution is BEST EFFORT throughout. A lookup failure logs a warning
// and the ticket renders with whatever it has, exactly as before — a
// missing venue must never hold up a buyer's ticket e-mail.
package delivery

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// presentationQuerier is the narrow slice of *gen.Queries this file needs.
// Declared as an interface so the fill logic can be exercised without a
// live database.
type presentationQuerier interface {
	GetTicketPresentationByID(ctx context.Context, ticketID uuid.UUID) (gen.TicketPresentationRow, error)
}

// needsPresentation reports whether any field resolvePresentation could
// fill is still empty. When an enqueuer supplied every hint the query is
// skipped entirely.
func needsPresentation(p Payload) bool {
	return p.EventName == "" ||
		p.SessionStart.IsZero() ||
		p.SessionTZ == "" ||
		p.VenueName == "" ||
		p.VenueCity == "" ||
		p.TierName == "" ||
		p.HolderName == "" ||
		p.TicketNumber == ""
}

// resolvePresentation fills the empty presentation fields of p from the
// ticket's own rows. It mutates p in place and never returns an error:
// every failure path leaves p untouched and logs.
func resolvePresentation(
	ctx context.Context,
	q presentationQuerier,
	ticketID uuid.UUID,
	p *Payload,
	logger *slog.Logger,
) {
	if q == nil || p == nil || !needsPresentation(*p) {
		return
	}
	row, err := q.GetTicketPresentationByID(ctx, ticketID)
	if err != nil {
		// pgx.ErrNoRows means the ticket row vanished between issuance and
		// delivery — nothing to resolve, and nothing worth an error log.
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("delivery: ticket presentation lookup failed; rendering with the payload values only",
				slog.String("ticket_id", ticketID.String()),
				slog.String("error", err.Error()),
			)
		}
		return
	}
	applyPresentation(p, row)
}

// applyPresentation copies the resolved row onto the payload, field by
// field, never overwriting a value the enqueuer already supplied. Split
// out from resolvePresentation so the hint-wins rule is unit-testable
// without a database.
func applyPresentation(p *Payload, row gen.TicketPresentationRow) {
	fillString(&p.EventName, row.EventName)
	fillString(&p.VenueName, row.VenueName)
	fillString(&p.VenueCity, row.VenueCity)
	fillString(&p.SessionTZ, row.VenueTimezone)
	fillString(&p.TierName, row.TierName)
	fillString(&p.HolderName, row.HolderName)

	if p.SessionStart.IsZero() && row.SessionStartAt != nil {
		p.SessionStart = *row.SessionStartAt
	}
	// system_ticket_id is a NOT NULL bigint sequence column (migration
	// 0088), so 0 can only mean "column not populated for this row".
	if p.TicketNumber == "" && row.SystemTicketID > 0 {
		p.TicketNumber = strconv.FormatInt(row.SystemTicketID, 10)
	}
}

// fillString assigns *src to *dst when dst is empty (after trimming) and
// src carries a non-blank value.
func fillString(dst *string, src *string) {
	if dst == nil || src == nil || strings.TrimSpace(*dst) != "" {
		return
	}
	if v := strings.TrimSpace(*src); v != "" {
		*dst = v
	}
}
