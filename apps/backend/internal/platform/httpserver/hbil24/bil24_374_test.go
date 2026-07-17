// bil24_374_test.go — contract tests for feature #374 PR2-18 BLOCKER:
// Stop Bil24 gateway from returning fake success and validate credentials.
//
// Tests cover four verification steps:
//
//	Step 1: CREATE_ORDER_EXT must never return resultCode=0 (stub security fix).
//	Step 2: CANCEL_ORDER must never return resultCode=0 (stub security fix).
//	Step 3: fid/token validation rejects unauthenticated and invalid-token requests.
//	Step 4: ADD_PROMO_CODES is explicitly gated (not just "unknown command").
//
// All tests are pure unit tests — no live PostgreSQL required.
package hbil24

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// newMinimalHandler builds a bare-minimum Handler with no dependencies wired
// (all nil). Sufficient for testing CREATE_ORDER_EXT, CANCEL_ORDER and
// ADD_PROMO_CODES which do not require any query handles.
func newMinimalHandler() *Handler {
	return New(
		nil, nil, nil, nil, nil,
		nil, nil, nil,
		ReservationDeps{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	)
}

// mustBcryptHash hashes plaintext with the minimum bcrypt cost for tests.
// Using cost 4 (MinCost) keeps test runs fast while still exercising the
// real bcrypt comparison path.
func mustBcryptHash(t *testing.T, plaintext string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword: %v", err)
	}
	return string(hash)
}

// channelSettings builds a settings JSON blob that carries a
// gateway_token_hash for use in fakeResCtx fixtures.
func channelSettings(t *testing.T, tokenHash string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"gateway_token_hash": tokenHash})
	if err != nil {
		t.Fatalf("json.Marshal channel settings: %v", err)
	}
	return b
}

// fakeResCtxWithToken is a minimal ReservationContextQuerier that returns a
// channel row whose Settings carry the provided gateway_token_hash.
type fakeResCtxWithToken struct {
	sessionID   uuid.UUID
	orgID       uuid.UUID
	channelID   uuid.UUID
	tokenHash   string // empty string = no hash stored in settings
	noChannel   bool   // if true, GetSalesChannelByID returns ErrNoRows
}

func (f *fakeResCtxWithToken) GetSessionOrgContext(_ context.Context, id uuid.UUID) (gen.SessionOrgContextRow, error) {
	if id != f.sessionID {
		return gen.SessionOrgContextRow{}, pgx.ErrNoRows
	}
	return gen.SessionOrgContextRow{SessionID: id, OrgID: f.orgID}, nil
}

func (f *fakeResCtxWithToken) GetSalesChannelByID(_ context.Context, id, orgID uuid.UUID) (gen.SalesChannelRow, error) {
	if f.noChannel || id != f.channelID || orgID != f.orgID {
		return gen.SalesChannelRow{}, pgx.ErrNoRows
	}
	var settings json.RawMessage
	if f.tokenHash != "" {
		var err error
		settings, err = json.Marshal(map[string]string{"gateway_token_hash": f.tokenHash})
		if err != nil {
			return gen.SalesChannelRow{}, err
		}
	} else {
		settings = json.RawMessage(`{}`)
	}
	return gen.SalesChannelRow{
		ID:       f.channelID,
		OrgID:    f.orgID,
		Name:     "test-channel",
		Settings: settings,
	}, nil
}

