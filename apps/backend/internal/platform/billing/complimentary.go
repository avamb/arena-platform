package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Values of orders.issued_via (migration 0174, spec 46). The collector copies
// it into billable_events.attributes under the key AttrIssuedVia.
const (
	IssuedViaStaff     = "staff"
	IssuedViaStaffBulk = "staff_bulk"
	// AttrIssuedVia is the attributes key of a staff invitation's issued_via.
	AttrIssuedVia = "issued_via"
)

// Quote modes: why an invitation costs what it costs.
const (
	QuoteModeFree     = "free"     // free up to the yearly limit
	QuoteModeStandard = "standard" // the account's terms say standard
	QuoteModeBulk     = "bulk"     // a CSV file: always standard
)

// BulkFromAttributes reports whether billable-event attributes name an
// invitation issued from a CSV file (issued_via staff_bulk). Attributes that
// do not decode, or carry no issued_via, are not bulk.
func BulkFromAttributes(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var a struct {
		IssuedVia string `json:"issued_via"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return false
	}
	return a.IssuedVia == IssuedViaStaffBulk
}

// ComplimentaryQuoteInput is one invitation (or one CSV file) to preview.
type ComplimentaryQuoteInput struct {
	// Seated and Unseated count the places on the plan and the general
	// admission places.
	Seated   int
	Unseated int
	// Bulk is true for a CSV file (always the standard rate).
	Bulk bool
	// UsedFree is how many complimentary tickets of the account were already
	// rated free in the calendar year of the terms' instant
	// (gen.CountFreeComplimentaryChargesBefore). Read only when the account
	// bills invitations free and the price list sets a yearly limit.
	UsedFree int64
}

// ComplimentaryQuoteLine is one group of charged tickets of a quote with the
// same unit rate.
type ComplimentaryQuoteLine struct {
	Tickets     int    `json:"tickets"`
	UnitMinor   int64  `json:"unit_minor"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

// ComplimentaryQuote is the billing preview of an invitation (spec 46,
// section 3.6): what the platform will charge the organizer for these
// tickets, computed by Rate exactly as the rate job will.
type ComplimentaryQuote struct {
	// Mode is free, standard or bulk.
	Mode string `json:"mode"`
	// Currency is the billing account's currency.
	Currency       string `json:"currency"`
	Tickets        int    `json:"tickets"`
	FreeTickets    int    `json:"free_tickets"`
	ChargedTickets int    `json:"charged_tickets"`
	AmountMinor    int64  `json:"amount_minor"`
	// Lines groups the charged tickets by unit rate; empty when all are free.
	Lines []ComplimentaryQuoteLine `json:"lines"`
	// FreeLimit is the yearly free limit, nil when there is none or when the
	// mode is not free. FreeLeft is what is left of it AFTER these tickets.
	FreeLimit *int64 `json:"free_limit"`
	FreeLeft  *int64 `json:"free_left"`
	// Text is a short English summary, "12 × 0.50 EUR = 6.00 EUR" or "free,
	// 488 of the yearly limit left". Clients that speak another language
	// build their own from the numbers.
	Text string `json:"text"`
}

// QuoteComplimentary previews the charge of an invitation under terms: every
// ticket goes through Rate as a ticket_complimentary event at terms.At, seated
// tickets first, with FreeTicketsOverLimit set the way the rate job sets it
// (the account's free count of the year has reached the limit). The result
// is what the rate job will write if the terms and the free count do not
// change before the invitation is collected.
func QuoteComplimentary(terms Terms, in ComplimentaryQuoteInput) (ComplimentaryQuote, error) {
	if in.Seated < 0 || in.Unseated < 0 || in.UsedFree < 0 {
		return ComplimentaryQuote{}, fmt.Errorf("%w: negative quote input", ErrInconsistentEvent)
	}
	q := ComplimentaryQuote{Mode: QuoteModeFree, Currency: terms.Currency, Tickets: in.Seated + in.Unseated,
		Lines: []ComplimentaryQuoteLine{}}
	switch {
	case in.Bulk:
		q.Mode = QuoteModeBulk
	case terms.ComplimentaryBilling() == ComplimentaryBillingStandard:
		q.Mode = QuoteModeStandard
	}
	var limit *int64
	if q.Mode == QuoteModeFree {
		limit = terms.FreeTicketsAnnualLimit()
	}
	used := in.UsedFree
	for i := 0; i < q.Tickets; i++ {
		seated := i < in.Seated
		ev := Event{Kind: KindTicketComplimentary, OccurredAt: terms.At, Seated: seated, Currency: terms.Currency,
			Bulk: in.Bulk, FreeTicketsOverLimit: limit != nil && used >= *limit}
		c, err := Rate(terms, ev)
		if err != nil {
			return ComplimentaryQuote{}, err
		}
		if c.Reason == ReasonFree {
			q.FreeTickets++
			used++
			continue
		}
		q.ChargedTickets++
		q.AmountMinor += c.AmountMinor
		q.addLine(c.AmountMinor)
	}
	if limit != nil {
		l, left := *limit, max(*limit-used, 0)
		q.FreeLimit, q.FreeLeft = &l, &left
	}
	q.Text = q.summary()
	return q, nil
}

func (q *ComplimentaryQuote) addLine(unit int64) {
	for i := range q.Lines {
		if q.Lines[i].UnitMinor == unit {
			q.Lines[i].Tickets++
			q.Lines[i].AmountMinor += unit
			return
		}
	}
	q.Lines = append(q.Lines, ComplimentaryQuoteLine{Tickets: 1, UnitMinor: unit, AmountMinor: unit, Currency: q.Currency})
}

func (q ComplimentaryQuote) summary() string {
	var parts []string
	if q.FreeTickets > 0 && q.ChargedTickets > 0 {
		parts = append(parts, strconv.Itoa(q.FreeTickets)+" free")
	}
	for _, l := range q.Lines {
		parts = append(parts, fmt.Sprintf("%d × %s", l.Tickets, formatMinor(l.UnitMinor, q.Currency)))
	}
	var b strings.Builder
	if q.ChargedTickets == 0 {
		b.WriteString("free")
	} else {
		b.WriteString(strings.Join(parts, " + "))
		b.WriteString(" = ")
		b.WriteString(formatMinor(q.AmountMinor, q.Currency))
	}
	if q.FreeLeft != nil {
		fmt.Fprintf(&b, ", %d of the yearly limit left", *q.FreeLeft)
	}
	return b.String()
}

// formatMinor writes an amount of a two-decimal currency: 650 EUR is "6.50 EUR".
func formatMinor(minor int64, currency string) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, minor/100, minor%100, currency)
}

