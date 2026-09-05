// cmd_cart_get_485_test.go — unit tests for feature #485 (W1-A5c, spec §7.5):
// the GET_CART projection of the session cart.
//
// Everything runs against in-memory fakes — no PostgreSQL. The live wire
// round-trip (real holds, real compatibility ids) belongs to the integration
// harness in tests/compat/bil24/get_cart_485_test.go.

package hbil24

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fakes
// ─────────────────────────────────────────────────────────────────────────────

// memCartQuerier is an in-memory GatewayCartQuerier: a list of reservations per
// gateway session plus the seat and GA lines of each reservation.
type memCartQuerier struct {
	byGateway map[uuid.UUID][]gen.ReservationRow
	seats     map[uuid.UUID][]gen.SessionSeatRow
	gaItems   map[uuid.UUID][]gen.ReservationGAItemRow
	listErr   error
	seatErr   error
}

func newMemCartQuerier() *memCartQuerier {
	return &memCartQuerier{
		byGateway: map[uuid.UUID][]gen.ReservationRow{},
		seats:     map[uuid.UUID][]gen.SessionSeatRow{},
		gaItems:   map[uuid.UUID][]gen.ReservationGAItemRow{},
	}
}

func (m *memCartQuerier) BindReservationToGatewaySession(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) error {
	return nil
}

func (m *memCartQuerier) GetActiveGatewayCartReservation(_ context.Context, gwID, sessionID uuid.UUID) (gen.ReservationRow, error) {
	for _, r := range m.byGateway[gwID] {
		if r.SessionID == sessionID {
			return r, nil
		}
	}
	return gen.ReservationRow{}, pgx.ErrNoRows
}

func (m *memCartQuerier) ListActiveGatewayCartReservations(_ context.Context, gwID uuid.UUID) ([]gen.ReservationRow, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.byGateway[gwID], nil
}

func (m *memCartQuerier) ListReservationSeats(_ context.Context, resID uuid.UUID) ([]gen.SessionSeatRow, error) {
	if m.seatErr != nil {
		return nil, m.seatErr
	}
	return m.seats[resID], nil
}

