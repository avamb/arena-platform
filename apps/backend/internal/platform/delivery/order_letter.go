// order_letter.go — ONE e-mail per order.
//
// Until 2026-10-07 every ticket of an order had its own delivery_jobs row, its
// own ticket.deliver worker job and its own e-mail, so a buyer of three seats
// got three letters. The sites the platform replaces (Lampyris, Vino&Co) send
// one letter with every PDF attached, and so does the ticket e-mail now: the
// first worker job of an order claims EVERY pending delivery job of that order
// and recipient in one statement (gen.ClaimPendingDeliveryJobsForOrder), builds
// all the PDFs, sends one message and marks all the jobs sent. The worker jobs
// of the other tickets find their delivery job already taken and end without
// sending.
//
// An invitation is NOT grouped (its letter describes one ticket), and a resend
// of a single ticket stays a single-ticket letter, because only that ticket's
// job is pending again.
package delivery

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/pdf"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

// letterMember is one ticket of the e-mail being built.
type letterMember struct {
	ticketID uuid.UUID
	jobID    uuid.UUID
	p        Payload
	pdf      []byte
	number   string
}

// claimLetterMembers claims the delivery jobs one e-mail will cover. For a
// ticket e-mail that is every pending job of the order and recipient (own job
// first); for an invitation, or a ticket without an order, only the ticket's
// own. It returns (nil, nil) when the own job is not pending — somebody else
// has it, or it is already terminal — which the caller treats as an
// idempotent skip.
func claimLetterMembers(
	ctx context.Context,
	q *gen.Queries,
	ticketID, jobID uuid.UUID,
	p Payload,
) ([]*letterMember, error) {
	if p.Template == TemplateInvitation {
		if _, err := q.ClaimDeliveryJobForProcessing(ctx, jobID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		return []*letterMember{{ticketID: ticketID, jobID: jobID, p: p}}, nil
	}

	claimed, err := q.ClaimPendingDeliveryJobsForOrder(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	var members []*letterMember
	ownSeen := false
	for _, j := range claimed {
		m := &letterMember{ticketID: j.TicketID, jobID: j.ID}
		if j.TicketID == ticketID {
			ownSeen = true
			m.p = p
			members = append([]*letterMember{m}, members...) // own first
			continue
		}
		members = append(members, m)
	}
	if !ownSeen {
		// Defensive: the statement only claims anything on behalf of a pending
		// own job. Hand back whatever came with it rather than strand it.
		releaseLetterMembers(ctx, q, toMembers(claimed), "own delivery job was not part of the claim", nil)
		return nil, nil
	}
	return members, nil
}

func toMembers(jobs []gen.DeliveryJobRow) []*letterMember {
	out := make([]*letterMember, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, &letterMember{ticketID: j.TicketID, jobID: j.ID})
	}
	return out
}

// releaseLetterMembers hands every claimed job back to 'pending' with the
// reason, so the worker's retry can claim the whole letter again. Best effort:
// a failure here is logged and the retry's claim will simply miss that row.
func releaseLetterMembers(ctx context.Context, q *gen.Queries, members []*letterMember, reason string, logger *slog.Logger) {
	if q == nil {
		return
	}
	for _, m := range members {
		if m.jobID == uuid.Nil {
			continue
		}
		if _, err := q.UpdateDeliveryJobStatus(ctx, m.jobID, StatusPending, &reason); err != nil && logger != nil {
			logger.Error("delivery: could not release delivery_job back to pending",
				slog.String("delivery_job_id", m.jobID.String()),
				slog.String("ticket_id", m.ticketID.String()),
				slog.String("error", err.Error()),
			)
		}
	}
}

// siblingPayload is the payload for another ticket of the same e-mail: the
// letter-level fields of p (template, language, organisation branding, sender)
// and none of its ticket-specific ones, which are resolved from the sibling's
// own rows.
func siblingPayload(p Payload, ticketID uuid.UUID) Payload {
	sp := p
	sp.TicketID = ticketID.String()
	sp.EventName = ""
	sp.SessionStart = time.Time{}
	sp.SessionTZ = ""
	sp.DoorsOpenAt = nil
	sp.VenueName = ""
	sp.VenueAddress = ""
	sp.VenueCity = ""
	sp.TierName = ""
	sp.HolderName = ""
	sp.TicketNumber = ""
	sp.OrderNumber = ""
	sp.PriceMinor = nil
	sp.Currency = ""
	sp.PosterMediaID = ""
	sp.PromoterName = ""
	sp.SeatSector, sp.SeatRow, sp.SeatNumber = "", "", ""
	sp.QRPayload = ""
	sp.HumanCode = ""
	sp.EAN13 = ""
	return sp
}

// prepareLetterTicket resolves everything one ticket of the letter needs (its
// seat, EAN-13, presentation values) and its PDF, and stores the PDF and the
// buyer-facing number on the member. The PDF credential is read when it exists
// and created otherwise, exactly as the single-ticket path always did.
func prepareLetterTicket(
	ctx context.Context,
	opts HandlerOptions,
	logger *slog.Logger,
	m *letterMember,
	own bool,
	branding templates.Branding,
	logoBytes []byte,
	template Payload,
) error {
	if !own {
		m.p = siblingPayload(template, m.ticketID)
		if opts.TicketQueries != nil {
			t, err := opts.TicketQueries.GetTicketByID(ctx, m.ticketID)
			switch {
			case err == nil:
				if t.SeatSector != nil {
					m.p.SeatSector = *t.SeatSector
				}
				if t.SeatRow != nil {
					m.p.SeatRow = *t.SeatRow
				}
				if t.SeatNumber != nil {
					m.p.SeatNumber = *t.SeatNumber
				}
			case !errors.Is(err, pgx.ErrNoRows):
				return fmt.Errorf("delivery: get ticket %s: %w", m.ticketID, err)
			}
		}
	}
	p := &m.p

	// EAN-13 (feature #503): best effort, never fatal.
	if p.EAN13 == "" && opts.CredentialQueries != nil {
		eanCred, eanErr := opts.CredentialQueries.GetCredentialByTicketID(ctx, m.ticketID, "ean13")
		if eanErr != nil {
			if !errors.Is(eanErr, pgx.ErrNoRows) {
				logger.Warn("delivery: get ean13 credential failed; rendering pdf without it",
					slog.String("ticket_id", m.ticketID.String()),
					slog.String("error", eanErr.Error()),
				)
			}
		} else {
			p.EAN13 = eanCred.Payload
		}
	}

	if opts.TicketQueries != nil {
		resolvePresentation(ctx, opts.TicketQueries, m.ticketID, p, logger)
	}

	pdfBytes, err := ticketPDFBytes(ctx, opts, logger, m.ticketID, *p, branding, logoBytes)
	if err != nil {
		return err
	}
	m.pdf = pdfBytes
	m.number = pdf.DisplayNumber(p.TicketNumber, m.ticketID.String())
	return nil
}

// sortLetterMembers puts the tickets in number order, so the list and the
// attachments read 253, 254, 255 whatever order the database answered in.
func sortLetterMembers(members []*letterMember) {
	sort.SliceStable(members, func(i, j int) bool {
		a, aErr := strconv.ParseInt(members[i].p.TicketNumber, 10, 64)
		b, bErr := strconv.ParseInt(members[j].p.TicketNumber, 10, 64)
		if aErr == nil && bErr == nil {
			return a < b
		}
		return members[i].number < members[j].number
	})
}

// ticketLines turns the members into the e-mail's ticket list. withPrice adds
// each ticket's own price; the ticket e-mail leaves it off when it prints the
// payment block, which already says what was paid.
func ticketLines(members []*letterMember, locale string, withPrice bool) []templates.TicketLine {
	lines := make([]templates.TicketLine, 0, len(members))
	for _, m := range members {
		l := templates.TicketLine{
			Number: m.number,
			Tier:   m.p.TierName,
			Seat:   templates.FormatSeat(locale, m.p.SeatSector, m.p.SeatRow, m.p.SeatNumber),
		}
		if withPrice && m.p.PriceMinor != nil && *m.p.PriceMinor > 0 {
			l.Price = formatMoney(*m.p.PriceMinor, m.p.Currency)
		}
		lines = append(lines, l)
	}
	return lines
}

// formatMoney prints minor units as "25.25 EUR". The platform stores every
// amount in hundredths of the currency.
func formatMoney(minor int64, currency string) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	out := fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
	if c := strings.TrimSpace(currency); c != "" {
		out += " " + strings.ToUpper(c)
	}
	return out
}

