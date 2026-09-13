package hbil24

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// A channel provisioned through the admin gateway-credential endpoint stores
// its hash as settings.gateway.token_hash (W1 shape). The legacy
// validateGatewayToken() path — still used by RESERVATION, the cart
// commands, CREATE_ORDER_EXT, PAY_ORDER and GET_TICKETS_BY_ORDER — read only
// the top-level gateway_token_hash and answered -1 "channel is not
// configured for gateway access" for every such channel (found live on the
// staging stand 2026-09-13 with the Lampyris ticket picker). It must resolve
// the hash through parseGatewaySettings like authenticateCommand does.
func TestBil24_ValidateGatewayToken_AcceptsNestedGatewayShape(t *testing.T) {
	sessionID := uuid.New()
	orgID := uuid.New()
	channelID := uuid.New()
	plainToken := "w1-gateway-token"
	tokenHash := mustBcryptHash(t, plainToken)

	tierID := uuid.New()
	admQ := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "general_admission", CapacityTotal: 50},
	}}
	ctxQ := &fakeResCtxWithToken{
		sessionID: sessionID,
		orgID:     orgID,
		channelID: channelID,
		tokenHash: tokenHash,
		nested:    true,
	}
	tierQ := &fakeTiers{tiers: map[uuid.UUID]gen.TicketTierRow{
		tierID: {ID: tierID, SessionID: sessionID, Name: "Standard", PricingMode: "fixed", PriceAmount: 1000, Currency: "CZK"},
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
	h := New(nil, nil, nil, nil, nil, admQ, nil, nil, deps,
		slog.New(slog.NewJSONHandler(io.Discard, nil))).WithRequireToken(true)

	body := `{"command":"RESERVATION","fid":"` + channelID.String() +
		`","token":"` + plainToken + `","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + tierID.String() + `","quantity":1}]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("nested settings.gateway.token_hash must authenticate RESERVATION, got resultCode=%d; resp: %v", rc, resp)
	}

	// Wrong token against the nested shape is still rejected.
	bad := `{"command":"RESERVATION","fid":"` + channelID.String() +
		`","token":"nope","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + tierID.String() + `","quantity":1}]}`
	resp = postJSON(t, h, bad)
	if rc := mustResultCode(t, resp); rc != ResultCodeUnauthorized {
		t.Fatalf("wrong token against nested shape must be rejected with %d, got %d; resp: %v", ResultCodeUnauthorized, rc, resp)
	}
}
