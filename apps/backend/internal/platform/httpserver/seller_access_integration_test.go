//go:build integration

package httpserver

// Who may touch the seller data (HP-4a review, 2026-10-13): an organization
// API key — even one holding org.update and channel.update — can neither read
// nor change the seller profile, nor the legal identity and contact of the
// organization, nor slip a public contact in through the generic channel
// PATCH; the agent and the manager are refused; a platform superadmin needs
// X-Admin-Reason. And a seller block never stays on after a required legal
// field is emptied.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

type sellerAccessFixture struct {
	srv     *Server
	ts      *httptest.Server
	secret  string
	pf      *prom0113Fixture
	org     uuid.UUID
	channel uuid.UUID
}

func newSellerAccessFixture(t *testing.T) *sellerAccessFixture {
	t.Helper()
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	ctx := context.Background()
	q := gen.New(srv.pgxPool)
	creator, err := q.InsertUser(ctx, "seller-access-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: q, userID: creator.ID}
	f := &sellerAccessFixture{srv: srv, ts: ts, secret: secret, pf: pf, org: pf.org(t, "SELLERACC"), channel: uuid.New()}
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, 'Hosted',
		'{"hosted_page":{"enabled":true,"show_seller":true,"public_contact":{"email":"tickets@teatro.example"}}}'::jsonb)`, f.channel, f.org); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE organizations SET legal_name='Teatro S.L.', tax_id='B12345678', tax_id_scheme='other',
		legal_address_line1='Calle 1', legal_address_city='Madrid', legal_address_postal_code='28001' WHERE id=$1`, f.org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM audit_events WHERE metadata->>'org_id' = $1`, f.org.String())
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM api_keys WHERE org_id = $1`, f.org)
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM memberships WHERE org_id = $1`, f.org)
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM sales_channels WHERE org_id = $1`, f.org)
	})
	return f
}

