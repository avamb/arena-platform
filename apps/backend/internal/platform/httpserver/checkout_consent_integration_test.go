//go:build integration

// checkout_consent_integration_test.go — P2: the buyer consent at checkout,
// through the real router.
//
//   - PATCH .../seller-display refuses consent_required while the seller
//     profile is incomplete (422 seller.incomplete) and accepts it once it is
//     complete; GET .../seller-profile answers it per channel.
//   - GET .../events/{id}: every session carries consent.required, the
//     seller's legal name and the two links.
//   - POST .../checkout/start: without the tick 422 checkout.consent_required
//     and no hold; with it 201 and a checkout_consents row carrying the
//     request's peer address, the browser string and the withdrawal flag the
//     feed answered.
//   - Flag off: 201 without a consent and no row; a tick sent anyway is kept.
//   - GET .../orders/{order_id}/consents answers the row to the order's
//     organization only.
//
// The cart is free (zero total), so no payment provider is involved.
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ -run TestCheckoutConsent
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const consentTestUA = "Mozilla/5.0 (P2 consent test) Arena/1.0"

// consentFixture is a free hosted checkout plus an owner of its organization.
type consentFixture struct {
	*hostedOrgFixture
	ownerID uuid.UUID
}

func newConsentFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) *consentFixture {
	t.Helper()
	f := newFreeBuyerFieldsFixture(t, ctx, pool, label, false, false)
	q := gen.New(pool)
	owner, err := q.InsertUser(ctx, fmt.Sprintf("consent-owner-%s-%s@arena-integration.test", label, uuid.NewString()[:8]), "x", "en")
	if err != nil {
		f.cleanup()
		t.Fatalf("InsertUser: %v", err)
	}
	if _, err := q.InsertMembership(ctx, owner.ID, f.orgID, "org_admin"); err != nil {
		f.cleanup()
		t.Fatalf("InsertMembership: %v", err)
	}
	// A hosted-page channel with a public e-mail; the organization's legal
	// data is filled per test.
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings =
		'{"hosted_page":{"enabled":true,"public_contact":{"email":"tickets@consent.example"}}}'::jsonb
		WHERE id = $1`, f.channelID); err != nil {
		f.cleanup()
		t.Fatalf("enable hosted page: %v", err)
	}
	return &consentFixture{hostedOrgFixture: f, ownerID: owner.ID}
}

func (f *consentFixture) close() {
	ctx := context.Background()
	_, _ = f.pool.Exec(ctx, `DELETE FROM audit_events WHERE action = 'v1.channel.seller_display_update' AND metadata->>'org_id' = $1`, f.orgID.String())
	_, _ = f.pool.Exec(ctx, `DELETE FROM memberships WHERE org_id = $1`, f.orgID)
	f.cleanup()
	_, _ = f.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.ownerID)
}

func (f *consentFixture) completeProfile(t *testing.T, ctx context.Context) {
	t.Helper()
	if _, err := f.pool.Exec(ctx, `UPDATE organizations SET legal_name = 'Consent Teatro S.L.',
		tax_id_scheme = 'eu_vat', tax_id = 'ESB12345678', legal_address_line1 = 'Calle Mayor 1',
		legal_address_city = 'Madrid', legal_address_postal_code = '28001', legal_address_country = 'ES'
		WHERE id = $1`, f.orgID); err != nil {
		t.Fatalf("fill the legal data: %v", err)
	}
}

func buildConsentServer(t *testing.T, pool *pgxpool.Pool) *Server {
	t.Helper()
	return buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.CORSAllowedOrigins = []string{hostedTicketsBaseURL}
		c.PublicTicketsBaseURL = hostedTicketsBaseURL
		c.OnboardingSiteURL = "https://site.arena-integration.test"
		c.PlatformTermsURL = "https://site.arena-integration.test/legal/buyer-terms"
	})
}

func consentAuthed(t *testing.T, srv *Server, asUser uuid.UUID, method, path, body string) (int, []byte) {
	t.Helper()
	token, _, err := auth.IssueJWT(botTestJWTSecret, asUser, nil, nil, botTestJWTIssuer, botTestJWTAudience, time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// startWithConsent sends a one-ticket free checkout from a fixed peer address
// and browser, with the consent member given (nil = absent).
func (f *consentFixture) startWithConsent(t *testing.T, srv *Server, consent map[string]any) (int, []byte) {
	t.Helper()
	payload := map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"qty":        1,
		"buyer":      map[string]any{"email": f.buyerMail},
		"return_url": hostedTicketsBaseURL + "/shows",
		"locale":     "ru",
	}
	if consent != nil {
		payload["consent"] = consent
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", consentTestUA)
	req.RemoteAddr = "203.0.113.42:51000"
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

type consentSessionWire struct {
	ID                   string `json:"id"`
	ShowWithdrawalNotice bool   `json:"show_withdrawal_notice"`
	Consent              struct {
		Required        bool   `json:"required"`
		SellerLegalName string `json:"seller_legal_name"`
		TermsURL        string `json:"terms_url"`
		PrivacyURL      string `json:"privacy_url"`
	} `json:"consent"`
}

func (f *consentFixture) feedSession(t *testing.T, srv *Server) consentSessionWire {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/"+f.feedToken+"/events/"+f.eventID.String(), nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET event = %d; body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "203.0.113.") || strings.Contains(rec.Body.String(), consentTestUA) {
		t.Fatalf("the public feed answers a buyer's address or browser: %s", rec.Body.String())
	}
	var body struct {
		Event struct {
			Sessions []consentSessionWire `json:"sessions"`
		} `json:"event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	for _, s := range body.Event.Sessions {
		if s.ID == f.sessionID.String() {
			return s
		}
	}
	t.Fatalf("session %s not in the event answer", f.sessionID)
	return consentSessionWire{}
}

