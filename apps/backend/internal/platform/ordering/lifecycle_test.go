package ordering

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// storeWithOrder seeds one order in the given status so the transition tests
// can start from an arbitrary point in the lifecycle.
func storeWithOrder(status string) (*fakeStore, gen.OrderRow) {
	f := newFakeStore()
	row := gen.OrderRow{
		ID:     uuid.New(),
		OrgID:  uuid.New(),
		Status: status,
	}
	f.orders[row.ID] = row
	return f, row
}

func TestMarkPaid_StampsPaidAtAndAudits(t *testing.T) {
	f, row := storeWithOrder(StatusPendingPayment)
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)

	got, err := MarkPaid(context.Background(), f, PaidInput{
		OrderID: row.ID,
		OrgID:   row.OrgID,
		Actor:   "webhook:stripe",
		Payload: map[string]any{"provider_ref": "pi_123"},
		Now:     now,
	})
	if err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if got.Status != StatusPaid {
		t.Fatalf("status = %s, want %s", got.Status, StatusPaid)
	}
	if got.PaidAt == nil || !got.PaidAt.Equal(now) {
		t.Fatalf("paid_at = %v, want %v", got.PaidAt, now)
	}
	if len(f.events) != 1 || f.events[0].Type != EventPaid || f.events[0].Actor != "webhook:stripe" {
		t.Fatalf("events = %+v, want one paid event by the webhook", f.events)
	}
	if !strings.Contains(string(f.events[0].Payload), "pi_123") {
		t.Fatalf("payload = %s, want the provider reference", f.events[0].Payload)
	}
}

// A payment webhook that is retried must not fail and must not double-audit.
func TestMarkPaid_IsIdempotent(t *testing.T) {
	f, row := storeWithOrder(StatusPaid)

	got, err := MarkPaid(context.Background(), f, PaidInput{OrderID: row.ID, OrgID: row.OrgID})
	if err != nil {
		t.Fatalf("MarkPaid on an already-paid order: %v", err)
	}
	if got.Status != StatusPaid {
		t.Fatalf("status = %s, want it left at paid", got.Status)
	}
	if len(f.events) != 0 {
		t.Fatalf("wrote %d events replaying a paid order", len(f.events))
	}
}

// A payment landing after the order died is a human decision, not an automatic
// resurrection.
func TestMarkPaid_RefusesDeadOrder(t *testing.T) {
	for _, status := range []string{StatusCancelled, StatusExpired} {
		f, row := storeWithOrder(status)
		_, err := MarkPaid(context.Background(), f, PaidInput{OrderID: row.ID, OrgID: row.OrgID})
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("MarkPaid from %s: err = %v, want ErrInvalidTransition", status, err)
		}
	}
}

