package shop

// fulfillment.go — the delivery steps of a shop order (spec
// 08_architecture/61 section 7b, migration 0199).
//
// shop_orders.fulfillment_step is the parcel's progress, apart from the money
// status. SetFulfillmentStep is the ONE writer every surface goes through: the
// event center's route, the Telegram bot and the delivery group's buttons. It
// runs under the order's row lock, so two presses at the same moment cannot
// set a step twice or queue two letters.
//
//	waiting -> packed -> handed_over -> delivered
//	waiting, packed, handed_over -> failed
//	waiting -> handed_over (packing is not a required step)
//	failed -> handed_over (another try)
//
// handed_over is what "ship" always was: status shipped, shipped_at, the
// tracking note and the buyer's "shipped" letter, once. delivered queues the
// "delivered" letter, also once per order. A refunded, cancelled or expired
// order is refused ("do not ship"), and so is an order not paid yet. An order
// parked in manual_review may only be handed over (the operator found the
// goods: they are taken off the shelf now, the old ship path).

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Delivery steps (shop_orders.fulfillment_step).
const (
	StepWaiting    = "waiting"
	StepPacked     = "packed"
	StepHandedOver = "handed_over"
	StepDelivered  = "delivered"
	StepFailed     = "failed"
)

// Where a step was set (shop_order_events.via).
const (
	ViaDeliveryGroup  = "delivery_group"
	ViaTelegramBot    = "telegram_bot"
	ViaAdminWeb       = "admin_web"
	ViaEventCenterWeb = "event_center_web"
	ViaAPI            = "api"
	ViaSystem         = "system"
)

// EventOrderFulfillmentChanged is written on every step change, whoever made
// it: the sales bot edits the delivery card from it.
const EventOrderFulfillmentChanged = "v1.shop.order.fulfillment_changed"

// MaxReasonLen bounds the reason of a failed delivery, in characters.
const MaxReasonLen = 500

// The reasons of "not delivered" that the delivery group picks with a button
// (V-18). Any other text is accepted from the event center and the bot.
const (
	ReasonNoAnswer     = "no_answer"
	ReasonNotHome      = "not_home"
	ReasonWrongAddress = "wrong_address"
	ReasonRefused      = "refused"
)

var fulfillmentSteps = map[string]bool{
	StepWaiting: true, StepPacked: true, StepHandedOver: true, StepDelivered: true, StepFailed: true,
}

var fulfillmentVias = map[string]bool{
	ViaDeliveryGroup: true, ViaTelegramBot: true, ViaAdminWeb: true, ViaEventCenterWeb: true, ViaAPI: true, ViaSystem: true,
}

// IsFulfillmentStep reports whether s is a delivery step.
func IsFulfillmentStep(s string) bool { return fulfillmentSteps[s] }

// fulfillmentNext is the transition table: from a step, the steps a person
// may set next.
var fulfillmentNext = map[string][]string{
	StepWaiting:    {StepPacked, StepHandedOver, StepFailed},
	StepPacked:     {StepHandedOver, StepFailed},
	StepHandedOver: {StepDelivered, StepFailed},
	StepFailed:     {StepHandedOver},
	StepDelivered:  nil,
}

// NextSteps lists the steps that may follow from (nil for delivered or an
// unknown step). The delivery card's buttons are these.
func NextSteps(from string) []string {
	return append([]string(nil), fulfillmentNext[from]...)
}

// CanMove reports whether a person may move an order from one step to another.
func CanMove(from, to string) bool {
	for _, s := range fulfillmentNext[from] {
		if s == to {
			return true
		}
	}
	return false
}

// CurrentStep is the delivery step of an order as a person reads it: the
// stored one, else handed_over for an order shipped before steps existed and
// waiting for a paid one. "" when the order has nothing to ship.
func CurrentStep(o gen.ShopOrderRow) string {
	if o.DeliveryKind == DeliveryNone {
		return ""
	}
	if o.FulfillmentStep != nil && *o.FulfillmentStep != "" {
		return *o.FulfillmentStep
	}
	if o.ShippedAt != nil {
		return StepHandedOver
	}
	return StepWaiting
}