// buildHandlerWithToken wires a Handler whose RESERVATION path enforces
// fid/token validation (requireToken=true). The fake reservation callbacks
// always succeed so authentication failures are the only source of errors.
func buildHandlerWithToken(
	t *testing.T,
	sessionID, orgID, channelID uuid.UUID,
	tokenHash string,
) *Handler {
	t.Helper()
	admQ := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "general_admission", CapacityTotal: 50},
	}}
	ctxQ := &fakeResCtxWithToken{
		sessionID: sessionID,
		orgID:     orgID,
		channelID: channelID,
		tokenHash: tokenHash,
	}
	tierID := uuid.New()
	tierQ := &fakeTiers{tiers: map[uuid.UUID]gen.TicketTierRow{
		tierID: {ID: tierID, SessionID: sessionID, Name: "Standard", PricingMode: "fixed", PriceAmount: 1000, Currency: "USD"},
	}}
	deps := ReservationDeps{
		CtxQ:  ctxQ,
		TierQ: tierQ,
		GAReserve: func(_ context.Context, in hcheckout.GAHoldInput) (gen.ReservationRow, error) {
			return gen.ReservationRow{
				ID:        uuid.New(),
				OrgID:     in.OrgID,
				ChannelID: in.ChannelID,
				SessionID: in.SessionID,
				State:     "draft",
				ExpiresAt: in.ExpiresAt,
			}, nil
		},
		Release: func(_ context.Context, id uuid.UUID) (gen.ReservationRow, error) {
			return gen.ReservationRow{ID: id, State: "cancelled"}, nil
		},
		PricingRules: hcheckout.PricingRules{},
	}
	return New(
		nil, nil, nil, nil, nil,
		admQ, nil, nil,
		deps,
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	).WithRequireToken(true)
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — CREATE_ORDER_EXT must NEVER return resultCode=0
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_374_CreateOrderExt_ValidInput_NeverReturnsSuccess(t *testing.T) {
	// Feature #374: the scaffold stub must return a non-zero result code.
	// Returning resultCode=0 from an unimplemented handler gives the caller a
	// false success signal (they believe an order was created when it wasn't).
	h := newMinimalHandler()
	sessionID := uuid.New().String()
	tierID := uuid.New().String()
	body := `{"command":"CREATE_ORDER_EXT","actionEventId":"` + sessionID +
		`","categoryPriceId":"` + tierID + `","quantity":1,"email":"x@y.com"}`
	resp := postJSON(t, h, body)
	rc := mustResultCode(t, resp)
	if rc == ResultCodeOK {
		t.Errorf("Step 1 FAIL: CREATE_ORDER_EXT returned resultCode=0 (fake success); got %d", rc)
	}
}

func TestBil24_374_CreateOrderExt_ReturnsNotImplemented(t *testing.T) {
	// Step 1 specific: must return ResultCodeNotImplemented (-5), not some
	// other error that might be confused with a validation failure.
	h := newMinimalHandler()
	sessionID := uuid.New().String()
	tierID := uuid.New().String()
	body := `{"command":"CREATE_ORDER_EXT","actionEventId":"` + sessionID +
		`","categoryPriceId":"` + tierID + `"}`
	resp := postJSON(t, h, body)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeNotImplemented {
		t.Errorf("Step 1: expected resultCode=%d (NOT_IMPLEMENTED), got %d", ResultCodeNotImplemented, rc)
	}
}

