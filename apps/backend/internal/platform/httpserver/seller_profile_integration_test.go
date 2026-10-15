//go:build integration

package httpserver

// Proof of the seller-profile routes (HP-4a) through the real router: the
// owner reads the profile and fills it, the switch refuses while data is
// missing and says what, a malformed e-mail or phone is refused, a personal
// mailbox is accepted with a hint, every other channel setting survives, the
// manager (organizer) is refused, and an owner of ANOTHER organization cannot
// reach this organization's channel.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hseller"
)

type sellerCall func(asUser uuid.UUID, method, path, body string) (int, []byte)

func sellerCaller(t *testing.T, srv *Server) sellerCall {
	return func(asUser uuid.UUID, method, path, body string) (int, []byte) {
		t.Helper()
		token, _, err := auth.IssueJWT(botTestJWTSecret, asUser, nil, nil, botTestJWTIssuer, botTestJWTAudience, time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		var rd *strings.Reader
		if body != "" {
			rd = strings.NewReader(body)
		} else {
			rd = strings.NewReader("")
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
}

// sellerChannelFixture adds a hosted-page channel (with unrelated settings
// that must survive every write) and a plain channel without a hosted page.
func sellerChannelFixture(t *testing.T, f *botInviteFixture, name string) (hosted, plain uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	hosted, plain = uuid.New(), uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, $3,
		'{"hosted_page": {"enabled": true, "keep": 1}, "other_key": "stays"}'::jsonb)`, hosted, f.orgID, name+" hosted"); err != nil {
		t.Fatalf("insert hosted channel: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, plain, f.orgID, name+" plain"); err != nil {
		t.Fatalf("insert plain channel: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.pool.Exec(c, `DELETE FROM audit_events WHERE action IN ('v1.channel.seller_display_update', 'v1.organization.contact_form_update') AND metadata->>'org_id' = $1`, f.orgID.String())
		_, _ = f.pool.Exec(c, `DELETE FROM sales_channels WHERE org_id = $1`, f.orgID)
	})
	return hosted, plain
}

func decodeSellerProfile(t *testing.T, raw []byte) hseller.Profile {
	t.Helper()
	var env struct {
		SellerProfile hseller.Profile `json:"seller_profile"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode profile: %v %s", err, raw)
	}
	return env.SellerProfile
}

func TestSellerProfile_OwnerFillsAndSwitches(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	call := sellerCaller(t, srv)
	ctx := context.Background()
	hosted, plain := sellerChannelFixture(t, f, "HP4a")
	base := "/v1/organizations/" + f.orgID.String()
	display := base + "/channels/" + hosted.String() + "/seller-display"

	// ── an empty profile: one hosted channel, everything required is missing ──
	code, raw := call(f.ownerID, http.MethodGet, base+"/seller-profile", "")
	if code != http.StatusOK {
		t.Fatalf("GET profile: %d %s", code, raw)
	}
	p := decodeSellerProfile(t, raw)
	if len(p.Channels) != 1 || p.Channels[0].ID != hosted.String() || p.Channels[0].ShowSeller || p.Channels[0].PublicContact != nil {
		t.Fatalf("channels = %+v", p.Channels)
	}
	wantMissing := []string{"legal_name", "tax_id", "address_line1", "address_city", "address_postal_code", "public_email"}
	if !reflect.DeepEqual(p.Channels[0].Missing, wantMissing) {
		t.Errorf("channel missing = %v, want %v", p.Channels[0].Missing, wantMissing)
	}
	if !reflect.DeepEqual(p.Missing, wantMissing[:5]) {
		t.Errorf("org missing = %v", p.Missing)
	}
	if p.Complete || p.SellerShown || p.Channels[0].Complete || p.Channels[0].SellerShown || p.Channels[0].SellerHidden {
		t.Errorf("an empty profile is neither complete nor shown: %+v", p)
	}

	// ── the switch refuses and says what is missing; nothing is written ──────
	code, raw = call(f.ownerID, http.MethodPatch, display, `{"show_seller": true}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), `"seller.incomplete"`) || !strings.Contains(string(raw), `"public_email"`) {
		t.Fatalf("show while empty: %d %s", code, raw)
	}
	var shown bool
	_ = pool.QueryRow(ctx, `SELECT COALESCE(settings #>> '{hosted_page,show_seller}', '') = 'true' FROM sales_channels WHERE id = $1`, hosted).Scan(&shown)
	if shown {
		t.Fatal("a refused switch must not write")
	}

	// ── malformed contact data ────────────────────────────────────────────────
	if code, raw = call(f.ownerID, http.MethodPatch, display, `{"public_contact": {"email": "not-an-email"}}`); code != http.StatusBadRequest || !strings.Contains(string(raw), "seller.invalid_email") {
		t.Errorf("bad e-mail: %d %s", code, raw)
	}
	if code, raw = call(f.ownerID, http.MethodPatch, display, `{"public_contact": {"phone": "call me"}}`); code != http.StatusBadRequest || !strings.Contains(string(raw), "seller.invalid_phone") {
		t.Errorf("bad phone: %d %s", code, raw)
	}
	if code, raw = call(f.ownerID, http.MethodPatch, display, `{"unknown": 1}`); code != http.StatusBadRequest {
		t.Errorf("unknown key: %d %s", code, raw)
	}
	// A channel without a hosted page is not reachable through this route.
	if code, raw = call(f.ownerID, http.MethodPatch, base+"/channels/"+plain.String()+"/seller-display", `{"show_seller": false}`); code != http.StatusNotFound {
		t.Errorf("plain channel: %d %s", code, raw)
	}

	// ── the legal data through PATCH /v1/organizations/{id} ───────────────────
	if code, raw = call(f.ownerID, http.MethodPatch, "/v1/organizations/"+f.orgID.String(), `{"legal_form": "`+strings.Repeat("x", 81)+`"}`); code != http.StatusBadRequest || !strings.Contains(string(raw), "org.invalid_legal_form") {
		t.Errorf("long legal form: %d %s", code, raw)
	}
	legal := `{"legal_name": "Teatro Ejemplo S.L.", "legal_form": " S.L. ", "registration_number": "Hoja M-1",
		"tax_id_scheme": "eu_vat", "tax_id": "ESB12345678", "legal_address_line1": "Calle Mayor 1",
		"legal_address_city": "Madrid", "legal_address_postal_code": "28013", "legal_address_country": "es"}`
	code, raw = call(f.ownerID, http.MethodPatch, "/v1/organizations/"+f.orgID.String(), legal)
	if code != http.StatusOK || !strings.Contains(string(raw), `"legal_form":"S.L."`) {
		t.Fatalf("legal PATCH: %d %s", code, raw)
	}
	if code, raw = call(f.ownerID, http.MethodGet, "/v1/organizations/"+f.orgID.String(), ""); code != http.StatusOK || !strings.Contains(string(raw), `"legal_form":"S.L."`) {
		t.Errorf("GET organization must carry legal_form: %d %s", code, raw)
	}

	// ── a personal mailbox is accepted with a hint, lower-cased ───────────────
	code, raw = call(f.ownerID, http.MethodPatch, display, `{"public_contact": {"email": " Teatro.Tickets@Gmail.com ", "phone": "+34 600 111 222"}}`)
	if code != http.StatusOK {
		t.Fatalf("contact: %d %s", code, raw)
	}
	p = decodeSellerProfile(t, raw)
	ch := p.Channels[0]
	if ch.PublicContact == nil || ch.PublicContact.Email != "teatro.tickets@gmail.com" || ch.PublicContact.Phone != "+34 600 111 222" || !ch.EmailLooksPersonal || len(ch.Missing) != 0 {
		t.Fatalf("after contact: %+v", ch)
	}
	if p.LegalForm == nil || *p.LegalForm != "S.L." {
		t.Errorf("profile legal_form = %v", p.LegalForm)
	}
	// P1a: complete data is shown with no switch at all.
	if !p.Complete || !p.SellerShown || !ch.Complete || !ch.SellerShown || ch.ShowSeller {
		t.Errorf("a complete profile must be shown by default: %+v", p)
	}

	// ── now the switch goes on, and every other setting survives ──────────────
	if code, raw = call(f.ownerID, http.MethodPatch, display, `{"show_seller": true}`); code != http.StatusOK {
		t.Fatalf("show: %d %s", code, raw)
	}
	var settings string
	if err := pool.QueryRow(ctx, `SELECT settings::text FROM sales_channels WHERE id = $1`, hosted).Scan(&settings); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"show_seller": true`, `"keep": 1`, `"enabled": true`, `"other_key": "stays"`, `"email": "teatro.tickets@gmail.com"`} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings lack %s: %s", want, settings)
		}
	}
	// The public seller block now carries the legal form (GetHostedPageSeller
	// needs an active feed token of the channel).
	token := "ft_hp4a_" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO agent_feed_tokens (sales_channel_id, token, label) VALUES ($1, $2, 'hp4a')`, hosted, token); err != nil {
		t.Fatalf("feed token: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_feed_tokens WHERE token = $1`, token)
	})
	if row, err := gen.New(pool).GetHostedPageSeller(ctx, f.orgID, token); err != nil || !row.ShowSeller || row.LegalForm == nil || *row.LegalForm != "S.L." {
		t.Errorf("GetHostedPageSeller = %+v, %v", row, err)
	}

	// ── taking the e-mail away while shown is refused; the phone alone is fine ─
	if code, raw = call(f.ownerID, http.MethodPatch, display, `{"public_contact": {"email": null}}`); code != http.StatusUnprocessableEntity {
		t.Errorf("clear e-mail while shown: %d %s", code, raw)
	}
	if code, raw = call(f.ownerID, http.MethodPatch, display, `{"public_contact": {"phone": null}}`); code != http.StatusOK {
		t.Errorf("clear phone: %d %s", code, raw)
	}
	if code, raw = call(f.ownerID, http.MethodPatch, display, `{"show_seller": false, "public_contact": null}`); code != http.StatusOK {
		t.Errorf("hide and remove contact: %d %s", code, raw)
	}
	if err := pool.QueryRow(ctx, `SELECT settings::text FROM sales_channels WHERE id = $1`, hosted).Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(settings, "public_contact") || !strings.Contains(settings, `"show_seller": false`) || !strings.Contains(settings, `"other_key": "stays"`) {
		t.Errorf("after removal: %s", settings)
	}

	// ── the audit trail names what changed, never the address ─────────────────
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'v1.channel.seller_display_update' AND resource_id = $1`, hosted.String()).Scan(&n); err != nil || n < 4 {
		t.Errorf("audit rows = %d (%v)", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'v1.channel.seller_display_update' AND resource_id = $1 AND metadata::text ILIKE '%gmail%'`, hosted.String()).Scan(&n); err != nil || n != 0 {
		t.Errorf("an audit row carries the e-mail: %d (%v)", n, err)
	}
}

func TestSellerProfile_StaysInsideTheOrganization(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	call := sellerCaller(t, srv)
	ctx := context.Background()
	hosted, _ := sellerChannelFixture(t, f, "HP4a iso")
	otherHosted, _ := sellerChannelFixture(t, other, "HP4a iso other")

	// A manager (organizer) of the SAME organization holds no org.update.
	q := gen.New(pool)
	managerEmail := f.newEmail("manager")
	manager, err := q.InsertUser(ctx, managerEmail, "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if _, err := q.InsertMembership(ctx, manager.ID, f.orgID, "organizer"); err != nil {
		t.Fatalf("InsertMembership: %v", err)
	}
	base := "/v1/organizations/" + f.orgID.String()
	if code, raw := call(manager.ID, http.MethodGet, base+"/seller-profile", ""); code != http.StatusForbidden {
		t.Errorf("manager GET: %d %s", code, raw)
	}
	if code, raw := call(manager.ID, http.MethodPatch, base+"/channels/"+hosted.String()+"/seller-display", `{"show_seller": false}`); code != http.StatusForbidden {
		t.Errorf("manager PATCH: %d %s", code, raw)
	}
	if code, raw := call(manager.ID, http.MethodPatch, "/v1/organizations/"+f.orgID.String(), `{"legal_form": "S.L."}`); code != http.StatusForbidden {
		t.Errorf("manager legal PATCH: %d %s", code, raw)
	}

	// The owner of ANOTHER organization: refused in this one, and this
	// organization's channel id is "not found" under their own.
	if code, raw := call(other.ownerID, http.MethodGet, base+"/seller-profile", ""); code != http.StatusForbidden {
		t.Errorf("foreign owner GET: %d %s", code, raw)
	}
	if code, raw := call(other.ownerID, http.MethodPatch, base+"/channels/"+hosted.String()+"/seller-display", `{"public_contact": {"email": "x@evil.example"}}`); code != http.StatusForbidden {
		t.Errorf("foreign owner PATCH in victim org: %d %s", code, raw)
	}
	if code, raw := call(other.ownerID, http.MethodPatch, "/v1/organizations/"+other.orgID.String()+"/channels/"+hosted.String()+"/seller-display", `{"public_contact": {"email": "x@evil.example"}}`); code != http.StatusNotFound {
		t.Errorf("foreign channel under own org: %d %s", code, raw)
	}
	var settings string
	if err := pool.QueryRow(ctx, `SELECT settings::text FROM sales_channels WHERE id = $1`, hosted).Scan(&settings); err != nil || strings.Contains(settings, "evil") {
		t.Errorf("the victim channel changed: %s (%v)", settings, err)
	}
	// Their own channel still works.
	if code, raw := call(other.ownerID, http.MethodPatch, "/v1/organizations/"+other.orgID.String()+"/channels/"+otherHosted.String()+"/seller-display", `{"public_contact": {"email": "tickets@own.example"}}`); code != http.StatusOK {
		t.Errorf("own channel: %d %s", code, raw)
	}
}