// OrderClosed reports whether nothing may be sent any more: the money went
// back in full, or the order was never paid and closed.
func OrderClosed(o gen.ShopOrderRow) bool {
	switch o.Status {
	case StatusRefunded, StatusCancelled, StatusExpired:
		return true
	}
	return false
}

// StepInput is what a step change carries besides the step.
type StepInput struct {
	// Tracking is the shipment number of handed_over; nil keeps the stored
	// note (a repeated handed_over with another number replaces it).
	Tracking *string
	// Reason is why a delivery failed (a code of the delivery group's buttons
	// or a person's text). Note is a free remark.
	Reason string
	Note   string
	// The actor: a platform user, or a delivery group member's Telegram
	// account. Name is what the history and the card show.
	ActorUserID     *uuid.UUID
	ActorTelegramID *int64
	ActorName       string
	// Via is where the step was set (ViaDeliveryGroup, ViaAdminWeb, ...).
	Via string
}

// StepResult is what a step change did.
type StepResult struct {
	Order gen.ShopOrderRow
	// Changed is false when the order already stood at that step and nothing
	// was written (a repeated press).
	Changed bool
	// Previous is the step before the change.
	Previous string
	// Event is the history row written (nil when nothing changed).
	Event *gen.ShopOrderEventRow
}

// Refusals of a step change.
func errFulfillmentClosed(o gen.ShopOrderRow) *Error {
	e := newErr(http.StatusConflict, "shop.fulfillment_order_closed",
		"this order was refunded or cancelled: do not ship it (status "+o.Status+")")
	e.Details = map[string]any{"status": o.Status, "fulfillment_step": CurrentStep(o)}
	return e
}

func errFulfillmentMove(from, to string) *Error {
	e := newErr(http.StatusConflict, "shop.fulfillment_invalid_step",
		"the order is at step "+from+" and cannot move to "+to)
	e.Details = map[string]any{"fulfillment_step": from, "requested_step": to, "allowed": NextSteps(from)}
	return e
}

// validateStepInput checks the step, the channel and the texts.
func validateStepInput(step string, in *StepInput) error {
	if !fulfillmentSteps[step] || step == StepWaiting {
		return newErr(http.StatusBadRequest, "shop.invalid_fulfillment_step",
			"step must be packed, handed_over, delivered or failed")
	}
	if in.Via == "" {
		in.Via = ViaAPI
	}
	if !fulfillmentVias[in.Via] {
		return newErr(http.StatusBadRequest, "shop.invalid_fulfillment_step", "unknown channel of the step")
	}
	in.Reason = strings.TrimSpace(in.Reason)
	in.Note = strings.TrimSpace(in.Note)
	in.ActorName = truncateRunes(strings.TrimSpace(in.ActorName), 200)
	if in.Tracking != nil {
		t := strings.TrimSpace(*in.Tracking)
		in.Tracking = &t
		if !textOK(t, MaxNoteLen) {
			return invalid("shop.text_too_long", "tracking is limited to 2000 characters")
		}
	}
	if !textOK(in.Reason, MaxReasonLen) {
		return invalid("shop.text_too_long", "reason is limited to 500 characters")
	}
	if !textOK(in.Note, MaxNoteLen) {
		return invalid("shop.text_too_long", "note is limited to 2000 characters")
	}
	if step == StepFailed && in.Reason == "" {
		return newErr(http.StatusBadRequest, "shop.fulfillment_reason_required", "say why the order was not delivered")
	}
	return nil
}