// QuoteQueries is the read set QuoteComplimentaryForSession needs. *gen.Queries
// satisfies it. Nothing is written: an organization without a billing account
// yet is quoted on the terms the collector would give its first account.
type QuoteQueries interface {
	TermsQueries
	RouteQueries
	GetBillingAccount(ctx context.Context, orgID uuid.UUID, currency, accountKind string) (gen.BillingAccountRow, error)
	GetOrgBillingCountry(ctx context.Context, orgID uuid.UUID) (string, error)
	GetSessionVenueCountry(ctx context.Context, sessionID uuid.UUID) (*string, error)
	CountFreeComplimentaryChargesBefore(ctx context.Context, accountID uuid.UUID, from, beforeAt time.Time, beforeID uuid.UUID) (int64, error)
}

// QuoteComplimentaryForSession previews an invitation of the organization on
// the session at the instant at: it finds the billing account the collector
// will bill it to (the session venue's country, else the organization's,
// routed to a currency exactly like billing.collect), loads the terms in
// force at at, counts the free complimentary tickets already rated this year
// when a limit applies, and calls QuoteComplimentary. The issue route of
// staff invitations and the CSV dry run both call it.
func QuoteComplimentaryForSession(ctx context.Context, q QuoteQueries, orgID, sessionID uuid.UUID, at time.Time,
	seated, unseated int, bulk bool,
) (ComplimentaryQuote, error) {
	country := ""
	vc, err := q.GetSessionVenueCountry(ctx, sessionID)
	if err != nil {
		return ComplimentaryQuote{}, fmt.Errorf("billing: venue country of session %s: %w", sessionID, err)
	}
	if vc != nil {
		country = *vc
	}
	if NormalizeCountry(country) == "" {
		c, err := q.GetOrgBillingCountry(ctx, orgID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return ComplimentaryQuote{}, fmt.Errorf("billing: country of org %s: %w", orgID, err)
		}
		country = c
	}
	currency, issuerID, err := RouteCurrency(ctx, q, country)
	if err != nil {
		return ComplimentaryQuote{}, err
	}
	at = at.UTC()
	var terms Terms
	acc, err := q.GetBillingAccount(ctx, orgID, currency, AccountKindOrganizer)
	switch {
	case err == nil:
		terms, err = TermsAt(ctx, q, acc.ID, at)
	case errors.Is(err, pgx.ErrNoRows):
		// The account EnsureAccount would create, with its first terms.
		acc = gen.BillingAccountRow{OrgID: orgID, Currency: currency, IssuerID: issuerID, AccountKind: AccountKindOrganizer}
		terms, err = termsWith(ctx, q, acc, gen.BillingAccountTermsRow{
			EffectiveFrom: InitialTermsEffectiveFrom, PriceListCode: StandardPriceListCode, PlanKind: PlanStandard,
			BillingCycle: "monthly", ShortfallPolicy: "invoice_postpaid", IssuerID: issuerID,
			FeeMode: FeeModeOrganizerPays, ComplimentaryBilling: ComplimentaryBillingFree,
		}, at)
	}
	if err != nil {
		return ComplimentaryQuote{}, err
	}
	in := ComplimentaryQuoteInput{Seated: seated, Unseated: unseated, Bulk: bulk}
	if !bulk && terms.ComplimentaryBilling() == ComplimentaryBillingFree && terms.FreeTicketsAnnualLimit() != nil && acc.ID != uuid.Nil {
		in.UsedFree, err = q.CountFreeComplimentaryChargesBefore(ctx, acc.ID, FreeLimitYearStart(at), at, uuid.Max)
		if err != nil {
			return ComplimentaryQuote{}, fmt.Errorf("billing: free complimentary count of account %s: %w", acc.ID, err)
		}
	}
	return QuoteComplimentary(terms, in)
}