type consentRowWire struct {
	ID                    string  `json:"id"`
	CheckoutSessionID     string  `json:"checkout_session_id"`
	Kind                  string  `json:"kind"`
	TermsVersion          string  `json:"terms_version"`
	Locale                *string `json:"locale"`
	AcceptedAt            string  `json:"accepted_at"`
	ClientIP              *string `json:"client_ip"`
	UserAgent             *string `json:"user_agent"`
	WithdrawalNoticeShown bool    `json:"withdrawal_notice_shown"`
}

func countConsents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM checkout_consents WHERE org_id = $1`, orgID).Scan(&n); err != nil {
		t.Fatalf("count consents: %v", err)
	}
	return n
}

func orderIDOfStart(t *testing.T, ctx context.Context, pool *pgxpool.Pool, body []byte) uuid.UUID {
	t.Helper()
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode checkout/start: %v", err)
	}
	order, err := gen.New(pool).GetOrderByCheckoutSession(ctx, uuid.MustParse(start.CheckoutSession.ID))
	if err != nil {
		t.Fatalf("GetOrderByCheckoutSession: %v", err)
	}
	return order.ID
}

func TestCheckoutConsent_FlagOnRequiresAndRecordsTheConsent(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	srv := buildConsentServer(t, pool)
	f := newConsentFixture(t, ctx, pool, "cs-on")
	defer f.close()

	base := "/v1/organizations/" + f.orgID.String()
	display := base + "/channels/" + f.channelID.String() + "/seller-display"

	// ── incomplete profile: the switch is refused and nothing is written ──
	code, raw := consentAuthed(t, srv, f.ownerID, http.MethodPatch, display, `{"consent_required": true}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), `"seller.incomplete"`) || !strings.Contains(string(raw), `"tax_id"`) {
		t.Fatalf("consent on while incomplete: %d %s", code, raw)
	}
	if s := f.feedSession(t, srv); s.Consent.Required {
		t.Fatalf("a refused switch must leave the feed without the consent: %+v", s.Consent)
	}

	// ── complete profile: switched on, the profile says so ─────────────────
	f.completeProfile(t, ctx)
	code, raw = consentAuthed(t, srv, f.ownerID, http.MethodPatch, display, `{"consent_required": true}`)
	if code != http.StatusOK {
		t.Fatalf("consent on: %d %s", code, raw)
	}
	p := decodeSellerProfile(t, raw)
	if len(p.Channels) != 1 || !p.Channels[0].ConsentRequired || !p.Channels[0].ConsentActive {
		t.Fatalf("profile channels after the switch: %+v", p.Channels)
	}
	code, raw = consentAuthed(t, srv, f.ownerID, http.MethodGet, base+"/seller-profile", "")
	if code != http.StatusOK || !decodeSellerProfile(t, raw).Channels[0].ConsentRequired {
		t.Fatalf("GET profile: %d %s", code, raw)
	}

	// ── the feed session tells the widget ──────────────────────────────────
	s := f.feedSession(t, srv)
	if !s.Consent.Required || s.Consent.SellerLegalName != "Consent Teatro S.L." ||
		s.Consent.TermsURL != "https://site.arena-integration.test/legal/buyer-terms" ||
		s.Consent.PrivacyURL != "https://site.arena-integration.test/legal/privacy" {
		t.Fatalf("feed consent = %+v", s.Consent)
	}
	if !s.ShowWithdrawalNotice {
		t.Fatal("a Spanish seller shows the withdrawal notice")
	}

	// ── without the tick: 422 before any hold ─────────────────────────────
	for _, c := range []map[string]any{nil, {"accepted": false}} {
		code, raw = f.startWithConsent(t, srv, c)
		if code != http.StatusUnprocessableEntity || buyerFieldsErrorCode(t, raw) != "checkout.consent_required" {
			t.Fatalf("checkout without consent %v: %d %s", c, code, raw)
		}
		if fp := f.footprint(t, ctx); fp.reservations != 0 || fp.unavailable != 0 {
			t.Fatalf("a refused checkout took inventory: %+v", fp)
		}
	}
	if n := countConsents(t, ctx, pool, f.orgID); n != 0 {
		t.Fatalf("a refused checkout wrote %d consents", n)
	}

	// ── with the tick: 201 and the evidence row ───────────────────────────
	before := time.Now().Add(-time.Second)
	code, raw = f.startWithConsent(t, srv, map[string]any{"accepted": true, "locale": "es"})
	if code != http.StatusCreated {
		t.Fatalf("checkout with consent: %d %s", code, raw)
	}
	if strings.Contains(string(raw), "203.0.113.42") {
		t.Fatalf("checkout/start answers the buyer's address: %s", raw)
	}
	orderID := orderIDOfStart(t, ctx, pool, raw)

	code, raw = consentAuthed(t, srv, f.ownerID, http.MethodGet, base+"/orders/"+orderID.String()+"/consents", "")
	if code != http.StatusOK {
		t.Fatalf("GET consents: %d %s", code, raw)
	}
	var got struct {
		OrderID  string           `json:"order_id"`
		Consents []consentRowWire `json:"consents"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode consents: %v %s", err, raw)
	}
	if got.OrderID != orderID.String() || len(got.Consents) != 1 {
		t.Fatalf("consents = %+v", got)
	}
	c := got.Consents[0]
	if c.Kind != "sale_terms" || c.TermsVersion != "platform-terms:2026-10-15;seller:"+f.orgID.String() {
		t.Errorf("kind/version = %q / %q", c.Kind, c.TermsVersion)
	}
	if c.ClientIP == nil || *c.ClientIP != "203.0.113.42" {
		t.Errorf("client_ip = %v, want 203.0.113.42 (the request's RemoteAddr)", c.ClientIP)
	}
	if c.UserAgent == nil || *c.UserAgent != consentTestUA {
		t.Errorf("user_agent = %v", c.UserAgent)
	}
	if c.Locale == nil || *c.Locale != "es" {
		t.Errorf("locale = %v, want es", c.Locale)
	}
	if c.WithdrawalNoticeShown != s.ShowWithdrawalNotice {
		t.Errorf("withdrawal_notice_shown = %v, the feed answered %v", c.WithdrawalNoticeShown, s.ShowWithdrawalNotice)
	}
	if at, err := time.Parse(time.RFC3339, c.AcceptedAt); err != nil || at.Before(before.Truncate(time.Second)) {
		t.Errorf("accepted_at = %q (%v)", c.AcceptedAt, err)
	}

	// ── the evidence never reaches an audit row ───────────────────────────
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%203.0.113.42%' OR metadata::text LIKE '%P2 consent test%'`).Scan(&leaked); err != nil {
		t.Fatalf("scan audit: %v", err)
	}
	if leaked != 0 {
		t.Errorf("%d audit rows carry the buyer's address or browser", leaked)
	}

	// ── another organization's owner: the route's own 404, and 403 in A ──
	other := newBotInviteFixture(t, pool)
	code, raw = consentAuthed(t, srv, other.ownerID, http.MethodGet,
		"/v1/organizations/"+other.orgID.String()+"/orders/"+orderID.String()+"/consents", "")
	if code != http.StatusNotFound || buyerFieldsErrorCode(t, raw) != "orders.not_found" {
		t.Fatalf("foreign order through the caller's own org: %d %s", code, raw)
	}
	code, raw = consentAuthed(t, srv, other.ownerID, http.MethodGet, base+"/orders/"+orderID.String()+"/consents", "")
	if code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("a non-member read another organization's consents: %d %s", code, raw)
	}
	if strings.Contains(string(raw), "203.0.113.42") {
		t.Fatalf("a refusal leaked the address: %s", raw)
	}

	// ── switching it off works without data checks ────────────────────────
	code, raw = consentAuthed(t, srv, f.ownerID, http.MethodPatch, display, `{"consent_required": false}`)
	if code != http.StatusOK || decodeSellerProfile(t, raw).Channels[0].ConsentRequired {
		t.Fatalf("consent off: %d %s", code, raw)
	}
}