func TestCancel_StampsCancelledAtWithReason(t *testing.T) {
	f, row := storeWithOrder(StatusPendingPayment)
	now := time.Date(2026, 9, 5, 11, 0, 0, 0, time.UTC)

	got, err := Cancel(context.Background(), f, CancelInput{
		OrderID: row.ID,
		OrgID:   row.OrgID,
		Reason:  "buyer_abandoned",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got.Status != StatusCancelled || got.CancelledAt == nil || !got.CancelledAt.Equal(now) {
		t.Fatalf("order = %+v, want cancelled at %v", got, now)
	}
	if len(f.events) != 1 || f.events[0].Type != EventCancelled {
		t.Fatalf("events = %+v, want one cancelled event", f.events)
	}
	if !strings.Contains(string(f.events[0].Payload), "buyer_abandoned") {
		t.Fatalf("payload = %s, want the reason", f.events[0].Payload)
	}
	// An unattributed cancel is still attributable in the audit log.
	if f.events[0].Actor != ActorSystem {
		t.Fatalf("actor = %q, want %q by default", f.events[0].Actor, ActorSystem)
	}
}

// Unwinding money belongs to the refund path; Cancel must not touch a paid order.
func TestCancel_RefusesPaidOrder(t *testing.T) {
	f, row := storeWithOrder(StatusPaid)
	_, err := Cancel(context.Background(), f, CancelInput{OrderID: row.ID, OrgID: row.OrgID})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

func TestCancel_IsIdempotent(t *testing.T) {
	f, row := storeWithOrder(StatusCancelled)
	if _, err := Cancel(context.Background(), f, CancelInput{OrderID: row.ID, OrgID: row.OrgID}); err != nil {
		t.Fatalf("re-cancel: %v", err)
	}
	if len(f.events) != 0 {
		t.Fatalf("wrote %d events re-cancelling", len(f.events))
	}
}

// W1-B7c (#506): a migrated WordPress shop was told about this order at
// order.paid, so it has to be told to void it. The notification fires only
// after the transition is actually recorded.
func TestCancel_NotifiesSubscribersOfARealTransition(t *testing.T) {
	f, row := storeWithOrder(StatusPendingPayment)
	row.SystemID = 4400123
	row.ChannelID = uuid.New()
	f.orders[row.ID] = row

	var published []gen.OrderRow
	got, err := Cancel(context.Background(), f, CancelInput{
		OrderID: row.ID,
		OrgID:   row.OrgID,
		Reason:  "operator_cancelled",
		Publish: func(_ context.Context, o gen.OrderRow) { published = append(published, o) },
	})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(published) != 1 {
		t.Fatalf("published %d notifications, want exactly 1", len(published))
	}
	// The publisher must see the POST-transition row: a site that re-reads on
	// notification would otherwise be handed a still-pending order.
	if published[0].Status != StatusCancelled {
		t.Fatalf("published status = %s, want %s", published[0].Status, StatusCancelled)
	}
	if published[0].ID != got.ID {
		t.Fatalf("published order %s, want the cancelled one %s", published[0].ID, got.ID)
	}

	payload := BuildOrderCancelledPayload(published[0], "operator_cancelled")
	for key, want := range map[string]any{
		"order_id":   row.ID.String(),
		"org_id":     row.OrgID.String(),
		"channel_id": row.ChannelID.String(),
		"system_id":  int64(4400123),
		"reason":     "operator_cancelled",
	} {
		if payload[key] != want {
			t.Errorf("payload[%q] = %v, want %v", key, payload[key], want)
		}
	}
}

// A retried CANCEL_ORDER must not void the order at the site twice — the
// idempotent no-op returns before the publisher is reached.
func TestCancel_DoesNotNotifyOnAReplay(t *testing.T) {
	f, row := storeWithOrder(StatusCancelled)
	calls := 0
	if _, err := Cancel(context.Background(), f, CancelInput{
		OrderID: row.ID,
		OrgID:   row.OrgID,
		Publish: func(context.Context, gen.OrderRow) { calls++ },
	}); err != nil {
		t.Fatalf("re-cancel: %v", err)
	}
	if calls != 0 {
		t.Fatalf("notified %d times replaying a cancellation, want 0", calls)
	}
}

// A refused transition must not notify either: nothing changed.
func TestCancel_DoesNotNotifyWhenTheTransitionIsRefused(t *testing.T) {
	f, row := storeWithOrder(StatusPaid)
	calls := 0
	_, err := Cancel(context.Background(), f, CancelInput{
		OrderID: row.ID,
		OrgID:   row.OrgID,
		Publish: func(context.Context, gen.OrderRow) { calls++ },
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
	if calls != 0 {
		t.Fatalf("notified %d times on a refused transition, want 0", calls)
	}
}

// 'expired' is the reason and cancelled_at is the "stopped being live at"
// timestamp the admin list sorts on, so Expire stamps both.
func TestExpire_StampsCancelledAtAndEmitsHoldExpired(t *testing.T) {
	f, row := storeWithOrder(StatusPendingPayment)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	got, err := Expire(context.Background(), f, ExpireInput{OrderID: row.ID, OrgID: row.OrgID, Now: now})
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if got.Status != StatusExpired || got.CancelledAt == nil || !got.CancelledAt.Equal(now) {
		t.Fatalf("order = %+v, want expired at %v", got, now)
	}
	if len(f.events) != 1 || f.events[0].Type != EventHoldExpired {
		t.Fatalf("events = %+v, want one hold_expired event", f.events)
	}
}

func TestExpire_RefusesPaidOrder(t *testing.T) {
	f, row := storeWithOrder(StatusPaid)
	_, err := Expire(context.Background(), f, ExpireInput{OrderID: row.ID, OrgID: row.OrgID})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

func TestExpire_IsIdempotent(t *testing.T) {
	f, row := storeWithOrder(StatusExpired)
	if _, err := Expire(context.Background(), f, ExpireInput{OrderID: row.ID, OrgID: row.OrgID}); err != nil {
		t.Fatalf("re-expire: %v", err)
	}
	if len(f.events) != 0 {
		t.Fatalf("wrote %d events re-expiring", len(f.events))
	}
}

// ReviveForPayment is the money-safety fix for a late PAY_ORDER on an order
// the expire sweep already closed: the buyer's WooCommerce charge already
// happened, so an expired order whose hold can still be secured must be
// payable again rather than answering -1 forever.
func TestReviveForPayment_MovesExpiredBackToPendingAndAudits(t *testing.T) {
	f, row := storeWithOrder(StatusExpired)
	row.CancelledAt = ptr(time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC))
	f.orders[row.ID] = row

	got, err := ReviveForPayment(context.Background(), f, ReviveInput{
		OrderID: row.ID, OrgID: row.OrgID, Actor: "gateway:1271",
	})
	if err != nil {
		t.Fatalf("ReviveForPayment: %v", err)
	}
	if got.Status != StatusPendingPayment {
		t.Fatalf("status = %s, want %s", got.Status, StatusPendingPayment)
	}
	if got.CancelledAt != nil {
		t.Fatalf("cancelled_at = %v, want nil — a revived order must not still look expired", got.CancelledAt)
	}
	if len(f.events) != 1 || f.events[0].Type != EventRevivedForPayment || f.events[0].Actor != "gateway:1271" {
		t.Fatalf("events = %+v, want one revived_for_payment event by the gateway actor", f.events)
	}
}

// A retried PAY_ORDER whose first attempt already revived (and paid) the
// order must not fail: MarkPaid handles the terminal idempotency, but the
// revival step itself must also treat pending_payment/paid as a harmless
// no-op rather than erroring before MarkPaid gets a chance to run.
func TestReviveForPayment_IsANoOpOnPendingOrPaid(t *testing.T) {
	for _, status := range []string{StatusPendingPayment, StatusPaid} {
		f, row := storeWithOrder(status)
		got, err := ReviveForPayment(context.Background(), f, ReviveInput{OrderID: row.ID, OrgID: row.OrgID})
		if err != nil {
			t.Fatalf("ReviveForPayment from %s: %v", status, err)
		}
		if got.Status != status {
			t.Fatalf("ReviveForPayment from %s: status = %s, want unchanged %s", status, got.Status, status)
		}
		if len(f.events) != 0 {
			t.Fatalf("ReviveForPayment from %s: wrote %d events, want 0", status, len(f.events))
		}
	}
}

// A genuinely dead order (cancelled, refunded, parked in manual_review by an
// earlier failed attempt) must never be silently resurrected — an operator or
// the site's own retry loop, not this function, decides what happens next.
func TestReviveForPayment_RefusesOtherStatuses(t *testing.T) {
	for _, status := range []string{StatusCancelled, "manual_review", "refunded", "abandoned"} {
		f, row := storeWithOrder(status)
		_, err := ReviveForPayment(context.Background(), f, ReviveInput{OrderID: row.ID, OrgID: row.OrgID})
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("ReviveForPayment from %s: err = %v, want ErrInvalidTransition", status, err)
		}
		if len(f.events) != 0 {
			t.Fatalf("ReviveForPayment from %s: wrote %d events on a refused revive", status, len(f.events))
		}
	}
}

// The forever-(-1)-loop defect found in review: order A expired, the same
// customer re-reserved for the same session, and CREATE_ORDER_EXT already
// minted a fresh pending_payment order B before A's late PAY_ORDER arrived.
// Reviving A collides with orders_one_pending_per_customer_session_uq
// (SQLSTATE 23505) — ReviveForPayment must surface this as the distinguishable
// ErrOpenOrderConflict, NOT a generic error, so the caller can park A in
// manual_review (money-safety) instead of retrying an identical failure
// forever.
func TestReviveForPayment_OpenOrderConflictIsDistinguishable(t *testing.T) {
	f, row := storeWithOrder(StatusExpired)
	f.reviveErrOn[row.ID] = &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "orders_one_pending_per_customer_session_uq",
	}

	_, err := ReviveForPayment(context.Background(), f, ReviveInput{OrderID: row.ID, OrgID: row.OrgID})
	if !errors.Is(err, ErrOpenOrderConflict) {
		t.Fatalf("err = %v, want ErrOpenOrderConflict", err)
	}
	if len(f.events) != 0 {
		t.Fatalf("wrote %d events on a conflicting revive", len(f.events))
	}
}

// A unique violation on some OTHER constraint (or one whose name the driver
// did not surface) must not be silently swallowed as ErrOpenOrderConflict —
// only when the constraint name is present AND matches, or is absent
// entirely, is it treated as this specific collision.
func TestReviveForPayment_OtherUniqueViolationIsNotMisclassified(t *testing.T) {
	f, row := storeWithOrder(StatusExpired)
	f.reviveErrOn[row.ID] = &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "some_other_unrelated_uq",
	}

	_, err := ReviveForPayment(context.Background(), f, ReviveInput{OrderID: row.ID, OrgID: row.OrgID})
	if errors.Is(err, ErrOpenOrderConflict) {
		t.Fatalf("err = %v, misclassified an unrelated unique violation as ErrOpenOrderConflict", err)
	}
	if err == nil {
		t.Fatal("ReviveForPayment: want an error for an unrelated unique violation, got nil")
	}
}

func TestFindOpenOrder_ReturnsTheOpenOrder(t *testing.T) {
	f := newFakeStore()
	open := gen.OrderRow{ID: uuid.New(), Status: StatusPendingPayment}
	f.openOrder = &open

	got, err := FindOpenOrder(context.Background(), f, uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("FindOpenOrder: %v", err)
	}
	if got.ID != open.ID {
		t.Fatalf("id = %s, want %s", got.ID, open.ID)
	}
}

// "No open order" is the common case for a first-time buyer, so it must be a
// branchable sentinel rather than a leaked pgx.ErrNoRows.
func TestFindOpenOrder_NoRowsBecomesErrNoOpenOrder(t *testing.T) {
	f := newFakeStore()
	f.openErr = pgx.ErrNoRows

	_, err := FindOpenOrder(context.Background(), f, uuid.New(), uuid.New())
	if !errors.Is(err, ErrNoOpenOrder) {
		t.Fatalf("err = %v, want ErrNoOpenOrder", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("pgx.ErrNoRows leaked out of the ordering package")
	}
}

func TestFindOpenOrder_PropagatesRealErrors(t *testing.T) {
	f := newFakeStore()
	sentinel := errors.New("connection reset")
	f.openErr = sentinel

	_, err := FindOpenOrder(context.Background(), f, uuid.New(), uuid.New())
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the underlying failure", err)
	}
	if errors.Is(err, ErrNoOpenOrder) {
		t.Fatal("a transport failure was reported as 'no open order'")
	}
}
