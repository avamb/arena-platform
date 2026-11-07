package hexport

// accounting.go — the workbook the organizer's accountant asks for (EC-V2,
// spec 62 §12.2):
//
//	GET /v1/organizations/{org_id}/exports/accounting.xlsx?from=&to=&event_id=&locale=
//
// Three sheets over the sessions that start in the range: per session and
// currency (the very cards of GET .../summary, read through
// horders.LoadOrgSummary), per session, category, price and promo code, and
// one row per ticket with the buyer and the refund. Test orders are in none
// of them. The file holds buyers' names and contacts, so a person downloads it
// and an organization API key does not. Every download is audited.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/horders"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/xlsxexport"
)

// AuditAccountingExport is the audit action of one accounting download.
const AuditAccountingExport = "v1.org.accounting_export.download"

// accountingSheetNames are the tab names by header language.
var accountingSheetNames = map[string][3]string{
	"en": {"By sessions", "By prices", "Tickets"},
	"ru": {"По сеансам", "По ценам", "Билеты"},
	"es": {"Por sesiones", "Por precios", "Entradas"},
}

var (
	accountingSessionCols = []csvexport.Column{
		csvexport.ColEvent, csvexport.ColSession, csvexport.ColCurrency, csvexport.ColOrdersCount,
		csvexport.ColSold, csvexport.ColComplimentary, csvexport.ColRefundedTickets, csvexport.ColActiveTickets,
		csvexport.ColRevenue, csvexport.ColRefundedAmount, csvexport.ColNet,
	}
	accountingPriceCols = []csvexport.Column{
		csvexport.ColEvent, csvexport.ColSession, csvexport.ColCategory, csvexport.ColPrice,
		csvexport.ColCurrency, csvexport.ColPromoCode, csvexport.ColTicketsCount, csvexport.ColDiscount, csvexport.ColRevenue,
	}
	accountingTicketCols = []csvexport.Column{
		csvexport.ColTicket, csvexport.ColOrder, csvexport.ColDate, csvexport.ColEvent, csvexport.ColSession,
		csvexport.ColBuyer, csvexport.ColEmail, csvexport.ColPhone, csvexport.ColCategory,
		csvexport.ColPricePaid, csvexport.ColCurrency, csvexport.ColTicketStatus, csvexport.ColInvitation,
		csvexport.ColRefundAmount, csvexport.ColRefundSettlement, csvexport.ColRefundRef, csvexport.ColTest,
	}
	// Columns that must stay text in the file: identifiers and contacts.
	accountingTextCols = map[csvexport.Column]bool{
		csvexport.ColTicket: true, csvexport.ColOrder: true, csvexport.ColPhone: true, csvexport.ColRefundRef: true,
	}
)

func accountingColumns(locale string, cols []csvexport.Column) []xlsxexport.Column {
	out := make([]xlsxexport.Column, len(cols))
	for i, c := range cols {
		out[i] = xlsxexport.Column{Header: csvexport.Label(locale, c), Text: accountingTextCols[c]}
	}
	return out
}

// HandleAccountingXLSX serves GET .../exports/accounting.xlsx.
func (h *Handler) HandleAccountingXLSX(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	if actor, ok := auth.ActorFromContext(r.Context()); ok && actor.IsService() {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"accounting.api_key_forbidden", "the accounting workbook holds buyers' contacts and is not served to an API key", r))
		return
	}
	f, code, msg := horders.ParseOrgSummaryFilter(r)
	if code != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(code, msg, r))
		return
	}
	ctx := r.Context()
	summary, part, err := horders.LoadOrgSummary(ctx, h.queries, orgID, f, horders.OrgAccountingMaxSessions)
	if err != nil {
		h.internal(w, r, "summary "+part, err)
		return
	}
	if summary.Truncated {
		httputil.WriteJSON(w, http.StatusRequestEntityTooLarge, httputil.ErrorEnvelopeWithDetails(
			"export.too_many_sessions", "the range holds more sessions than one workbook takes; narrow the dates", r,
			map[string]any{"max_sessions": horders.OrgAccountingMaxSessions},
		))
		return
	}
	ids := horders.OrgSummarySessionIDs(summary)
	var tickets []gen.AccountingTicketRow
	if len(ids) > 0 {
		total, err := h.queries.CountAccountingTickets(ctx, ids)
		if err != nil {
			h.internal(w, r, "count", err)
			return
		}
		if h.tooManyRows(w, r, total) {
			return
		}
		after := int64(0)
		for {
			page, err := h.queries.ListAccountingTickets(ctx, ids, after, batchSize)
			if err != nil {
				h.internal(w, r, "tickets", err)
				return
			}
			tickets = append(tickets, page...)
			if len(page) < int(batchSize) {
				break
			}
			after = page[len(page)-1].SystemTicketID
		}
	}

	locale := csvexport.LocaleFromRequest(r)
	names, ok := accountingSheetNames[locale]
	if !ok {
		names = accountingSheetNames[csvexport.DefaultLocale]
	}
	book, err := xlsxexport.BuildBook([]xlsxexport.Sheet{
		{Name: names[0], Columns: accountingColumns(locale, accountingSessionCols), Rows: accountingSessionRows(summary)},
		{Name: names[1], Columns: accountingColumns(locale, accountingPriceCols), Rows: accountingPriceRows(summary)},
		{Name: names[2], Columns: accountingColumns(locale, accountingTicketCols), Rows: accountingTicketRows(tickets)},
	})
	if err != nil {
		h.internal(w, r, "workbook", err)
		return
	}
	h.auditAccounting(r, orgID, summary, len(tickets), locale)

	w.Header().Set("Content-Type", xlsxexport.ContentType)
	w.Header().Set("X-Row-Count", strconv.Itoa(len(tickets)))
	w.Header().Set("Content-Disposition", `attachment; filename="`+accountingFileName(f, time.Now())+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(book)
}