func TestCheckoutConsent_FlagOffNeedsNoConsentButKeepsAGivenOne(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	srv := buildConsentServer(t, pool)
	f := newConsentFixture(t, ctx, pool, "cs-off")
	defer f.close()
	f.completeProfile(t, ctx)

	if s := f.feedSession(t, srv); s.Consent.Required || s.Consent.SellerLegalName != "" {
		t.Fatalf("flag off: feed consent = %+v", s.Consent)
	}
	code, raw := f.startWithConsent(t, srv, nil)
	if code != http.StatusCreated {
		t.Fatalf("checkout without consent, flag off: %d %s", code, raw)
	}
	if n := countConsents(t, ctx, pool, f.orgID); n != 0 {
		t.Fatalf("no consent sent, yet %d rows", n)
	}

	// A tick the buyer gave anyway is still a consent: recorded.
	code, raw = f.startWithConsent(t, srv, map[string]any{"accepted": true})
	if code != http.StatusCreated {
		t.Fatalf("checkout with an unrequested consent: %d %s", code, raw)
	}
	if n := countConsents(t, ctx, pool, f.orgID); n != 1 {
		t.Fatalf("an unrequested tick was not recorded: %d rows", n)
	}
	var locale *string
	if err := pool.QueryRow(ctx, `SELECT locale FROM checkout_consents WHERE org_id = $1`, f.orgID).Scan(&locale); err != nil {
		t.Fatalf("read locale: %v", err)
	}
	if locale == nil || *locale != "ru" {
		t.Errorf("locale = %v, want the checkout's own ru", locale)
	}
}

