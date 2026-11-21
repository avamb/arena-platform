// reissue_letter.go — the letter with replacement tickets (spec 63).
//
// A letter that carries a ticket of an open reissue explains why new tickets
// came (templates.Data.Reissue, decision R-19). Once the relay accepted it,
// the order's buyer e-mail and customer move to the new address
// (ticketreissue.AfterLetter, decision R-2); a refused send marks the reissue
// 'send_failed' instead and the address stays.
package delivery

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ticketreissue"
)

// reissueDataFor answers the explanation block of the letter, nil for an
// ordinary letter. Best effort: a failed lookup sends the ordinary letter.
func reissueDataFor(ctx context.Context, opts HandlerOptions, logger *slog.Logger, kind string, ticketID uuid.UUID) *templates.ReissueData {
	if (kind != templates.TemplateKindTicket && kind != templates.TemplateKindInvitation) || opts.DeliveryJobQueries == nil {
		return nil
	}
	f, err := ticketreissue.FactsForLetter(ctx, opts.DeliveryJobQueries.DB(), ticketID)
	if err != nil {
		logger.Warn("delivery: reissue lookup failed; sending an ordinary letter",
			slog.String("ticket_id", ticketID.String()), slog.String("error", err.Error()))
		return nil
	}
	if !f.Found {
		return nil
	}
	d := &templates.ReissueData{Typo: f.Typo()}
	if f.OrderNumber > 0 {
		d.OrderNumber = strconv.FormatInt(f.OrderNumber, 10)
	}
	return d
}

// afterReissueLetter tells the reissue journal how the relay answered. A
// failure only logs: the letter itself is already decided.
func afterReissueLetter(ctx context.Context, opts HandlerOptions, logger *slog.Logger, members []*letterMember, recipient string, sent bool) {
	if opts.DeliveryJobQueries == nil || len(members) == 0 {
		return
	}
	db, ok := opts.DeliveryJobQueries.DB().(ticketreissue.TxBeginner)
	if !ok {
		return
	}
	ids := make([]uuid.UUID, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.ticketID)
	}
	if err := ticketreissue.AfterLetter(ctx, db, logger, ids, recipient, sent); err != nil {
		logger.Error("delivery: reissue journal update failed after the letter",
			slog.Bool("sent", sent), slog.String("error", err.Error()))
	}
}
