//go:build integration

// engine_referral_cashback_integration_test.go — a "refer a friend" cashback
// (spec 52) is a money-only partial refund that a scan of the ticket does not
// stop (owner decision R-RF3), while every other refund of a scanned ticket
// still is.
package refunds_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

func TestEngine_ReferralCashbackIgnoresAScanOthersDoNot(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true}
	e := f.engine(m, false)
	ctx := context.Background()
	f.exec(t, `UPDATE tickets SET used_at = now() WHERE id = $1`, f.tickets[0])
	amount := int64(300)
	item := refunds.Item{TicketID: f.tickets[0], Amount: &amount}

	// An ordinary refund of the scanned ticket is still refused.
	plain := f.batch("k-plain", false, item)
	if _, err := e.CreateBatch(ctx, plain); refusalCode(err) != refunds.CodeTicketScanned {
		t.Fatalf("ordinary money-only refund of a scanned ticket: err = %v; want %s", err, refunds.CodeTicketScanned)
	}
	// So is a referral refund that would cancel the ticket.
	cancelling := f.batch("k-cancel", true, item)
	cancelling.Origin = refunds.OriginReferralCashback
	if _, err := e.CreateBatch(ctx, cancelling); refusalCode(err) != refunds.CodeTicketScanned {
		t.Fatalf("referral refund that cancels a scanned ticket: err = %v; want %s", err, refunds.CodeTicketScanned)
	}
	if m.callCount() != 0 {
		t.Fatalf("%d provider calls after two refusals; want 0", m.callCount())
	}

	// The cashback itself goes through.
	cashback := f.batch("k-cashback", false, item)
	cashback.Origin = refunds.OriginReferralCashback
	cashback.NotifyBuyer = false
	res, err := e.CreateBatch(ctx, cashback)
	if err != nil {
		t.Fatalf("cashback of a scanned ticket: %v", err)
	}
	r := res.Refunds[0]
	if r.State != refunds.StateSucceeded || r.Amount != 300 || r.CancelTicket {
		t.Fatalf("refund = %+v; want succeeded 300, ticket kept", r)
	}
	var origin string
	if err := f.pool.QueryRow(ctx, `SELECT origin FROM refunds WHERE id = $1`, r.ID).Scan(&origin); err != nil || origin != refunds.OriginReferralCashback {
		t.Fatalf("stored origin = %q (%v); want %s", origin, err, refunds.OriginReferralCashback)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" {
		t.Fatalf("ticket is %s; the cashback must not cancel it", st)
	}
	// It spent no superadmin exception either (there was none to spend).
	if n := f.count(t, `SELECT count(*) FROM refund_scan_exceptions sx JOIN tickets t ON t.id = sx.ticket_id
		JOIN orders o ON o.id = t.order_id WHERE o.org_id = $1`); n != 0 {
		t.Fatalf("%d scan exceptions exist", n)
	}
}

func TestEngine_UnknownRefundOriginIsRefused(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true}
	in := f.batch("k-origin", false, refunds.Item{TicketID: f.tickets[0]})
	in.Origin = "provider_dashboard" // only the engine's own sweep writes that
	_, err := f.engine(m, false).CreateBatch(context.Background(), in)
	var e *refunds.Error
	if !errors.As(err, &e) || e.Status != http.StatusBadRequest || e.Code != refunds.CodeInvalidRequest {
		t.Fatalf("err = %v; want 400 %s", err, refunds.CodeInvalidRequest)
	}
}