// TestCheckoutConsent_OrderConsentsStayInsideTheOrganization is the
// org-isolation proof the sweep table names for GET .../orders/{order_id}/consents.
func TestCheckoutConsent_OrderConsentsStayInsideTheOrganization(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	srv := buildConsentServer(t, pool)
	a := newConsentFixture(t, ctx, pool, "cs-iso-a")
	defer a.close()
	b := newConsentFixture(t, ctx, pool, "cs-iso-b")
	defer b.close()

	code, raw := a.startWithConsent(t, srv, map[string]any{"accepted": true})
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %s", code, raw)
	}
	orderA := orderIDOfStart(t, ctx, pool, raw)

	// B's owner, through B's own path with A's order: 404, no data.
	code, raw = consentAuthed(t, srv, b.ownerID, http.MethodGet,
		"/v1/organizations/"+b.orgID.String()+"/orders/"+orderA.String()+"/consents", "")
	if code != http.StatusNotFound || strings.Contains(string(raw), "203.0.113.42") {
		t.Fatalf("B read A's consents: %d %s", code, raw)
	}
	// Control: A's owner reads them.
	code, raw = consentAuthed(t, srv, a.ownerID, http.MethodGet,
		"/v1/organizations/"+a.orgID.String()+"/orders/"+orderA.String()+"/consents", "")
	if code != http.StatusOK || !strings.Contains(string(raw), "203.0.113.42") {
		t.Fatalf("A's own read: %d %s", code, raw)
	}
}