func (m *memCartQuerier) ListReservationGAItems(_ context.Context, resID uuid.UUID) ([]gen.ReservationGAItemRow, error) {
	return m.gaItems[resID], nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Fixture helpers
// ─────────────────────────────────────────────────────────────────────────────

// getCartFixture is one wired GET_CART world: a channel, a live gateway
// session, a tier catalogue and the in-memory cart behind them.
type getCartFixture struct {
	h        *Handler
	channel  gen.SalesChannelRow
	cart     *memCartQuerier
	gw       gen.GatewaySessionRow
	userID   int64
	tierID   uuid.UUID
	sessions []uuid.UUID // event sessions the tier catalogue covers
}

// newGetCartFixture wires a Handler with the whole §7.4 cart surface plus the
// §7.3 gateway-session surface, and mints one live session for userID 10001.
// feePercent is written verbatim into sales_channels.fee_percent so the
// truncation rule of chargePercent can be exercised with a fractional value.
func newGetCartFixture(t *testing.T, feePercent string) *getCartFixture {
	t.Helper()

	ch := createUserChannel(t)
	ch.FeePercent = feePercent

	sq := newMemSessionQuerier()
	customerID := uuid.New()
	const userID int64 = 10001
	sq.bySystemID[userID] = gen.CustomerRow{ID: customerID, SystemID: userID}
	gw, err := sq.InsertGatewaySession(context.Background(), "sess-485", customerID,
		ch.OrgID, ch.ID, "cs", nil, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("seed gateway session: %v", err)
	}

	sessionID := uuid.New()
	tierID := uuid.New()
	tiers := &fakeTiers{tiers: map[uuid.UUID]gen.TicketTierRow{
		tierID: {
			ID: tierID, SessionID: sessionID, Name: "Standard",
			PricingMode: "fixed", PriceAmount: 500, Currency: "CZK",
		},
	}}

	cart := newMemCartQuerier()
	h := New(nil, nil, nil, nil, nil, nil, nil, nil,
		ReservationDeps{TierQ: tiers},
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	h = h.WithChannelLookup(&fakeChannelLookup{
		byDisplayNumber: map[int64]gen.SalesChannelRow{ch.DisplayNumber: ch},
		byUUID:          map[uuid.UUID]gen.SalesChannelRow{ch.ID: ch},
	}).WithRequireToken(true).WithGatewaySessions(sq).WithGatewayCart(CartDeps{
		Q: cart,
		Extend: func(context.Context, hcheckout.HoldMutationInput) (hcheckout.HoldMutationResult, error) {
			return hcheckout.HoldMutationResult{}, nil
		},
		Shrink: func(context.Context, hcheckout.HoldMutationInput) (hcheckout.HoldMutationResult, error) {
			return hcheckout.HoldMutationResult{}, nil
		},
		Refresh: func(context.Context, []uuid.UUID, time.Duration) ([]gen.ReservationRow, error) {
			return nil, nil
		},
	})

	return &getCartFixture{
		h: h, channel: ch, cart: cart, gw: gw, userID: userID,
		tierID: tierID, sessions: []uuid.UUID{sessionID},
	}
}

// addSeatedLine appends one reservation holding the given seats of one event
// session, all stamped with the fixture tier, expiring ttl from now.
func (f *getCartFixture) addSeatedLine(sessionID uuid.UUID, ttl time.Duration, systemSeatIDs ...int64) uuid.UUID {
	resID := uuid.New()
	f.cart.byGateway[f.gw.ID] = append(f.cart.byGateway[f.gw.ID], gen.ReservationRow{
		ID: resID, OrgID: f.gw.OrgID, ChannelID: f.gw.ChannelID,
		SessionID: sessionID, State: "held",
		ExpiresAt: time.Now().UTC().Add(ttl),
	})
	tier := f.tierID
	for _, sid := range systemSeatIDs {
		f.cart.seats[resID] = append(f.cart.seats[resID], gen.SessionSeatRow{
			ID: uuid.New(), SessionID: sessionID, SeatKey: "Parter-3-12",
			SectorName: "Parter", RowName: "3", SeatNumber: "12",
			TierID: &tier, Status: "held", SystemSeatID: sid,
		})
	}
	return resID
}

// getCart runs the command and returns the decoded flat envelope.
func (f *getCartFixture) getCart(t *testing.T, mutate ...func(*bil24Request)) map[string]any {
	t.Helper()
	req := bil24Request{
		Command:   "GET_CART",
		FID:       "1271",
		Token:     createUserToken,
		Locale:    "ru-RU",
		UserID:    f.userID,
		SessionID: f.gw.SessionToken,
	}
	for _, m := range mutate {
		m(&req)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/compat/bil24/json", strings.NewReader("{}"))
	f.h.handleBil24GetCart(w, r, req)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, w.Body.String())
	}
	return out
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// assertKeySet enforces the spec §15.2 STRICT key-set rule on one object.
func assertKeySet(t *testing.T, label string, got map[string]any, want []string) {
	t.Helper()
	sort.Strings(want)
	have := keysOf(got)
	if strings.Join(have, ",") != strings.Join(want, ",") {
		t.Errorf("%s key set = %v, want %v", label, have, want)
	}
}

// getCartTopLevelKeys is the exact §7.5 envelope, and the reason no total
// alias may creep back in.
var getCartTopLevelKeys = []string{
	"resultCode", "description", "command",
	"cartTimeout", "currency", "sum", "discountAmount", "chargeAmount", "totalSum",
	"actionEventList",
}

func numOf(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("response has no %q key: %v", key, m)
	}
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("response %q = %#v, want a JSON number", key, v)
	}
	return n
}

// ─────────────────────────────────────────────────────────────────────────────
// Happy paths
// ─────────────────────────────────────────────────────────────────────────────

// Spec §7.5: an empty cart is a SUCCESS — resultCode 0, an empty
// actionEventList, every money field zero and cartTimeout 0.
func TestBil24_485_GetCart_EmptyCartIsSuccess(t *testing.T) {
	f := newGetCartFixture(t, "5.00")

	resp := f.getCart(t)

	if code := numOf(t, resp, "resultCode"); code != 0 {
		t.Fatalf("resultCode = %v, want 0 (description %v)", code, resp["description"])
	}
	assertKeySet(t, "GET_CART", resp, getCartTopLevelKeys)
	for _, key := range []string{"cartTimeout", "sum", "discountAmount", "chargeAmount", "totalSum"} {
		if got := numOf(t, resp, key); got != 0 {
			t.Errorf("empty cart %s = %v, want 0", key, got)
		}
	}
	list, ok := resp["actionEventList"].([]any)
	if !ok {
		t.Fatalf("actionEventList = %#v, want a JSON array", resp["actionEventList"])
	}
	if len(list) != 0 {
		t.Errorf("empty cart actionEventList = %#v, want []", list)
	}
}