// buildPaymentData makes the e-mail's "Payment" block from the purchase's
// money, or returns nil when there is nothing honest to show: no order, a free
// or complimentary order, an order that is not paid, or a letter that does not
// carry every ticket of the order (a resend of one ticket must not print the
// whole order's total next to a single seat).
//
// The breakdown rows are printed only when they add up to the total; if they
// do not (tax or provider fee the platform never explained to the buyer) the
// block shows the order number, the time and the total alone.
func buildPaymentData(s gen.DeliveryPaymentSummary, tz string, ticketsInLetter int) *templates.PaymentData {
	if s.OrderNumber == nil || *s.OrderNumber <= 0 {
		return nil
	}
	if s.OrderStatus == nil || *s.OrderStatus != "paid" {
		return nil
	}
	if s.OrderSource != nil && *s.OrderSource == "complimentary" {
		return nil
	}
	if s.Total <= 0 || int64(ticketsInLetter) != s.TicketCount {
		return nil
	}
	d := &templates.PaymentData{
		OrderNumber: strconv.FormatInt(*s.OrderNumber, 10),
		Total:       formatMoney(s.Total, s.Currency),
	}
	if s.PaidAt != nil {
		d.PaidAt = formatSessionForEmail(*s.PaidAt, tz)
	}
	if s.Subtotal-s.Discount+s.PlatformFee == s.Total {
		d.Subtotal = formatMoney(s.Subtotal, s.Currency)
		if s.Discount > 0 {
			d.Discount = formatMoney(s.Discount, s.Currency)
		}
		if s.PlatformFee > 0 {
			d.ServiceFee = formatMoney(s.PlatformFee, s.Currency)
		}
	}
	return d
}

