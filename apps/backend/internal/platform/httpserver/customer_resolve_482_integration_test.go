//go:build integration

// customer_resolve_482_integration_test.go — live-DB coverage for feature
// #482 (W1-A4d, spec §12.3): a public-feed checkout with a name/phone/email
// buyer resolves (or creates) a `customers` row, links it to the org via
// `customer_org_links`, stamps `reservations.customer_id`, and the new
// org-scoped read endpoints (GET .../customers and GET .../customers/{id})
// surface that same customer to an authenticated, org-member caller.
//
// The test drives the REAL endpoints
//
//	POST /v1/public/feeds/{feed_token}/checkout/start
//	GET  /v1/organizations/{org_id}/customers?q=
//	GET  /v1/organizations/{org_id}/customers/{id}
//
// through the mounted chi router (no handler stubs — see AGENTS.md).
//
// Prerequisites: DATABASE_URL against a migrated database (head >= 0095).
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestW1A4d_482
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// buildW1A4dAuthServer mirrors productionIntegrationServer (see
// auth_production_integration_test.go) — a full *Server with BOTH the dev
// StubProvider and the production JWTVerifier wired, so the test can mint a
// real bearer token and drive the customer.read-gated org routes end to end.
func buildW1A4dAuthServer(t *testing.T, pool *pgxpool.Pool) (*Server, string, string, string) {
	t.Helper()
	const secret = "w1a4d-482-integration-secret-32b!!"
	const issuer = "arena-api"
	const audience = "arena-api"

	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret: secret, Issuer: issuer, Audience: audience,
		DefaultTTL: time.Hour, Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	verifier, err := auth.NewJWTVerifier(secret, issuer, audience)
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		AppName:        "test",
		AppVersion:     "0.0.0-dev",
		RequestTimeout: 10 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  secret,
		JWTIssuer:      issuer,
		JWTAudience:    audience,
		JWTDefaultTTL:  time.Hour,
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en"},
	}
	srv := New(Options{
		Config:   cfg,
		Pool:     pool,
		PgxPool:  pool,
		Auth:     stub,
		Verifier: verifier,
	})
	return srv, secret, issuer, audience
}

