// Package hinvitations is the staff invitation surface of spec 46: an owner
// or a manager issues tickets for 0 on chosen seats, including the seats
// of the organizer's reserve, lists the invitations of a session and
// cancels them.
//
// issue.go is the ONE issue transaction (spec 46, section 3.2). An
// invitation is an ordinary order with source 'complimentary', written on
// the organization's hidden internal channel, so tickets, the e-ticket, the
// door scan and the change-of-session letters all work exactly as for a
// purchase. What differs is only how the seats are taken: a staff
// invitation may take an 'unavailable' seat (the reserve) as well as an
// 'available' one, and it SELLS the seat in the same transaction that holds
// it, so a seat of the reserve is never visible as free for an instant.
package hinvitations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/invitations"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/txretry"
)

// Delivery modes of an invitation (spec 46, section 3.4).
const (
	DeliveryGuestEmail = "guest_email"
	DeliveryMyEmail    = "my_email"
	DeliveryPDFOnly    = "pdf_only"
)

// IssuedViaStaff is orders.issued_via of a one-by-one staff invitation and
// IssuedViaStaffBulk the one of an invitation written by a CSV batch.
const (
	IssuedViaStaff     = "staff"
	IssuedViaStaffBulk = "staff_bulk"
)

// MaxPlaces caps one issue request; a bigger list goes through the CSV
// upload (INV-06).
const MaxPlaces = 50

// seatOrigin values of order_items.seat_origin (migration 0174).
const (
	originAvailable   = "available"
	originUnavailable = "unavailable"
)

// holdLifetime is the expiry stamped on the invitation's reservation. The
// reservation is converted in the same transaction, so the value is only
// there because the column is NOT NULL; the TTL sweep never sees the row
// in an active state.
const holdLifetime = 15 * time.Minute

// requestRefPrefix marks orders.external_ref of a staff invitation:
// 'inv:' || request_id. external_ref is unique per channel
// (orders_channel_external_ref_uq) and an organization has one internal
// channel, so a request_id is unique per organization without a migration.
const requestRefPrefix = "inv:"

// orderExternalRefIndex is the unique index a concurrent repeat of the same
// request_id loses on.
const orderExternalRefIndex = "orders_channel_external_ref_uq"

// RequestRef is the orders.external_ref of a staff invitation request.
func RequestRef(requestID uuid.UUID) string { return requestRefPrefix + requestID.String() }

// GALine is one General Admission category of an invitation.
type GALine struct {
	TierID   uuid.UUID
	Quantity int32
}

// Guest is the person the invitation is for. Name is printed on the ticket.
type Guest struct {
	Name   string
	Email  string
	Phone  string
	Locale string
}

// IssueInput is one staff invitation request, already validated for shape
// by the HTTP layer (validateIssueRequest).
type IssueInput struct {
	OrgID     uuid.UUID
	SessionID uuid.UUID
	RequestID uuid.UUID
	SeatKeys  []string
	GA        []GALine
	Guest     Guest
	Delivery  string
	Note      string
	// IssuedBy is the user who issues; nil for an organization API key.
	IssuedBy *uuid.UUID
	// Actor is the order_events actor ('user:<uuid>' or 'system').
	Actor string
	// Audit writes the v1.invitation.issue row on the issuing transaction.
	// Nil skips the audit (unit fixtures only).
	Audit func(ctx context.Context, tx pgx.Tx, order gen.OrderRow, meta map[string]any) error
	// Now is the clock; zero means time.Now().
	Now time.Time
	// BatchID is the invitation_batches row a bulk order belongs to (the CSV
	// batch, INV-06); nil for a one-by-one invitation. A bulk order is
	// stamped issued_via 'staff_bulk'.
	BatchID *uuid.UUID
	// BatchSeq is the guest's 1-based position in the batch (kept in
	// orders.metadata.invitation.batch_seq for the result file).
	BatchSeq int

	// cache holds what a batch resolves once for all its guests (the
	// internal channel, the price of each category); nil for a single
	// invitation, which resolves everything itself.
	cache *issueCache
}