// ticketLinesFor is the ticket list of the letter: every ticket for a ticket
// e-mail, nothing for an invitation, whose letter describes one ticket in its
// own words.
func ticketLinesFor(kind string, members []*letterMember, locale string, withPrice bool) []templates.TicketLine {
	if kind != templates.TemplateKindTicket {
		return nil
	}
	return ticketLines(members, locale, withPrice)
}

// ticketPDFBytes returns the PDF of one ticket: the stored credential when
// there is one, otherwise a fresh render that is stored for next time. Without
// a credential store it renders on the fly and, if even that fails, returns no
// bytes so the e-mail still goes out (without an attachment).
func ticketPDFBytes(
	ctx context.Context,
	opts HandlerOptions,
	logger *slog.Logger,
	ticketID uuid.UUID,
	p Payload,
	branding templates.Branding,
	logoBytes []byte,
) ([]byte, error) {
	if opts.CredentialQueries == nil {
		// No credential store — render on the fly so we still attach a PDF.
		poster := resolvePoster(ctx, opts.Media, p.PosterMediaID, logger)
		pdfPayload, prErr := renderTicketPDF(ctx, ticketID, p, branding, logoBytes, poster)
		if prErr != nil {
			logger.Warn("delivery: render pdf failed; sending without attachment",
				slog.String("ticket_id", ticketID.String()),
				slog.String("error", prErr.Error()),
			)
			return nil, nil
		}
		return pdfPayload, nil
	}

	cred, credErr := opts.CredentialQueries.GetCredentialByTicketID(ctx, ticketID, "pdf")
	if credErr != nil {
		if !errors.Is(credErr, pgx.ErrNoRows) {
			return nil, fmt.Errorf("delivery: get pdf credential for ticket %s: %w", ticketID, credErr)
		}
		// Generate and store a new PDF credential. The poster bytes are
		// fetched only on this path, so a ticket whose credential already
		// exists costs the media store nothing; a poster that is missing, too
		// large, slow or not a drawable image yields nil and the page simply
		// has no poster column (see resolvePoster).
		poster := resolvePoster(ctx, opts.Media, p.PosterMediaID, logger)
		pdfPayload, prErr := renderTicketPDF(ctx, ticketID, p, branding, logoBytes, poster)
		if prErr != nil {
			return nil, fmt.Errorf("delivery: render pdf for ticket %s: %w", ticketID, prErr)
		}
		encoded := base64.StdEncoding.EncodeToString(pdfPayload)
		cred, credErr = opts.CredentialQueries.InsertTicketCredential(ctx, ticketID, "pdf", encoded)
		if credErr != nil {
			return nil, fmt.Errorf("delivery: insert pdf credential for ticket %s: %w", ticketID, credErr)
		}
	}
	pdfBytes, decErr := base64.StdEncoding.DecodeString(cred.Payload)
	if decErr != nil {
		return nil, fmt.Errorf("delivery: decode pdf payload for ticket %s: %w", ticketID, decErr)
	}
	return pdfBytes, nil
}