// TestW1A4d_482_PublicFeedCheckoutResolvesCustomerAndOrgSurfaceSeesIt is the
// feature #482 acceptance test: a public-feed purchase with a name/phone
// buyer resolves the customer + org link + reservation stamp, and the new
// GET /v1/organizations/{org_id}/customers[?q=]/{id} surface can read it back
// under a real customer.read-scoped membership (feature #482 migration 0095
// grants customer.read to the organizer role).
func TestW1A4d_482_PublicFeedCheckoutResolvesCustomerAndOrgSurfaceSeesIt(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newW1A6cFixture(t, ctx, pool)
	defer f.cleanup()

	srv, secret, issuer, audience := buildW1A4dAuthServer(t, pool)

	const qty = 1
	const buyerName = "Petra Resolve"

	// ── 1. Real endpoint: public-feed checkout start with a full buyer ───────
	body, err := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"qty":        qty,
		"buyer": map[string]any{
			"email": f.buyerMail,
			"name":  buyerName,
			"phone": f.buyerPhone,
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var startResp struct {
		CheckoutSession struct {
			ID string `json:"id"`
		} `json:"checkout_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("decode start response: %v (body: %s)", err, rec.Body.String())
	}
	csID, err := uuid.Parse(startResp.CheckoutSession.ID)
	if err != nil {
		t.Fatalf("checkout_session.id is not a UUID: %v", err)
	}

	// ── 2. customers.Resolve created a customer, linked it to the org, and ───
	//      stamped the reservation behind this checkout session.
	var customerID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT customer_id FROM customer_identities WHERE value_normalized = $1`,
		f.buyerMail).Scan(&customerID); err != nil {
		t.Fatalf("customer_identities lookup by buyer email: %v", err)
	}

	var linkCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_org_links WHERE customer_id = $1 AND org_id = $2`,
		customerID, f.orgID).Scan(&linkCount); err != nil {
		t.Fatalf("count customer_org_links: %v", err)
	}
	if linkCount != 1 {
		t.Fatalf("customer_org_links for (customer, org) = %d, want exactly 1", linkCount)
	}

	var reservationCustomerID *uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT customer_id FROM reservations WHERE id = (
		     SELECT reservation_id FROM checkout_sessions WHERE id = $1
		 )`, csID).Scan(&reservationCustomerID); err != nil {
		t.Fatalf("read reservations.customer_id: %v", err)
	}
	if reservationCustomerID == nil || *reservationCustomerID != customerID {
		t.Fatalf("reservations.customer_id = %v, want %s (§12.2 customer resolution did not stamp the reservation)",
			reservationCustomerID, customerID)
	}

	// ── 3. Grant an org.read/customer.read membership and mint a bearer ──────
	//      token for a caller who is an org member (feature #482 migration
	//      0095 grants customer.read to the organizer role).
	userID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`,
		userID, fmt.Sprintf("w1a4d-482-caller-%s@arena-integration.test", userID.String()[:8])); err != nil {
		t.Fatalf("insert caller user: %v", err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) }()

	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (id, user_id, org_id, role, status, joined_at)
		 VALUES ($1, $2, $3, 'organizer', 'active', now())`,
		uuid.New(), userID, f.orgID); err != nil {
		t.Fatalf("insert caller membership: %v", err)
	}

	tok, _, err := auth.IssueJWT(secret, userID, &f.orgID, nil, issuer, audience, time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}

	// ── 4. GET /v1/organizations/{org_id}/customers?q= finds the new customer.
	listReq := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+f.orgID.String()+"/customers?q="+url.QueryEscape(buyerName), nil)
	listReq.Header.Set("Authorization", "Bearer "+tok)
	listRec := httptest.NewRecorder()
	srv.router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET customers?q= = %d, want 200; body: %s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Customers []struct {
			ID string `json:"id"`
		} `json:"customers"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v (body: %s)", err, listRec.Body.String())
	}
	found := false
	for _, c := range listResp.Customers {
		if c.ID == customerID.String() {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("GET customers?q=%q did not return the resolved customer %s; got %+v",
			buyerName, customerID, listResp.Customers)
	}

	// ── 5. GET /v1/organizations/{org_id}/customers/{id} returns the card ────
	//      with the buyer's phone masked (unverified) and email present.
	cardReq := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+f.orgID.String()+"/customers/"+customerID.String(), nil)
	cardReq.Header.Set("Authorization", "Bearer "+tok)
	cardRec := httptest.NewRecorder()
	srv.router.ServeHTTP(cardRec, cardReq)
	if cardRec.Code != http.StatusOK {
		t.Fatalf("GET customers/{id} = %d, want 200; body: %s", cardRec.Code, cardRec.Body.String())
	}
	var cardResp struct {
		ID         string `json:"id"`
		Identities []struct {
			Kind     string `json:"kind"`
			Value    string `json:"value"`
			Verified bool   `json:"verified"`
		} `json:"identities"`
	}
	if err := json.Unmarshal(cardRec.Body.Bytes(), &cardResp); err != nil {
		t.Fatalf("decode card response: %v (body: %s)", err, cardRec.Body.String())
	}
	if cardResp.ID != customerID.String() {
		t.Fatalf("card id = %s, want %s", cardResp.ID, customerID)
	}
	phoneMasked := false
	for _, ident := range cardResp.Identities {
		if ident.Kind == "phone" {
			if ident.Verified {
				t.Errorf("phone identity unexpectedly verified for a public-feed buyer")
			}
			if ident.Value == f.buyerPhone {
				t.Errorf("phone identity value = %q, want masked (unverified strong identity)", ident.Value)
			}
			phoneMasked = true
		}
	}
	if !phoneMasked {
		t.Errorf("card did not surface a phone identity; got %+v", cardResp.Identities)
	}
}