// member creates a user holding role in the organization and returns a JWT.
func (f *sellerAccessFixture) member(t *testing.T, role string) string {
	t.Helper()
	ctx := context.Background()
	u, err := f.pf.q.InsertUser(ctx, "seller-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	if role == "superadmin" {
		if _, err := f.srv.pgxPool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
			SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, u.ID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = f.srv.pgxPool.Exec(context.Background(), `DELETE FROM user_roles WHERE user_id = $1`, u.ID)
		})
	} else if _, err := f.pf.q.InsertMembership(ctx, u.ID, f.org, role); err != nil {
		t.Fatal(err)
	}
	tok, _, err := auth.IssueJWT(f.secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (f *sellerAccessFixture) do(t *testing.T, method, path, bearer, reason, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.ts.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if reason != "" {
		req.Header.Set("X-Admin-Reason", reason)
	}
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (f *sellerAccessFixture) setting(t *testing.T, path string) string {
	t.Helper()
	var v *string
	if err := f.srv.pgxPool.QueryRow(context.Background(), `SELECT settings #>> $2::text[] FROM sales_channels WHERE id = $1`,
		f.channel, "{"+path+"}").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v == nil {
		return "<nil>"
	}
	return *v
}

func TestSellerAccess_APIKeyIsRefusedEverywhere(t *testing.T) {
	f := newSellerAccessFixture(t)
	key := f.pf.key(t, f.org, "org.update", "channel.update", "channel.read")
	base := "/v1/organizations/" + f.org.String()

	for _, c := range []struct {
		name, method, path, body, code string
		status                         int
	}{
		{"read profile", http.MethodGet, base + "/seller-profile", "", "seller.api_key_forbidden", http.StatusForbidden},
		{"telegram switch", http.MethodPatch, base + "/seller-profile", `{"contact_form_to_telegram":false}`, "seller.api_key_forbidden", http.StatusForbidden},
		{"public contact", http.MethodPatch, base + "/channels/" + f.channel.String() + "/seller-display",
			`{"public_contact":{"email":"attacker@evil.example"}}`, "seller.api_key_forbidden", http.StatusForbidden},
		{"legal name", http.MethodPatch, base, `{"legal_name":"Evil Ltd"}`, "org.identity_fields_api_key_forbidden", http.StatusForbidden},
		{"contact e-mail", http.MethodPatch, base, `{"contact_email":"attacker@evil.example"}`, "org.identity_fields_api_key_forbidden", http.StatusForbidden},
	} {
		st, out := f.do(t, c.method, c.path, key, "", c.body)
		if st != c.status || prom0113Code(out) != c.code {
			t.Errorf("%s: %d %v, want %d %s", c.name, st, out, c.status, c.code)
		}
	}
	if got := f.setting(t, "hosted_page,public_contact,email"); got != "tickets@teatro.example" {
		t.Errorf("public contact changed to %q", got)
	}
	var legal string
	_ = f.srv.pgxPool.QueryRow(context.Background(), `SELECT legal_name FROM organizations WHERE id=$1`, f.org).Scan(&legal)
	if legal != "Teatro S.L." {
		t.Errorf("legal name changed to %q", legal)
	}

	// The key still renames the organization, as before.
	if st, out := f.do(t, http.MethodPatch, base, key, "", `{"name":"Teatro renamed `+uuid.NewString()[:6]+`"}`); st != http.StatusOK {
		t.Errorf("plain rename by key: %d %v", st, out)
	}

	// The generic channel PATCH keeps the stored seller keys whatever it is sent.
	st, out := f.do(t, http.MethodPatch, base+"/channels/"+f.channel.String(), key, "",
		`{"settings":{"hosted_page":{"enabled":true,"show_seller":false,"public_contact":{"email":"attacker@evil.example"}},"extra":1}}`)
	if st != http.StatusOK {
		t.Fatalf("channel PATCH: %d %v", st, out)
	}
	if got := f.setting(t, "hosted_page,public_contact,email"); got != "tickets@teatro.example" {
		t.Errorf("channel PATCH replaced the public contact with %q", got)
	}
	if got := f.setting(t, "hosted_page,show_seller"); got != "true" {
		t.Errorf("channel PATCH changed show_seller to %q", got)
	}
	if got := f.setting(t, "extra"); got != "1" {
		t.Errorf("other settings must still be written, extra = %q", got)
	}
}

func TestSellerAccess_RolesAndSuperadmin(t *testing.T) {
	f := newSellerAccessFixture(t)
	path := "/v1/organizations/" + f.org.String() + "/seller-profile"
	display := "/v1/organizations/" + f.org.String() + "/channels/" + f.channel.String() + "/seller-display"
	for _, role := range []string{"agent", "organizer"} {
		if st, out := f.do(t, http.MethodGet, path, f.member(t, role), "", ""); st != http.StatusForbidden {
			t.Errorf("%s GET: %d %v, want 403", role, st, out)
		}
	}
	sa := f.member(t, "superadmin")
	if st, out := f.do(t, http.MethodGet, path, sa, "", ""); st != http.StatusBadRequest || prom0113Code(out) != "superadmin.missing_reason" {
		t.Errorf("superadmin without reason: %d %v", st, out)
	}
	if st, out := f.do(t, http.MethodGet, path, sa, "seller data check", ""); st != http.StatusOK {
		t.Errorf("superadmin with reason GET: %d %v", st, out)
	}
	if st, out := f.do(t, http.MethodPatch, display, sa, "seller data check", `{"public_contact":{"email":"box@teatro.example"}}`); st != http.StatusOK {
		t.Errorf("superadmin with reason PATCH: %d %v", st, out)
	}
	if got := f.setting(t, "hosted_page,public_contact,email"); got != "box@teatro.example" {
		t.Errorf("superadmin write = %q", got)
	}
}

func TestSellerAccess_ClearingARequiredFieldHidesTheBlock(t *testing.T) {
	f := newSellerAccessFixture(t)
	owner := f.member(t, "org_admin")
	base := "/v1/organizations/" + f.org.String()

	// An optional field keeps the block on.
	if st, out := f.do(t, http.MethodPatch, base, owner, "", `{"legal_form":"S.L."}`); st != http.StatusOK || out["seller_display_hidden"] != nil {
		t.Fatalf("legal form: %d %v", st, out)
	}
	if got := f.setting(t, "hosted_page,show_seller"); got != "true" {
		t.Fatalf("show_seller after an optional field = %q", got)
	}
	var form string
	_ = f.srv.pgxPool.QueryRow(context.Background(), `SELECT legal_form FROM organizations WHERE id=$1`, f.org).Scan(&form)
	if form != "S.L." {
		t.Errorf("legal_form = %q", form)
	}

	// Emptying the tax number turns the block off and says on which channel.
	st, out := f.do(t, http.MethodPatch, base, owner, "", `{"tax_id":null,"tax_id_scheme":null}`)
	if st != http.StatusOK {
		t.Fatalf("clear tax: %d %v", st, out)
	}
	hidden, _ := out["seller_display_hidden"].([]any)
	if len(hidden) != 1 || hidden[0] != f.channel.String() {
		t.Errorf("seller_display_hidden = %v", out["seller_display_hidden"])
	}
	if got := f.setting(t, "hosted_page,show_seller"); got != "false" {
		t.Errorf("show_seller after clearing the tax number = %q", got)
	}
	if !strings.Contains(f.setting(t, "hosted_page,public_contact,email"), "tickets@") {
		t.Error("the public contact must stay")
	}
}