func TestBil24_374_CreateOrderExt_MissingSessionID_StillRejectsBeforeStub(t *testing.T) {
	// Validation errors (missing fields) should still return -2, not -5.
	h := newMinimalHandler()
	resp := postJSON(t, h, `{"command":"CREATE_ORDER_EXT","categoryPriceId":"`+uuid.New().String()+`"}`)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeInvalidRequest {
		t.Errorf("Step 1: missing actionEventId should return %d, got %d", ResultCodeInvalidRequest, rc)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — CANCEL_ORDER must NEVER return resultCode=0
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_374_CancelOrder_ValidInput_NeverReturnsSuccess(t *testing.T) {
	// Feature #374: the scaffold stub must return a non-zero result code.
	// Returning resultCode=0 gives the caller a false cancellation signal.
	h := newMinimalHandler()
	orderID := uuid.New().String()
	resp := postJSON(t, h, `{"command":"CANCEL_ORDER","orderId":"`+orderID+`"}`)
	rc := mustResultCode(t, resp)
	if rc == ResultCodeOK {
		t.Errorf("Step 2 FAIL: CANCEL_ORDER returned resultCode=0 (fake success); got %d", rc)
	}
}

func TestBil24_374_CancelOrder_ReturnsNotImplemented(t *testing.T) {
	// Step 2 specific: must return ResultCodeNotImplemented (-5).
	h := newMinimalHandler()
	orderID := uuid.New().String()
	resp := postJSON(t, h, `{"command":"CANCEL_ORDER","orderId":"`+orderID+`"}`)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeNotImplemented {
		t.Errorf("Step 2: expected resultCode=%d (NOT_IMPLEMENTED), got %d", ResultCodeNotImplemented, rc)
	}
}

func TestBil24_374_CancelOrder_MissingOrderID_StillRejectsBeforeStub(t *testing.T) {
	// Validation errors (missing fields) should still return -2, not -5.
	h := newMinimalHandler()
	resp := postJSON(t, h, `{"command":"CANCEL_ORDER"}`)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeInvalidRequest {
		t.Errorf("Step 2: missing orderId should return %d, got %d", ResultCodeInvalidRequest, rc)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — fid/token validation rejects unauthenticated / invalid tokens
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_374_TokenValidation_MissingToken_Rejected(t *testing.T) {
	// When requireToken=true, a RESERVATION with no token field must be
	// rejected with ResultCodeUnauthorized (-4).
	sessionID := uuid.New()
	orgID := uuid.New()
	channelID := uuid.New()
	plainToken := "secret-gateway-token"
	tokenHash := mustBcryptHash(t, plainToken)

	h := buildHandlerWithToken(t, sessionID, orgID, channelID, tokenHash)

	// No "token" field in request — must be rejected.
	body := `{"command":"RESERVATION","fid":"` + channelID.String() +
		`","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + uuid.New().String() + `","quantity":1}]}`
	resp := postJSON(t, h, body)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeUnauthorized {
		t.Errorf("Step 3: missing token should return %d (Unauthorized), got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_374_TokenValidation_WrongToken_Rejected(t *testing.T) {
	// When requireToken=true, a RESERVATION with a wrong token must be
	// rejected with ResultCodeUnauthorized (-4).
	sessionID := uuid.New()
	orgID := uuid.New()
	channelID := uuid.New()
	plainToken := "correct-secret-token"
	tokenHash := mustBcryptHash(t, plainToken)

	h := buildHandlerWithToken(t, sessionID, orgID, channelID, tokenHash)

	// Wrong token — must be rejected.
	body := `{"command":"RESERVATION","fid":"` + channelID.String() +
		`","token":"wrong-token","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + uuid.New().String() + `","quantity":1}]}`
	resp := postJSON(t, h, body)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeUnauthorized {
		t.Errorf("Step 3: wrong token should return %d (Unauthorized), got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_374_TokenValidation_NoHashConfigured_Rejected(t *testing.T) {
	// When requireToken=true and the channel has NO gateway_token_hash stored
	// in its settings, the request must be rejected even if a token is supplied.
	// A channel without a configured hash has not been set up for gateway access.
	sessionID := uuid.New()
	orgID := uuid.New()
	channelID := uuid.New()

	// tokenHash="" means the fake channel returns settings={} (no hash stored).
	h := buildHandlerWithToken(t, sessionID, orgID, channelID, "")

	body := `{"command":"RESERVATION","fid":"` + channelID.String() +
		`","token":"any-token","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + uuid.New().String() + `","quantity":1}]}`
	resp := postJSON(t, h, body)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeUnauthorized {
		t.Errorf("Step 3: channel with no gateway_token_hash should return %d, got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_374_TokenValidation_CorrectToken_AllowsThrough(t *testing.T) {
	// When requireToken=true and the correct token is supplied, the RESERVATION
	// should proceed past authentication and reach the actual GA hold path.
	// With valid session/channel/tier wired and GA callback returning success,
	// we expect resultCode=0.
	sessionID := uuid.New()
	orgID := uuid.New()
	channelID := uuid.New()
	plainToken := "correct-gateway-token"
	tokenHash := mustBcryptHash(t, plainToken)

	// Build a tier that exists in the session.
	tierID := uuid.New()
	admQ := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "general_admission", CapacityTotal: 50},
	}}
	ctxQ := &fakeResCtxWithToken{
		sessionID: sessionID,
		orgID:     orgID,
		channelID: channelID,
		tokenHash: tokenHash,
	}
	tierQ := &fakeTiers{tiers: map[uuid.UUID]gen.TicketTierRow{
		tierID: {ID: tierID, SessionID: sessionID, Name: "Standard", PricingMode: "fixed", PriceAmount: 1000, Currency: "USD"},
	}}
	reservationID := uuid.New()
	deps := ReservationDeps{
		CtxQ:  ctxQ,
		TierQ: tierQ,
		GAReserve: func(_ context.Context, in hcheckout.GAHoldInput) (gen.ReservationRow, error) {
			return gen.ReservationRow{
				ID:        reservationID,
				OrgID:     in.OrgID,
				ChannelID: in.ChannelID,
				SessionID: in.SessionID,
				State:     "draft",
				ExpiresAt: in.ExpiresAt,
			}, nil
		},
		PricingRules: hcheckout.PricingRules{},
	}
	h := New(nil, nil, nil, nil, nil, admQ, nil, nil, deps,
		slog.New(slog.NewJSONHandler(io.Discard, nil))).WithRequireToken(true)

	body := `{"command":"RESERVATION","fid":"` + channelID.String() +
		`","token":"` + plainToken + `","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + tierID.String() + `","quantity":2}]}`
	resp := postJSON(t, h, body)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeOK {
		t.Errorf("Step 3: correct token should allow RESERVATION through, got resultCode=%d; resp: %v",
			rc, resp)
	}
}

func TestBil24_374_TokenValidation_RequireTokenFalse_NoValidation(t *testing.T) {
	// When requireToken=false (default), token validation is skipped for
	// backward compatibility. Existing integrations without a configured
	// gateway_token_hash must continue to work.
	sessionID := uuid.New()
	orgID := uuid.New()
	channelID := uuid.New()
	tierID := uuid.New()

	admQ := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "general_admission", CapacityTotal: 50},
	}}
	// Channel with no hash in settings — would be rejected when requireToken=true,
	// but must be accepted when requireToken=false.
	ctxQ := &fakeResCtxWithToken{
		sessionID: sessionID,
		orgID:     orgID,
		channelID: channelID,
		tokenHash: "", // no hash configured
	}
	tierQ := &fakeTiers{tiers: map[uuid.UUID]gen.TicketTierRow{
		tierID: {ID: tierID, SessionID: sessionID, Name: "GA", PricingMode: "fixed", PriceAmount: 500, Currency: "USD"},
	}}
	deps := ReservationDeps{
		CtxQ:  ctxQ,
		TierQ: tierQ,
		GAReserve: func(_ context.Context, in hcheckout.GAHoldInput) (gen.ReservationRow, error) {
			return gen.ReservationRow{
				ID: uuid.New(), OrgID: in.OrgID, ChannelID: in.ChannelID,
				SessionID: in.SessionID, State: "draft", ExpiresAt: in.ExpiresAt,
			}, nil
		},
		PricingRules: hcheckout.PricingRules{},
	}
	// requireToken=false (default via New, no WithRequireToken call)
	h := New(nil, nil, nil, nil, nil, admQ, nil, nil, deps,
		slog.New(slog.NewJSONHandler(io.Discard, nil)))

	body := `{"command":"RESERVATION","fid":"` + channelID.String() +
		`","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + tierID.String() + `","quantity":1}]}`
	resp := postJSON(t, h, body)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeOK {
		t.Errorf("Step 3: requireToken=false should skip validation and allow RESERVATION, got %d; resp: %v",
			rc, resp)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — ADD_PROMO_CODES is explicitly gated (not unknown command)
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_374_AddPromoCodes_NotUnknownCommand(t *testing.T) {
	// ADD_PROMO_CODES must NOT return ResultCodeUnknownCommand (-1).
	// It is a recognized command that is explicitly not implemented, so it
	// should return ResultCodeNotImplemented (-5). This lets callers
	// distinguish "the gateway doesn't know this command" from "this command
	// is not available in this gateway version".
	h := newMinimalHandler()
	resp := postJSON(t, h, `{"command":"ADD_PROMO_CODES","fid":"1271"}`)
	rc := mustResultCode(t, resp)
	if rc == ResultCodeUnknownCommand {
		t.Errorf("Step 4: ADD_PROMO_CODES must not return %d (unknown command); got %d",
			ResultCodeUnknownCommand, rc)
	}
}

func TestBil24_374_AddPromoCodes_ReturnsNotImplemented(t *testing.T) {
	// ADD_PROMO_CODES should return ResultCodeNotImplemented (-5) explicitly.
	h := newMinimalHandler()
	resp := postJSON(t, h, `{"command":"ADD_PROMO_CODES","fid":"1271"}`)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeNotImplemented {
		t.Errorf("Step 4: expected ADD_PROMO_CODES to return %d (NOT_IMPLEMENTED), got %d",
			ResultCodeNotImplemented, rc)
	}
}

func TestBil24_374_AddPromoCodes_CommandEchoedCorrectly(t *testing.T) {
	// The "command" field in the response must echo the canonical uppercase
	// command name.
	h := newMinimalHandler()
	resp := postJSON(t, h, `{"command":"ADD_PROMO_CODES","fid":"1271"}`)
	cmd, _ := resp["command"].(string)
	if cmd != "ADD_PROMO_CODES" {
		t.Errorf("Step 4: expected command echo 'ADD_PROMO_CODES', got %q", cmd)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Result code constants for new codes (feature #374)
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_374_ResultCodeConstants(t *testing.T) {
	if ResultCodeUnauthorized != -4 {
		t.Errorf("ResultCodeUnauthorized: expected -4, got %d", ResultCodeUnauthorized)
	}
	if ResultCodeNotImplemented != -5 {
		t.Errorf("ResultCodeNotImplemented: expected -5, got %d", ResultCodeNotImplemented)
	}
	// Verify they don't collide with existing codes.
	existing := map[int]string{
		0:   "ResultCodeOK",
		-1:  "ResultCodeUnknownCommand",
		-2:  "ResultCodeInvalidRequest",
		-3:  "ResultCodeNotFound",
		-99: "ResultCodeInternalError",
	}
	for code, name := range existing {
		if ResultCodeUnauthorized == code {
			t.Errorf("ResultCodeUnauthorized (-4) collides with %s (%d)", name, code)
		}
		if ResultCodeNotImplemented == code {
			t.Errorf("ResultCodeNotImplemented (-5) collides with %s (%d)", name, code)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WithRequireToken builder method (feature #374)
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_374_WithRequireToken_IsChainable(t *testing.T) {
	// WithRequireToken must return the same *Handler (for chaining).
	h := newMinimalHandler()
	h2 := h.WithRequireToken(true)
	if h2 != h {
		t.Error("WithRequireToken must return the receiver for chaining")
	}
}

func TestBil24_374_WithRequireToken_DefaultIsFalse(t *testing.T) {
	// New() must create a Handler with requireToken=false by default.
	// Verifiable indirectly: RESERVATION without token should succeed
	// when requireToken is not explicitly set (and the command reaches the
	// no-deps gate before authentication, returning resultCode=-99 not -4).
	h := newMinimalHandler()
	// With no reservation deps, the command self-gates on missing deps (-99).
	// If requireToken were true and evaluated first, it would return -4 instead.
	sessionID := uuid.New()
	body := `{"command":"RESERVATION","fid":"` + uuid.New().String() +
		`","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + uuid.New().String() + `","quantity":1}]}`
	resp := postJSON(t, h, body)
	rc := mustResultCode(t, resp)
	// Should be -99 (service unavailable from nil deps) NOT -4 (unauthorized)
	if rc == ResultCodeUnauthorized {
		t.Errorf("Default requireToken=false: got Unauthorized (-4), expected service-unavailable (-99); deps gate did not fire first")
	}
}
