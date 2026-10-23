package hexport

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
)

// salesColumns are the columns of a sales export, in the spec's order
// (§5.8): Заказ;Статус заказа;Дата;Покупатель;E-mail;Телефон;Категория;
// Цена;Валюта;Штрихкод;Статус билета;Вошёл;Время входа;Источник входа;
// Сканер / вход;Канал;Промокод (M2: the first admitted scan of the ticket).
// The event-level file appends Сеанс.
var salesColumns = []csvexport.Column{
	csvexport.ColOrder, csvexport.ColOrderStatus, csvexport.ColDate,
	csvexport.ColBuyer, csvexport.ColEmail, csvexport.ColPhone,
	csvexport.ColCategory, csvexport.ColPrice, csvexport.ColCurrency,
	csvexport.ColBarcode, csvexport.ColTicketStatus, csvexport.ColEntered,
	csvexport.ColEnteredAt, csvexport.ColEnteredSource, csvexport.ColEnteredScanner,
	csvexport.ColChannel, csvexport.ColPromoCode,
}

// HandleSessionSales serves
// GET /v1/organizations/{org_id}/sessions/{session_id}/sales.csv —
// one row per ticket of the session.
func (h *Handler) HandleSessionSales(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	sessionID, ok := uuidParam(w, r, "session_id")
	if !ok {
		return
	}
	ctx := r.Context()
	header, err := h.queries.GetExportSessionHeader(ctx, sessionID, orgID)
	if isNoRows(err) {
		notFound(w, r, "session.not_found", "session not found")
		return
	}
	if err != nil {
		h.internal(w, r, "session", err)
		return
	}
	loc := venueLocation(header.VenueTimezone)
	h.streamSales(w, r, &sessionID, nil, false,
		filename("sales", slugOr(header.EventSlug, header.EventID.String()), time.Now(), loc))
}

// HandleEventSales serves
// GET /v1/organizations/{org_id}/events/{event_id}/sales.csv —
// one row per ticket across every session of the event, plus the session.
func (h *Handler) HandleEventSales(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := uuidParam(w, r, "event_id")
	if !ok {
		return
	}
	header, err := h.queries.GetExportEventHeader(r.Context(), eventID, orgID)
	if isNoRows(err) {
		notFound(w, r, "event.not_found", "event not found")
		return
	}
	if err != nil {
		h.internal(w, r, "event", err)
		return
	}
	// The event's sessions may sit in different venues; the file name takes
	// the server's calendar date, every date column its own venue's zone.
	h.streamSales(w, r, nil, &eventID, true,
		filename("sales", slugOr(header.Slug, header.ID.String()), time.Now(), time.UTC))
}

// streamSales counts, refuses over the cap, then streams the tickets of one
// session or one event in keyset batches of batchSize.
func (h *Handler) streamSales(w http.ResponseWriter, r *http.Request, sessionID, eventID *uuid.UUID, withSession bool, name string) {
	ctx := r.Context()
	total, err := h.queries.CountExportSalesRows(ctx, sessionID, eventID)
	if err != nil {
		h.internal(w, r, "count", err)
		return
	}
	if h.tooManyRows(w, r, total) {
		return
	}
	locale := csvexport.LocaleFromRequest(r)
	cols := salesColumns
	if withSession {
		cols = append(append([]csvexport.Column{}, salesColumns...), csvexport.ColSession)
	}

	s := begin(w, name)
	if err := s.csv.WriteHeader(csvexport.Header(locale, cols...)); err != nil {
		h.abort(ctx, "header", err)
	}
	after := int64(0)
	for {
		rows, err := h.queries.ListExportSalesRows(ctx, sessionID, eventID, after, batchSize)
		if err != nil {
			h.abort(ctx, "rows", err)
		}
		for _, row := range rows {
			if err := s.csv.WriteRow(salesCells(row, withSession)...); err != nil {
				h.abort(ctx, "write", err)
			}
			after = row.SystemTicketID
		}
		if err := s.flush(); err != nil {
			h.abort(ctx, "flush", err)
		}
		if len(rows) < int(batchSize) {
			return
		}
	}
}

// salesCells renders one ticket. The barcode is the stored EAN-13
// credential; a ticket that predates feature #502 and was never backfilled
// has none, and the export derives the retired PlatformCode from its
// system_ticket_id exactly as orderexport does — checksum-valid, never
// minted or persisted.
func salesCells(row gen.ExportSalesRow, withSession bool) []csvexport.Cell {
	loc := venueLocation(row.VenueTimezone)
	barcode := ean13.PlatformCode(row.SystemTicketID)
	if row.Barcode != nil && *row.Barcode != "" {
		barcode = *row.Barcode
	}
	orderDate := row.IssuedAt
	if row.OrderCreatedAt != nil {
		orderDate = *row.OrderCreatedAt
	}
	// A ticket is entered when it carries used_at or an admitted scan (the
	// refund guard's own reading); the entry time is the first admission.
	enteredAt := row.UsedAt
	if enteredAt == nil {
		enteredAt = row.EnteredAt
	}
	entered := enteredAt != nil
	var order csvexport.Cell
	if row.OrderNumber != nil {
		order = csvexport.Number(*row.OrderNumber)
	}
	cells := []csvexport.Cell{
		order,
		csvexport.TextPtr(row.OrderStatus),
		csvexport.DateTime(orderDate, loc),
		csvexport.TextPtr(row.BuyerName),
		csvexport.TextPtr(row.BuyerEmail),
		csvexport.TextPtr(row.BuyerPhone),
		csvexport.TextPtr(row.TierName),
		csvexport.MoneyPtr(row.ItemTotal),
		csvexport.TextPtr(row.Currency),
		csvexport.Text(barcode),
		csvexport.Text(row.TicketStatus),
		csvexport.YesNo(entered),
		csvexport.DateTimePtr(enteredAt, loc),
		csvexport.TextPtr(row.EnteredSource),
		csvexport.Text(scannerGate(row.EnteredScanner, row.EnteredGate)),
		csvexport.TextPtr(row.ChannelName),
		csvexport.TextPtr(row.PromoCode),
	}
	if withSession {
		cells = append(cells, csvexport.DateTime(row.SessionStart, loc))
	}
	return cells
}

// scannerGate joins the scanner's own name and the gate it stood at, each
// only when it exists ("Scanner 3 / Gate 2"). MACS names both; arena's own
// scanner reports the gate alone.
func scannerGate(scanner, gate *string) string {
	var parts []string
	for _, p := range []*string{scanner, gate} {
		if p != nil && strings.TrimSpace(*p) != "" {
			parts = append(parts, strings.TrimSpace(*p))
		}
	}
	return strings.Join(parts, " / ")
}