// issueCache saves the statements a batch would repeat per guest. Every
// round trip counts: a 1 000-place file is a few thousand statements.
type issueCache struct {
	channelID uuid.UUID
	amounts   map[uuid.UUID]int64
}

func (c *issueCache) amount(tier uuid.UUID) (int64, bool) {
	if c == nil {
		return 0, false
	}
	a, ok := c.amounts[tier]
	return a, ok
}

func (c *issueCache) put(tier uuid.UUID, amount int64) {
	if c != nil {
		c.amounts[tier] = amount
	}
}

// IssueResult is the committed invitation.
type IssueResult struct {
	OrderID  uuid.UUID
	Replayed bool
}

// IssueError is a refusal of the issue transaction with the HTTP status and
// machine-readable code the route answers.
type IssueError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *IssueError) Error() string { return e.Code + ": " + e.Message }

func refuse(status int, code, msg string, details map[string]any) *IssueError {
	return &IssueError{Status: status, Code: code, Message: msg, Details: details}
}

// errRequestRace is returned from inside the transaction when a concurrent
// request with the same request_id committed first; Issue then answers the
// winner's order as a replay.
var errRequestRace = errors.New("hinvitations: concurrent request with the same request_id")

// Issue runs the staff invitation issue transaction (spec 46, section 3.2)
// and answers the order it wrote, or the order an earlier request with the
// same request_id wrote (Replayed). Refusals are *IssueError.
func Issue(ctx context.Context, pool hcheckout.TxStarter, q *gen.Queries, in IssueInput) (IssueResult, error) {
	if pool == nil || q == nil {
		return IssueResult{}, errors.New("hinvitations: Issue requires a pool and queries")
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	ref := RequestRef(in.RequestID)

	if res, ok, err := replay(ctx, q, in, ref); err != nil || ok {
		return res, err
	}

	var orderID uuid.UUID
	err := txretry.Do(ctx, txretry.DefaultAttempts, func() error {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("hinvitations: begin issue tx: %w", err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

		id, err := issueTx(ctx, tx, q.WithTx(tx), in, ref)
		if err != nil {
			return err // a replayHit rolls the version bump back too
		}
		if err := tx.Commit(ctx); err != nil {
			if isRequestRace(err) {
				return errRequestRace
			}
			return fmt.Errorf("hinvitations: commit issue: %w", err)
		}
		orderID = id
		return nil
	})
	var hit *replayHit
	if errors.As(err, &hit) {
		return hit.res, nil
	}
	if errors.Is(err, errRequestRace) {
		res, ok, rerr := replay(ctx, q, in, ref)
		if rerr != nil {
			return IssueResult{}, rerr
		}
		if ok {
			return res, nil
		}
		return IssueResult{}, err
	}
	if err != nil {
		return IssueResult{}, err
	}
	return IssueResult{OrderID: orderID}, nil
}

// replay answers the order of an earlier request with the same request_id.
// A request_id reused for another session is a client error, never a
// silent second answer about a different session.
func replay(ctx context.Context, q *gen.Queries, in IssueInput, ref string) (IssueResult, bool, error) {
	prev, err := q.GetInvitationOrderByRequest(ctx, in.OrgID, ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return IssueResult{}, false, nil
	}
	if err != nil {
		return IssueResult{}, false, fmt.Errorf("hinvitations: request lookup: %w", err)
	}
	if prev.SessionID != in.SessionID {
		return IssueResult{}, false, refuse(http.StatusConflict, "invitation.request_id_reused",
			"this request_id was already used for another session", nil)
	}
	if prev.Fingerprint != nil && *prev.Fingerprint != Fingerprint(in) {
		return IssueResult{}, false, refuse(http.StatusConflict, "invitation.request_id_reused",
			"this request_id was already used with a different request", nil)
	}
	return IssueResult{OrderID: prev.ID, Replayed: true}, true, nil
}

// replayHit carries a replay found INSIDE the issue transaction (after the
// sessions row lock) out of it; the transaction is rolled back.
type replayHit struct{ res IssueResult }

func (h *replayHit) Error() string { return "hinvitations: request already issued" }

// Fingerprint is the digest of what an issue request asks for: the session,
// the seats and General Admission lines in a canonical order, the guest and
// the delivery. A request_id replayed with a different body is refused
// instead of silently answering the first invitation.
func Fingerprint(in IssueInput) string {
	seats := append([]string(nil), in.SeatKeys...)
	sort.Strings(seats)
	type gaLine struct {
		TierID   string `json:"tier_id"`
		Quantity int32  `json:"qty"`
	}
	ga := make([]gaLine, 0, len(in.GA))
	for _, l := range in.GA {
		ga = append(ga, gaLine{TierID: l.TierID.String(), Quantity: l.Quantity})
	}
	sort.Slice(ga, func(i, j int) bool {
		if ga[i].TierID != ga[j].TierID {
			return ga[i].TierID < ga[j].TierID
		}
		return ga[i].Quantity < ga[j].Quantity
	})
	raw, _ := json.Marshal(struct {
		SessionID string   `json:"session_id"`
		Seats     []string `json:"seat_keys"`
		GA        []gaLine `json:"ga"`
		Name      string   `json:"guest_name"`
		Email     string   `json:"guest_email"`
		Phone     string   `json:"guest_phone"`
		Delivery  string   `json:"delivery"`
	}{
		SessionID: in.SessionID.String(),
		Seats:     seats,
		GA:        ga,
		Name:      strings.TrimSpace(in.Guest.Name),
		Email:     strings.ToLower(strings.TrimSpace(in.Guest.Email)),
		Phone:     strings.TrimSpace(in.Guest.Phone),
		Delivery:  in.Delivery,
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func isRequestRace(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == orderExternalRefIndex
}

// takenPlace is one place the invitation took, with where it came from.
type takenPlace struct {
	seat   gen.SessionSeatRow
	origin string
}

// bumpSessionVersion takes the sessions row lock (the seat_status_version
// bump) as the transaction's first statement. Lock order (AGENTS.md, every
// hold mutation): sessions row FIRST, then the session_seats rows, then
// inventory_ledger.
func bumpSessionVersion(ctx context.Context, txq *gen.Queries, sessionID uuid.UUID) (int64, error) {
	version, err := txq.IncrementSessionSeatStatusVersion(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, refuse(http.StatusNotFound, "session.not_found", "session not found", nil)
	}
	if err != nil {
		return 0, fmt.Errorf("hinvitations: bump seat_status_version: %w", err)
	}
	return version, nil
}

// issueTarget loads the session and refuses a cancelled or deleted one. No
// sales window, no is_open, no sales end, no start time (spec 46, decision
// R-3).
func issueTarget(ctx context.Context, txq *gen.Queries, orgID, sessionID uuid.UUID) (gen.InvitationSessionTarget, error) {
	target, err := txq.GetInvitationSessionTarget(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (target.Deleted || target.OrgID != orgID)) {
		return target, refuse(http.StatusNotFound, "session.not_found", "session not found", nil)
	}
	if err != nil {
		return target, fmt.Errorf("hinvitations: session lookup: %w", err)
	}
	if target.Status == "cancelled" {
		return target, refuse(http.StatusUnprocessableEntity, "invitation.session_cancelled",
			"the session is cancelled, no invitation can be issued for it", nil)
	}
	return target, nil
}

// issueTx is the transaction body of ONE invitation.
func issueTx(ctx context.Context, tx pgx.Tx, txq *gen.Queries, in IssueInput, ref string) (uuid.UUID, error) {
	// 1. The sessions row lock, before any read the transaction decides from.
	version, err := bumpSessionVersion(ctx, txq, in.SessionID)
	if err != nil {
		return uuid.Nil, err
	}
	// 1b. The same request_id again, now under the sessions row lock: a
	// concurrent twin that held the lock first has committed by now, so its
	// order is visible here and this request answers it as a replay instead
	// of finding its seats sold (409 invitation.seats_taken).
	if res, ok, err := replay(ctx, txq, in, ref); err != nil {
		return uuid.Nil, err
	} else if ok {
		return uuid.Nil, &replayHit{res: res}
	}

	// 2. Only a cancelled or deleted session refuses.
	target, err := issueTarget(ctx, txq, in.OrgID, in.SessionID)
	if err != nil {
		return uuid.Nil, err
	}

	// 3a. Seats by key, FOR UPDATE in seat_key order.
	places, err := lockSeats(ctx, txq, in.SessionID, in.SeatKeys)
	if err != nil {
		return uuid.Nil, err
	}
	// 3b. General Admission places of each category: free first, then reserve.
	tiers := map[uuid.UUID]gen.TicketTierRow{}
	places, err = allocateGA(ctx, txq, in, places, tiers, nil)
	if err != nil {
		return uuid.Nil, err
	}
	w, err := writeInvitation(ctx, tx, txq, in, ref, version, target.EventID, places, tiers)
	if err != nil {
		return uuid.Nil, err
	}
	return w.OrderID, nil
}

// allocateGA locks the General Admission places of every line of the
// invitation, free places first and then the organizer's reserve, and
// appends them to places. exclude holds places this transaction has already
// locked for other invitations of a batch: SKIP LOCKED does not skip rows
// the SAME transaction locked, so they are asked for and dropped.
func allocateGA(
	ctx context.Context,
	txq *gen.Queries,
	in IssueInput,
	places []takenPlace,
	tiers map[uuid.UUID]gen.TicketTierRow,
	exclude map[uuid.UUID]bool,
) ([]takenPlace, error) {
	for _, line := range in.GA {
		tier, err := sessionTier(ctx, txq, in.SessionID, line.TierID)
		if err != nil {
			return nil, err
		}
		tiers[tier.ID] = tier
		chosen := make(map[uuid.UUID]bool, len(places)+len(exclude))
		for id := range exclude {
			chosen[id] = true
		}
		for _, p := range places {
			chosen[p.seat.ID] = true
		}
		units, err := txq.LockInvitationGAUnits(ctx, in.SessionID, line.TierID, line.Quantity+int32(len(chosen))) //nolint:gosec // capped by MaxPlaces / MaxBatchPlaces
		if err != nil {
			return nil, fmt.Errorf("hinvitations: lock GA places: %w", err)
		}
		got := int32(0)
		for _, u := range units {
			if got == line.Quantity {
				break
			}
			if chosen[u.ID] {
				continue
			}
			places = append(places, takenPlace{seat: u, origin: u.Status})
			got++
		}
		if got < line.Quantity {
			return nil, refuse(http.StatusConflict, "invitation.not_enough_places",
				"the category has fewer free or reserved places than requested",
				map[string]any{"tier_id": line.TierID.String(), "requested": line.Quantity, "available": got})
		}
	}
	return places, nil
}

// written is an invitation order the transaction wrote.
type written struct {
	OrderID     uuid.UUID
	SystemID    int64
	Places      int
	FromReserve int
}

// writeInvitation is steps 4 to 11 of the issue transaction for one guest
// whose places are already locked: capacity, reservation, prices, order,
// sale of the places, free completion, audit. A batch calls it once per
// guest on the batch's one transaction.
func writeInvitation(
	ctx context.Context,
	tx pgx.Tx,
	txq *gen.Queries,
	in IssueInput,
	ref string,
	version int64,
	eventID uuid.UUID,
	places []takenPlace,
	tiers map[uuid.UUID]gen.TicketTierRow,
) (written, error) {
	quantity := int32(len(places)) //nolint:gosec // capped at MaxPlaces / MaxBatchPlaces

	// 4. Session-level capacity, as every seat/GA-unit hold reserves it.
	if _, err := txq.ReserveCapacity(ctx, in.SessionID, nil, quantity); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return written{}, refuse(http.StatusConflict, "invitation.capacity_exceeded",
				"the session has no capacity left for these places", map[string]any{"requested": quantity})
		}
		return written{}, fmt.Errorf("hinvitations: reserve capacity: %w", err)
	}

	// 5. The hidden internal channel, the reservation, the held places.
	channelID := uuid.Nil
	if in.cache != nil {
		channelID = in.cache.channelID
	}
	if channelID == uuid.Nil {
		var err error
		if channelID, err = invitations.EnsureInternalChannel(ctx, txq, in.OrgID); err != nil {
			return written{}, err
		}
	}
	res, err := txq.InsertReservation(ctx, in.OrgID, channelID, in.SessionID, nil, in.IssuedBy, quantity, in.Now.Add(holdLifetime))
	if err != nil {
		return written{}, fmt.Errorf("hinvitations: insert reservation: %w", err)
	}
	// One statement holds every place and one links them: a bulk file is
	// thousands of round trips otherwise.
	ids := make([]uuid.UUID, 0, len(places))
	for _, p := range places {
		ids = append(ids, p.seat.ID)
	}
	heldRows, err := txq.HoldSessionSeatsForInvitation(ctx, ids, res.ID, version)
	if err != nil {
		return written{}, fmt.Errorf("hinvitations: hold places: %w", err)
	}
	held := make(map[uuid.UUID]gen.SessionSeatRow, len(heldRows))
	for _, h := range heldRows {
		held[h.ID] = h
	}
	var lost []string
	for i, p := range places {
		h, ok := held[p.seat.ID]
		if !ok {
			lost = append(lost, p.seat.SeatKey)
			continue
		}
		places[i].seat = h
	}
	if len(lost) > 0 {
		return written{}, seatsTaken(lost)
	}
	if err := txq.InsertReservationSeats(ctx, res.ID, ids); err != nil {
		return written{}, fmt.Errorf("hinvitations: link places: %w", err)
	}

	// 6. Prices: the face value of each category, then the whole subtotal
	// discounted (hcheckout.ComplimentaryBreakdown, the gateway's own rule).
	bd, tierPrices, err := price(ctx, txq, in, places, tiers)
	if err != nil {
		return written{}, err
	}
	token, err := checkoutToken()
	if err != nil {
		return written{}, fmt.Errorf("hinvitations: checkout token: %w", err)
	}
	cs, err := txq.InsertCheckoutSessionWithToken(ctx, in.OrgID, channelID, res.ID, in.IssuedBy, token, localePtr(in.Guest.Locale), nil)
	if err != nil {
		return written{}, fmt.Errorf("hinvitations: insert checkout session: %w", err)
	}
	if _, err := txq.ConfirmCheckoutSession(ctx, cs.ID,
		bd.Subtotal, bd.Discount, bd.PlatformFee, bd.ProviderFee, bd.Tax, bd.Total, bd.Currency, nil,
	); err != nil {
		return written{}, fmt.Errorf("hinvitations: confirm checkout session: %w", err)
	}

	// 7. The guest as a customer of the organization, best effort.
	customerID := resolveGuest(ctx, tx, in, channelID)

	// 8. The order, WITHOUT the customer: it is born pending_payment, and a
	// guest with an open purchase of their own on this session would trip
	// orders_one_pending_per_customer_session_uq. Step 10b links it once
	// the order is paid, in this same transaction.
	order, err := writeOrder(ctx, txq, in, ref, cs.ID, eventID, tierPrices)
	if err != nil {
		return written{}, err
	}
	seatIDs := make([]uuid.UUID, 0, len(places))
	origins := make([]string, 0, len(places))
	fromReserve := 0
	for _, p := range places {
		seatIDs = append(seatIDs, p.seat.ID)
		origins = append(origins, p.origin)
		if p.origin == originUnavailable {
			fromReserve++
		}
	}
	if err := txq.SetOrderItemsSeatOrigin(ctx, order.ID, seatIDs, origins); err != nil {
		return written{}, fmt.Errorf("hinvitations: seat origin: %w", err)
	}

	// 9. Sold in THIS transaction: places held -> sold, capacity held ->
	// sold, reservation converted. The checkout.convert_reservation job the
	// fulfilment tail enqueues then finds a converted reservation (no-op).
	if err := hcheckout.ConvertReservationInTx(ctx, txq, res.ID); err != nil {
		return written{}, fmt.Errorf("hinvitations: sell places: %w", err)
	}
	// 10. Completed for 0: ticket issuance job, order paid.
	if _, err := hcheckout.CompleteFreeCheckoutTx(ctx, tx, txq, cs.ID); err != nil {
		return written{}, fmt.Errorf("hinvitations: complete free checkout: %w", err)
	}
	// 10b. The guest's customer row, now that the order is paid.
	if customerID != nil {
		if err := txq.SetInvitationOrderCustomer(ctx, order.ID, *customerID); err != nil {
			return written{}, fmt.Errorf("hinvitations: link guest: %w", err)
		}
	}

	// 11. Audit: counts and the delivery mode, never the guest's address.
	if in.Audit != nil {
		meta := map[string]any{
			"session_id":   in.SessionID.String(),
			"request_id":   in.RequestID.String(),
			"places":       len(places),
			"seats":        len(in.SeatKeys),
			"ga_places":    len(places) - len(in.SeatKeys),
			"from_reserve": fromReserve,
			"delivery":     in.Delivery,
			"system_id":    order.SystemID,
		}
		if err := in.Audit(ctx, tx, order, meta); err != nil {
			return written{}, fmt.Errorf("hinvitations: audit: %w", err)
		}
	}
	return written{OrderID: order.ID, SystemID: order.SystemID, Places: len(places), FromReserve: fromReserve}, nil
}

// lockSeats locks the named seats and accepts only available and
// unavailable ones (spec 46, section 3.2 step 3).
func lockSeats(ctx context.Context, txq *gen.Queries, sessionID uuid.UUID, keys []string) ([]takenPlace, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	locked, err := txq.LockSessionSeatsForHold(ctx, sessionID, sorted)
	if err != nil {
		return nil, fmt.Errorf("hinvitations: lock seats: %w", err)
	}
	found := make(map[string]gen.SessionSeatRow, len(locked))
	for _, s := range locked {
		found[s.SeatKey] = s
	}
	var unknown, taken []string
	for _, k := range sorted {
		s, ok := found[k]
		switch {
		case !ok:
			unknown = append(unknown, k)
		case s.Status != originAvailable && s.Status != originUnavailable:
			taken = append(taken, k)
		}
	}
	if len(unknown) > 0 {
		return nil, refuse(http.StatusBadRequest, "invitation.unknown_seat",
			"the session has no seat with this key", map[string]any{"seat_keys": unknown})
	}
	if len(taken) > 0 {
		return nil, seatsTaken(taken)
	}
	out := make([]takenPlace, 0, len(sorted))
	for _, k := range sorted {
		s := found[k]
		out = append(out, takenPlace{seat: s, origin: s.Status})
	}
	return out, nil
}

func seatsTaken(keys []string) *IssueError {
	return refuse(http.StatusConflict, "invitation.seats_taken",
		"some seats are held or sold and cannot be given as an invitation",
		map[string]any{"seat_keys": keys})
}

func sessionTier(ctx context.Context, txq *gen.Queries, sessionID, tierID uuid.UUID) (gen.TicketTierRow, error) {
	tier, err := txq.GetTicketTierByID(ctx, tierID, sessionID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && tier.DeletedAt != nil) {
		return gen.TicketTierRow{}, refuse(http.StatusBadRequest, "invitation.unknown_category",
			"the session has no such category", map[string]any{"tier_id": tierID.String()})
	}
	if err != nil {
		return gen.TicketTierRow{}, fmt.Errorf("hinvitations: category lookup: %w", err)
	}
	return tier, nil
}

// price resolves the face value of every category the places belong to
// through the one resolver (priceresolve) and turns the cart into an
// invitation (total 0). A category priced by the buyer (pwyw) or free has
// a face value of 0.
func price(
	ctx context.Context,
	txq *gen.Queries,
	in IssueInput,
	places []takenPlace,
	tiers map[uuid.UUID]gen.TicketTierRow,
) (hcheckout.PricingBreakdown, map[uuid.UUID]int64, error) {
	counts := map[uuid.UUID]int32{}
	var order []uuid.UUID
	for _, p := range places {
		if p.seat.TierID == nil {
			return hcheckout.PricingBreakdown{}, nil, refuse(http.StatusUnprocessableEntity, "invitation.seat_without_category",
				"a seat has no category, assign one before issuing an invitation",
				map[string]any{"seat_keys": []string{p.seat.SeatKey}})
		}
		tid := *p.seat.TierID
		if _, ok := tiers[tid]; !ok {
			t, err := sessionTier(ctx, txq, in.SessionID, tid)
			if err != nil {
				return hcheckout.PricingBreakdown{}, nil, err
			}
			tiers[tid] = t
		}
		if counts[tid] == 0 {
			order = append(order, tid)
		}
		counts[tid]++
	}
	rows := make([]gen.TicketTierRow, 0, len(order))
	for _, tid := range order {
		rows = append(rows, tiers[tid])
	}
	amounts := make(map[uuid.UUID]int64, len(rows))
	var missing []gen.TicketTierRow
	for _, r := range rows {
		if a, ok := in.cache.amount(r.ID); ok {
			amounts[r.ID] = a
		} else {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		eff, err := priceresolve.ForTiers(ctx, txq, missing, in.Now)
		if err != nil {
			return hcheckout.PricingBreakdown{}, nil, fmt.Errorf("hinvitations: resolve prices: %w", err)
		}
		for _, r := range missing {
			amounts[r.ID] = eff[r.ID].Amount
			in.cache.put(r.ID, eff[r.ID].Amount)
		}
	}
	unit := make(map[uuid.UUID]int64, len(order))
	lines := make([]hcheckout.PricingLineInput, 0, len(order))
	currency := ""
	for _, tid := range order {
		t := tiers[tid]
		p := amounts[tid]
		if t.PricingMode == "free" || t.PricingMode == "pwyw" {
			p = 0
		}
		unit[tid] = p
		if currency == "" {
			currency = t.Currency
		}
		lines = append(lines, hcheckout.PricingLineInput{TierID: tid.String(), Quantity: counts[tid], UnitPrice: p})
	}
	bd := hcheckout.ComputePricingLines(lines, 0, currency, hcheckout.PricingRules{})
	return hcheckout.ComplimentaryBreakdown(bd), unit, nil
}

// resolveGuest attaches the guest's e-mail/phone to a customer of the
// organization inside a SAVEPOINT (AGENTS.md best-effort rule): a failure
// is logged by the savepoint's rollback and never costs the invitation.
// The order is linked to the customer only when the letter goes to the
// guest; for my_email and pdf_only nothing may route a ticket letter to the
// guest's address.
func resolveGuest(ctx context.Context, tx pgx.Tx, in IssueInput, channelID uuid.UUID) *uuid.UUID {
	email := strings.TrimSpace(in.Guest.Email)
	phone := strings.TrimSpace(in.Guest.Phone)
	if email == "" && phone == "" {
		return nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil
	}
	store := customers.NewStoreFromQueries(gen.New(sp))
	res, err := customers.Resolve(ctx, store, customers.ResolveInput{
		Email:     email,
		Phone:     phone,
		Name:      strings.TrimSpace(in.Guest.Name),
		ChannelID: channelID,
		Locale:    normalizeLocale(in.Guest.Locale),
		Source:    customers.SourceLive,
	})
	if err == nil {
		err = customers.LinkOrg(ctx, store, res.Customer.ID, in.OrgID, "order")
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		return nil
	}
	if err := sp.Commit(ctx); err != nil {
		return nil
	}
	if in.Delivery != DeliveryGuestEmail {
		return nil
	}
	id := res.Customer.ID
	return &id
}

// writeOrder materialises the order through the shared path and stamps the
// staff columns. The delivery mode and the internal note live in
// orders.metadata.invitation, where the letters (INV-03) read them.
func writeOrder(
	ctx context.Context,
	txq *gen.Queries,
	in IssueInput,
	ref string,
	checkoutSessionID, eventID uuid.UUID,
	tierPrices map[uuid.UUID]int64,
) (gen.OrderRow, error) {
	meta := map[string]any{
		"delivery":    in.Delivery,
		"request_id":  in.RequestID.String(),
		"fingerprint": Fingerprint(in),
	}
	if n := strings.TrimSpace(in.Note); n != "" {
		meta["note"] = n
	}
	if l := normalizeLocale(in.Guest.Locale); l != "" {
		meta["locale"] = l
	}
	if in.BatchID != nil {
		meta["batch_id"] = in.BatchID.String()
		meta["batch_seq"] = in.BatchSeq
	}
	raw, err := json.Marshal(map[string]any{"invitation": meta})
	if err != nil {
		return gen.OrderRow{}, fmt.Errorf("hinvitations: metadata: %w", err)
	}
	name := strings.TrimSpace(in.Guest.Name)
	var email, phone *string
	if in.Delivery == DeliveryGuestEmail {
		e := strings.TrimSpace(in.Guest.Email)
		email = &e
	}
	if p := strings.TrimSpace(in.Guest.Phone); p != "" {
		phone = &p
	}
	out, err := ordering.CreateOrderFromCheckout(ctx, txq, ordering.CreateInput{
		CheckoutSessionID: checkoutSessionID,
		EventID:           eventID,
		Source:            ordering.SourceComplimentary,
		Actor:             in.Actor,
		ExternalRef:       &ref,
		ChargePercentBP:   0,
		BuyerName:         &name,
		BuyerEmail:        email,
		BuyerPhone:        phone,
		TierUnitPrices:    tierPrices,
		Metadata:          raw,
	})
	if err != nil {
		if isRequestRace(err) {
			return gen.OrderRow{}, errRequestRace
		}
		return gen.OrderRow{}, fmt.Errorf("hinvitations: create order: %w", err)
	}
	if in.BatchID != nil {
		if err := txq.SetOrderInvitationBatch(ctx, out.Order.ID, IssuedViaStaffBulk, in.IssuedBy, *in.BatchID); err != nil {
			return gen.OrderRow{}, fmt.Errorf("hinvitations: stamp batch issuance: %w", err)
		}
	} else if err := txq.SetOrderStaffIssuance(ctx, out.Order.ID, IssuedViaStaff, in.IssuedBy); err != nil {
		return gen.OrderRow{}, fmt.Errorf("hinvitations: stamp issuance: %w", err)
	}
	return out.Order, nil
}

func checkoutToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// normalizeLocale folds a guest locale onto the e-mail template locales
// (the same rule as the widget's checkout/start); anything else is "".
func normalizeLocale(raw string) string {
	c := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(c, "-_"); i > 0 {
		c = c[:i]
	}
	for _, l := range templates.SupportedLocales {
		if c == l {
			return c
		}
	}
	return ""
}

func localePtr(raw string) *string {
	if l := normalizeLocale(raw); l != "" {
		return &l
	}
	return nil
}
