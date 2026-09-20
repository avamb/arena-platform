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
//
// The same treatment was extended (2026-09-20, same day) to the four
// remaining fields the ported layout draws but nothing populated: the
// order number (orders.system_id, printed in the footer), the venue
// street address, the price the buyer actually paid for THAT ticket
// (order_items.total, not the tier's list price) and the poster artwork
// beside the date block (sessions.poster_media_id ?? events.poster_media_id).
// The poster's BYTES are fetched separately by resolvePoster through the
// same MediaResolver the org logo already goes through — bounded in time
// and size, and dropped rather than failed on any problem.
package delivery

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/pdf"
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
		p.VenueAddress == "" ||
		p.VenueCity == "" ||
		p.TierName == "" ||
		p.HolderName == "" ||
		p.TicketNumber == "" ||
		p.OrderNumber == "" ||
		p.PriceMinor == nil ||
		p.PosterMediaID == ""
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
	fillString(&p.VenueAddress, row.VenueAddress)
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
	// orders.system_id is likewise a NOT NULL sequence column, so a
	// non-positive value can only be a row that never got one.
	if p.OrderNumber == "" && row.OrderNumber != nil && *row.OrderNumber > 0 {
		p.OrderNumber = strconv.FormatInt(*row.OrderNumber, 10)
	}

	// Price. A resolved amount of 0 is left UNSET on purpose rather than
	// carried through as a zero: that is the invitation case (the whole
	// subtotal is discounted, so every order item totals 0), and a ticket
	// that says "0 EUR" reads as a pricing bug rather than as a gift. The
	// renderer drops the price cell for both nil and 0 — setting nil here
	// just makes the intent explicit one layer earlier.
	if p.PriceMinor == nil && row.PriceMinor != nil && *row.PriceMinor > 0 {
		amount := *row.PriceMinor
		p.PriceMinor = &amount
	}
	fillString(&p.Currency, row.PriceCurrency)

	if p.PosterMediaID == "" && row.PosterMediaID != nil && *row.PosterMediaID != uuid.Nil {
		p.PosterMediaID = row.PosterMediaID.String()
	}
}

// Bounds on the poster fetch. The poster is decoration: it may cost the
// delivery of a ticket neither a long wait nor an unbounded allocation.
const (
	// posterFetchTimeout caps how long the media store may take. The
	// job's own context still applies; this only shortens it.
	posterFetchTimeout = 5 * time.Second
	// maxPosterBytes is the largest artwork that is worth attaching to
	// every single e-mail. Organizer posters are web-sized JPEGs; anything
	// past this is a print master uploaded by mistake.
	maxPosterBytes = 8 << 20 // 8 MiB
)

// resolvePoster fetches the poster artwork for mediaID through the same
// MediaResolver the org logo uses, and returns the bytes the PDF layout
// should draw — or nil.
//
// EVERY failure mode returns nil — silently when there is simply nothing
// to fetch (no media resolver wired, no poster on the event), with a
// warning when something went wrong: a media row that vanished, a store
// outage, a slow store, artwork larger than maxPosterBytes, or a media
// object whose
// bytes are not one of the image formats the renderer accepts (a PDF or
// an SVG poster is perfectly legitimate media — it simply cannot be drawn
// into this page). A ticket without its poster is still a valid ticket; a
// buyer who never receives one is not.
func resolvePoster(ctx context.Context, m MediaResolver, mediaID string, logger *slog.Logger) []byte {
	id := strings.TrimSpace(mediaID)
	if id == "" || m == nil {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, posterFetchTimeout)
	defer cancel()

	raw, _, err := m.ResolveLogo(fetchCtx, id)
	switch {
	case err == nil:
	case errors.Is(err, ErrLogoNotFound):
		logger.Warn("delivery: poster media id not found; rendering the ticket without a poster",
			slog.String("poster_media_id", id),
		)
		return nil
	default:
		logger.Warn("delivery: poster fetch failed; rendering the ticket without a poster",
			slog.String("poster_media_id", id),
			slog.String("error", err.Error()),
		)
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxPosterBytes {
		logger.Warn("delivery: poster is too large to attach to every e-mail; rendering the ticket without it",
			slog.String("poster_media_id", id),
			slog.Int("bytes", len(raw)),
			slog.Int("limit_bytes", maxPosterBytes),
		)
		return nil
	}
	// Sniff the bytes rather than trusting media_objects.content_type: the
	// renderer's own decoder is the only authority on what it can draw, and
	// a mislabelled upload would otherwise reach gofpdf.
	if !pdf.SupportsImage(raw) {
		logger.Warn("delivery: poster is not an image format the ticket renderer accepts; rendering the ticket without it",
			slog.String("poster_media_id", id),
		)
		return nil
	}
	return raw
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