// SetFulfillmentStep moves one order of orgID to step inside tx, under the
// order's row lock. Every surface goes through it (the event center, the bot,
// the delivery group). It writes the step, a shop_order_events row and the
// outbox event v1.shop.order.fulfillment_changed in tx, and for handed_over
// and delivered queues the buyer's letter (once per order).
func (s *Service) SetFulfillmentStep(ctx context.Context, tx pgx.Tx, orgID, orderID uuid.UUID, step string, in StepInput) (StepResult, error) {
	if err := validateStepInput(step, &in); err != nil {
		return StepResult{}, err
	}
	q := gen.New(tx)
	o, err := q.GetShopOrderForUpdate(ctx, orderID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return StepResult{}, ErrOrderNotFound
	}
	if err != nil {
		return StepResult{}, err
	}
	if o.Source == OrderSourceShopify {
		return StepResult{}, ErrOrderHistoric
	}
	if o.DeliveryKind == DeliveryNone {
		// Digital or service goods: there is no parcel, no address and no
		// letter, so a step would only write a false event (SA-32).
		return StepResult{}, newErr(http.StatusConflict, "shop.order_not_shippable",
			"this order has nothing to ship: it holds only digital or service goods")
	}
	switch o.Status {
	case StatusRefunded, StatusCancelled, StatusExpired:
		return StepResult{}, errFulfillmentClosed(o)
	case StatusPendingPayment:
		return StepResult{}, newErr(http.StatusConflict, "shop.fulfillment_order_unpaid",
			"this order is not paid yet: there is nothing to ship")
	case StatusManualReview:
		if step != StepHandedOver {
			return StepResult{}, newErr(http.StatusConflict, "shop.fulfillment_order_in_review",
				"this order waits for a decision in the event center; it can only be shipped or refunded")
		}
	}
	from := CurrentStep(o)
	if o.Status == StatusManualReview {
		from = StepWaiting
	}
	res := StepResult{Order: o, Previous: from}

	if step == from {
		// A repeated press. Only a new shipment number of handed_over is news.
		if step != StepHandedOver || in.Tracking == nil || *in.Tracking == o.TrackingNote {
			return res, nil
		}
		out, err := q.SetShopOrderTracking(ctx, o.ID, *in.Tracking)
		if err != nil {
			return StepResult{}, err
		}
		return s.recordStep(ctx, tx, q, out, from, step, in)
	}
	if !CanMove(from, step) {
		return StepResult{}, errFulfillmentMove(from, step)
	}

	switch step {
	case StepHandedOver:
		if _, err := s.handOverTx(ctx, tx, q, o, in.Tracking); err != nil {
			return StepResult{}, err
		}
	case StepDelivered:
		had, err := q.ShopOrderHadStep(ctx, o.ID, StepDelivered)
		if err != nil {
			return StepResult{}, err
		}
		if !had {
			// Delivered again after a failed try and a new hand-over: the
			// buyer heard "delivered" already (V-17, once per order).
			s.enqueueOrderEmailTx(ctx, tx, EmailDelivered, o)
		}
	}
	out, err := q.SetShopOrderFulfillmentStep(ctx, o.ID, step)
	if err != nil {
		return StepResult{}, err
	}
	return s.recordStep(ctx, tx, q, out, from, step, in)
}

// recordStep writes the history row and the outbox event of a change.
func (s *Service) recordStep(ctx context.Context, tx pgx.Tx, q *gen.Queries, o gen.ShopOrderRow, from, step string, in StepInput) (StepResult, error) {
	ev := gen.ShopOrderEventRow{
		OrgID: o.OrgID, OrderID: o.ID, Step: step, Reason: in.Reason, Note: in.Note,
		ActorUserID: in.ActorUserID, ActorTelegramID: in.ActorTelegramID, ActorName: in.ActorName, Via: in.Via,
	}
	if from != "" {
		f := from
		ev.FromStep = &f
	}
	if step == StepHandedOver {
		ev.Tracking = o.TrackingNote
	}
	row, err := q.InsertShopOrderEvent(ctx, ev)
	if err != nil {
		return StepResult{}, err
	}
	extra := map[string]any{
		"fulfillment_step": step, "previous_step": from, "event_id": row.ID.String(),
		"via": in.Via, "actor_name": in.ActorName,
	}
	if step == StepFailed {
		extra["reason"] = in.Reason
	}
	if err := s.appendEvent(ctx, tx, EventOrderFulfillmentChanged, o, extra); err != nil {
		return StepResult{}, err
	}
	return StepResult{Order: o, Changed: true, Previous: from, Event: &row}, nil
}