// Spec §7.5 basic case: one CZK 500 seat behind a 5 % channel fee.
func TestBil24_485_GetCart_SingleSeat(t *testing.T) {
	f := newGetCartFixture(t, "5.00")
	f.addSeatedLine(f.sessions[0], 20*time.Minute, 1731)

	resp := f.getCart(t)

	if code := numOf(t, resp, "resultCode"); code != 0 {
		t.Fatalf("resultCode = %v, want 0 (description %v)", code, resp["description"])
	}
	assertKeySet(t, "GET_CART", resp, getCartTopLevelKeys)
	if got, want := resp["currency"], "CZK"; got != want {
		t.Errorf("currency = %v, want %v", got, want)
	}
	if got := numOf(t, resp, "sum"); got != 500 {
		t.Errorf("sum = %v, want 500", got)
	}
	// discountAmount stays 0 until promo codes land (feature #491).
	if got := numOf(t, resp, "discountAmount"); got != 0 {
		t.Errorf("discountAmount = %v, want 0", got)
	}
	if got := numOf(t, resp, "chargeAmount"); got != 25 {
		t.Errorf("chargeAmount = %v, want 25", got)
	}
	if got := numOf(t, resp, "totalSum"); got != 525 {
		t.Errorf("totalSum = %v, want 525", got)
	}
	ct := numOf(t, resp, "cartTimeout")
	if ct <= 0 || ct > 1200 {
		t.Errorf("cartTimeout = %v, want 0 < n <= 1200", ct)
	}
	if ct != float64(int64(ct)) {
		t.Errorf("cartTimeout = %v, want an integer number of seconds", ct)
	}

	events, _ := resp["actionEventList"].([]any)
	if len(events) != 1 {
		t.Fatalf("actionEventList = %#v, want exactly one group", resp["actionEventList"])
	}
	group, _ := events[0].(map[string]any)
	assertKeySet(t, "actionEventList[0]", group,
		[]string{"actionEventId", "chargePercent", "seatList"})
	if got := numOf(t, group, "chargePercent"); got != 5 {
		t.Errorf("chargePercent = %v, want 5", got)
	}
	seats, _ := group["seatList"].([]any)
	if len(seats) != 1 {
		t.Fatalf("seatList = %#v, want exactly one row", group["seatList"])
	}
	row, _ := seats[0].(map[string]any)
	assertKeySet(t, "seatList[0]", row,
		[]string{"seatId", "categoryPriceId", "tariffPlanId", "price", "discount"})
	if got := numOf(t, row, "seatId"); got != 1731 {
		t.Errorf("seatId = %v, want 1731", got)
	}
	if got := numOf(t, row, "price"); got != 500 {
		t.Errorf("price = %v, want 500", got)
	}
	if got := numOf(t, row, "discount"); got != 0 {
		t.Errorf("discount = %v, want 0", got)
	}
	if row["tariffPlanId"] != nil {
		t.Errorf("tariffPlanId = %#v, want null", row["tariffPlanId"])
	}
	// seatList rows carry NO actionEventId: §7.5 hoists it to the group.
	if _, leaked := row["actionEventId"]; leaked {
		t.Error("seatList row carries actionEventId; §7.5 hoists it to the action event group")
	}
}

// Spec §7.5: the cart is grouped BY action event — two event sessions in one
// cart produce two groups, and the seats never migrate between them.
func TestBil24_485_GetCart_GroupsByActionEvent(t *testing.T) {
	f := newGetCartFixture(t, "5.00")
	other := uuid.New()
	f.addSeatedLine(f.sessions[0], 20*time.Minute, 1731, 1732)
	f.addSeatedLine(other, 10*time.Minute, 1740)

	resp := f.getCart(t)

	if code := numOf(t, resp, "resultCode"); code != 0 {
		t.Fatalf("resultCode = %v, want 0 (description %v)", code, resp["description"])
	}
	events, _ := resp["actionEventList"].([]any)
	if len(events) != 2 {
		t.Fatalf("actionEventList = %#v, want two groups", resp["actionEventList"])
	}
	first, _ := events[0].(map[string]any)
	second, _ := events[1].(map[string]any)
	if seats, _ := first["seatList"].([]any); len(seats) != 2 {
		t.Errorf("first group seatList = %#v, want two rows", first["seatList"])
	}
	if seats, _ := second["seatList"].([]any); len(seats) != 1 {
		t.Errorf("second group seatList = %#v, want one row", second["seatList"])
	}
	if first["actionEventId"] == second["actionEventId"] {
		t.Errorf("both groups carry actionEventId %v; want distinct ids", first["actionEventId"])
	}
	// sum spans BOTH groups: three seats of the 500 tier.
	if got := numOf(t, resp, "sum"); got != 1500 {
		t.Errorf("sum = %v, want 1500", got)
	}
	// cartTimeout follows the NEAREST expiry across the whole cart.
	if ct := numOf(t, resp, "cartTimeout"); ct <= 0 || ct > 600 {
		t.Errorf("cartTimeout = %v, want the nearest expiry (<= 600s)", ct)
	}
}