// accountingFileName is accounting_<from>_<to>.xlsx, or accounting_<date>.xlsx
// for an unbounded range.
func accountingFileName(f horders.OrgSummaryFilter, now time.Time) string {
	const layout = "2006-01-02" // allow:timeformat: a calendar date in a file name
	name := "accounting_" + now.UTC().Format(layout)
	if f.From != nil || f.To != nil {
		from, to := "start", "end"
		if f.From != nil {
			from = f.From.UTC().Format(layout)
		}
		if f.To != nil {
			// `to` is exclusive internally; the name carries the last day.
			to = f.To.UTC().Add(-time.Second).Format(layout)
		}
		name = "accounting_" + from + "_" + to
	}
	return name + ".xlsx"
}

func sessionLoc(tz *string) *time.Location { return venueLocation(tz) }

func accountingSessionRows(s horders.OrgSummary) [][]csvexport.Cell {
	rows := [][]csvexport.Cell{}
	for _, sess := range s.Sessions {
		loc := sessionLoc(sess.VenueTimezone)
		for _, c := range sess.Cards {
			rows = append(rows, []csvexport.Cell{
				csvexport.Text(sess.EventName), csvexport.DateTime(sess.Start, loc), csvexport.Text(c.Currency),
				csvexport.Number(c.Orders), csvexport.Number(c.Sold), csvexport.Number(c.Complimentary),
				csvexport.Number(c.RefundedTickets), csvexport.Number(c.ActiveTickets),
				csvexport.Money(c.Revenue), csvexport.Money(c.Refunded), csvexport.Money(c.Net),
			})
		}
	}
	return rows
}

func accountingPriceRows(s horders.OrgSummary) [][]csvexport.Cell {
	rows := [][]csvexport.Cell{}
	for _, sess := range s.Sessions {
		loc := sessionLoc(sess.VenueTimezone)
		for _, c := range sess.Cards {
			for _, b := range c.Breakdown {
				promo := ""
				if b.PromoCode != nil {
					promo = *b.PromoCode
				}
				rows = append(rows, []csvexport.Cell{
					csvexport.Text(sess.EventName), csvexport.DateTime(sess.Start, loc), csvexport.Text(b.TierName),
					csvexport.Money(b.UnitPrice), csvexport.Text(c.Currency), csvexport.Text(promo),
					csvexport.Number(b.Tickets), csvexport.Money(b.Discount), csvexport.Money(b.Revenue),
				})
			}
		}
	}
	return rows
}

func accountingTicketRows(tickets []gen.AccountingTicketRow) [][]csvexport.Cell {
	rows := make([][]csvexport.Cell, 0, len(tickets))
	for _, t := range tickets {
		loc := venueLocation(t.VenueTimezone)
		var order csvexport.Cell
		if t.OrderNumber != nil {
			order = csvexport.Number(*t.OrderNumber)
		}
		rows = append(rows, []csvexport.Cell{
			csvexport.Number(t.SystemTicketID), order, csvexport.DateTime(t.IssuedAt, loc),
			csvexport.Text(t.EventName), csvexport.DateTime(t.SessionStart, loc),
			csvexport.TextPtr(t.BuyerName), csvexport.TextPtr(t.BuyerEmail), csvexport.TextPtr(t.BuyerPhone),
			csvexport.TextPtr(t.TierName), csvexport.MoneyPtr(t.ItemTotal), csvexport.Text(t.Currency),
			csvexport.Text(t.TicketStatus), csvexport.YesNo(t.Complimentary),
			csvexport.MoneyPtr(t.RefundAmount), csvexport.TextPtr(t.RefundSettlement), csvexport.TextPtr(t.RefundRef),
			// Test orders are left out of the workbook, so this is always
			// "no"; the column keeps the layout of the CSV exports.
			csvexport.YesNo(false),
		})
	}
	return rows
}

func (h *Handler) auditAccounting(r *http.Request, orgID uuid.UUID, s horders.OrgSummary, tickets int, locale string) {
	if h.audit == nil {
		return
	}
	ctx := r.Context()
	actor, _ := auth.ActorFromContext(ctx)
	actorID := ""
	if _, err := uuid.Parse(actor.ID); err == nil {
		actorID = actor.ID // audit_events.actor_id is a bare uuid
	}
	q := r.URL.Query()
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actorID,
		Action:       AuditAccountingExport,
		ResourceType: "organization",
		ResourceID:   orgID.String(),
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata: map[string]any{
			"from":     strings.TrimSpace(q.Get("from")),
			"to":       strings.TrimSpace(q.Get("to")),
			"event_id": strings.TrimSpace(q.Get("event_id")),
			"sessions": len(s.Sessions),
			"tickets":  tickets,
			"locale":   locale,
		},
	}
	if err := h.audit.Write(ctx, ev); err != nil {
		h.logger.Error("hexport: accounting audit write failed", "error", err)
	}
}