// handOverTx is "ship": the status, shipped_at, the tracking note, the
// v1.shop.order.shipped event and the "shipped" letter, all of it only the
// first time. A parcel handed over again after a failed try keeps its
// shipped_at and sends no second letter; only a new number is stored.
func (s *Service) handOverTx(ctx context.Context, tx pgx.Tx, q *gen.Queries, o gen.ShopOrderRow, tracking *string) (gen.ShopOrderRow, error) {
	if o.ShippedAt != nil {
		if tracking == nil || *tracking == o.TrackingNote {
			return o, nil
		}
		return q.SetShopOrderTracking(ctx, o.ID, *tracking)
	}
	if o.Status == StatusManualReview {
		if reviewReasonIs(o, ReviewPaymentUnconfirmed) {
			// Parked because its PAYMENT waits for a human (an amount
			// mismatch, a refund at the provider before arena confirmed it):
			// the goods go out only once that payment is settled.
			if err := requireSucceededPayment(ctx, q, o); err != nil {
				return gen.ShopOrderRow{}, err
			}
		}
		if err := q.LockShopOrderVariants(ctx, o.ID); err != nil {
			return gen.ShopOrderRow{}, err
		}
		lines, err := q.ShopOrderShortages(ctx, o.ID)
		if err != nil {
			return gen.ShopOrderRow{}, err
		}
		if short := shortLines(lines); len(short) > 0 {
			e := newErr(http.StatusConflict, "shop.out_of_stock",
				"the goods of this order are not free on the shelf; add stock or refund the order")
			e.Details = map[string]any{"items": short}
			return gen.ShopOrderRow{}, e
		}
		if err := s.applySaleTx(ctx, q, o); err != nil {
			return gen.ShopOrderRow{}, err
		}
	}
	out, err := q.SetShopOrderStatus(ctx, o.ID, StatusShipped, tracking, nil)
	if err != nil {
		return gen.ShopOrderRow{}, err
	}
	if err := s.appendEvent(ctx, tx, EventOrderShipped, out, map[string]any{"tracking_note": out.TrackingNote}); err != nil {
		return gen.ShopOrderRow{}, err
	}
	s.enqueueOrderEmailTx(ctx, tx, EmailShipped, out)
	if o.RefundedMinor > 0 {
		// Part of the order was refunded before it was handed over: the
		// shipment must not erase that. The status follows the money again
		// through the one rule every refund writer uses (SA-10).
		out, err = s.syncRefundsTx(ctx, tx, q, out)
		if err != nil {
			return gen.ShopOrderRow{}, err
		}
	}
	return out, nil
}

// ApplyFulfillmentStep is SetFulfillmentStep in its own transaction, answering
// the order as the routes show it (with its lines, payment and history).
func (s *Service) ApplyFulfillmentStep(ctx context.Context, orgID, orderID uuid.UUID, step string, in StepInput) (OrderView, StepResult, error) {
	if err := s.ready(); err != nil {
		return OrderView{}, StepResult{}, err
	}
	if in.ActorName == "" && in.ActorUserID != nil {
		// Read on the pool BEFORE the transaction opens.
		in.ActorName = s.authorName(ctx, StaffNoteAuthor{UserID: in.ActorUserID})
	}
	var res StepResult
	err := s.inTx(ctx, func(tx pgx.Tx, _ *gen.Queries) error {
		var err error
		res, err = s.SetFulfillmentStep(ctx, tx, orgID, orderID, step, in)
		return err
	})
	if err != nil {
		return OrderView{}, StepResult{}, err
	}
	v, err := s.view(ctx, s.q, res.Order)
	return v, res, err
}