// Spec §7.5: chargePercent is an int — a fractional fee_percent truncates in
// the group while chargeAmount stays exact.
func TestBil24_485_GetCart_ChargePercentTruncatesButAmountIsExact(t *testing.T) {
	f := newGetCartFixture(t, "5.50")
	f.addSeatedLine(f.sessions[0], 20*time.Minute, 1731)

	resp := f.getCart(t)

	events, _ := resp["actionEventList"].([]any)
	if len(events) != 1 {
		t.Fatalf("actionEventList = %#v, want one group", resp["actionEventList"])
	}
	group, _ := events[0].(map[string]any)
	if got := numOf(t, group, "chargePercent"); got != 5 {
		t.Errorf("chargePercent = %v, want 5 (int of 5.50)", got)
	}
	if got := numOf(t, resp, "chargeAmount"); got != 27.5 {
		t.Errorf("chargeAmount = %v, want 27.5 (5.5%% of 500)", got)
	}
	if got := numOf(t, resp, "totalSum"); got != 527.5 {
		t.Errorf("totalSum = %v, want 527.5", got)
	}
}

// Guardrail: spec §7.5 names totalSum as the ONE total. Older plugin builds
// also read totalAmount / estimatedTotal / estimateTotal; emitting any of them
// would make the site pick a different number than the one it is charged.
func TestBil24_485_GetCart_NoTotalAliases(t *testing.T) {
	f := newGetCartFixture(t, "5.00")
	f.addSeatedLine(f.sessions[0], 20*time.Minute, 1731)

	resp := f.getCart(t)

	for _, banned := range []string{"totalAmount", "estimatedTotal", "estimateTotal", "discount", "charge"} {
		if _, present := resp[banned]; present {
			t.Errorf("response carries %q; §7.5 allows only totalSum / discountAmount / chargeAmount", banned)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Error envelopes
// ─────────────────────────────────────────────────────────────────────────────

// Spec §5: a mutating-or-not command still needs the channel token.
func TestBil24_485_GetCart_MissingTokenIsUnauthorized(t *testing.T) {
	f := newGetCartFixture(t, "5.00")

	resp := f.getCart(t, func(r *bil24Request) { r.Token = "" })

	if code := numOf(t, resp, "resultCode"); code != float64(ResultCodeUnauthorized) {
		t.Errorf("resultCode = %v, want %d", code, ResultCodeUnauthorized)
	}
}

// Spec §6 / §7.3: an unknown or expired sessionId is resultCode 1 so the site
// re-runs CREATE_USER.
func TestBil24_485_GetCart_UnknownSessionIsStale(t *testing.T) {
	f := newGetCartFixture(t, "5.00")

	resp := f.getCart(t, func(r *bil24Request) { r.SessionID = "nope" })

	if code := numOf(t, resp, "resultCode"); code != float64(ResultCodeSessionExpired) {
		t.Errorf("resultCode = %v, want %d", code, ResultCodeSessionExpired)
	}
}

// A cart read that fails on infrastructure is transient (-1), not -99: the
// plugin must retry rather than surface a hard error to the buyer.
func TestBil24_485_GetCart_ListingFailureIsTransient(t *testing.T) {
	f := newGetCartFixture(t, "5.00")
	f.cart.listErr = errors.New("connection reset")

	resp := f.getCart(t)

	if code := numOf(t, resp, "resultCode"); code != float64(ResultCodeTransient) {
		t.Errorf("resultCode = %v, want %d", code, ResultCodeTransient)
	}
}

// Without the cart surface there is no session cart to read: the command must
// self-gate with -99 instead of reporting an empty cart that does not exist.
func TestBil24_485_GetCart_SelfGatesWithoutCartDeps(t *testing.T) {
	f := newGetCartFixture(t, "5.00")
	f.h = f.h.WithGatewayCart(CartDeps{})

	resp := f.getCart(t)

	if code := numOf(t, resp, "resultCode"); code != float64(ResultCodeInternalError) {
		t.Errorf("resultCode = %v, want %d", code, ResultCodeInternalError)
	}
}

// The dispatcher must route GET_CART — an unrouted command answers -2.
func TestBil24_485_GetCart_IsRoutedByDispatcher(t *testing.T) {
	f := newGetCartFixture(t, "5.00")

	body := `{"command":"GET_CART","fid":"1271","token":"` + createUserToken +
		`","userId":10001,"sessionId":"` + f.gw.SessionToken + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/compat/bil24/json", strings.NewReader(body))
	f.h.HandleBil24Command(w, r)

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, w.Body.String())
	}
	if code := numOf(t, resp, "resultCode"); code != 0 {
		t.Fatalf("dispatched GET_CART resultCode = %v, want 0 (description %v)",
			code, resp["description"])
	}
	assertKeySet(t, "GET_CART", resp, getCartTopLevelKeys)
}
