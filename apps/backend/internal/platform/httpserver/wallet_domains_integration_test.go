//go:build integration

// wallet_domains_integration_test.go - spec 65 section 5 (task E3): a Stripe
// payment configuration registers the wallet domains in the organization's
// Stripe account, the organizer lists them, and a sales channel lists the
// hosts of its own sites. Stripe is a stub reached through StripeAPIBaseURL.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// walletStubStripe answers the three Stripe calls a save makes: the balance
// check of the credential and the payment_method_domains list and create.
type walletStubStripe struct {
	mu       sync.Mutex
	server   *httptest.Server
	domains  map[string]string // domain_name -> pmd id
	failPMD  bool
	pmdCalls int
}

func newWalletStubStripe(t *testing.T) *walletStubStripe {
	t.Helper()
	s := &walletStubStripe{domains: map[string]string{}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/balance"):
			_, _ = w.Write([]byte(`{"object":"balance","available":[]}`))
		case strings.Contains(r.URL.Path, "/payment_method_domains"):
			s.pmdCalls++
			if s.failPMD {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"stub is down"}}`))
				return
			}
			if r.Method == http.MethodGet {
				name := r.URL.Query().Get("domain_name")
				if id, ok := s.domains[name]; ok {
					_, _ = w.Write([]byte(`{"data":[{"id":"` + id + `","domain_name":"` + name +
						`","enabled":true,"apple_pay":{"status":"active"},"google_pay":{"status":"active"},"link":{"status":"active"}}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			}
			_ = r.ParseForm()
			name := r.PostForm.Get("domain_name")
			id := "pmd_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
			s.domains[name] = id
			_, _ = w.Write([]byte(`{"id":"` + id + `","domain_name":"` + name +
				`","enabled":true,"apple_pay":{"status":"active"},"google_pay":{"status":"active"},"link":{"status":"inactive","status_details":{"error_message":"link is not set up"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *walletStubStripe) setFail(v bool) {
	s.mu.Lock()
	s.failPMD = v
	s.mu.Unlock()
}

type walletDomainJSON struct {
	Domain          string  `json:"domain"`
	Source          string  `json:"source"`
	Enabled         *bool   `json:"enabled"`
	ApplePayStatus  *string `json:"apple_pay_status"`
	GooglePayStatus *string `json:"google_pay_status"`
	LinkStatus      *string `json:"link_status"`
	StatusDetail    *string `json:"status_detail"`
	LastError       *string `json:"last_error"`
	CheckedAt       *string `json:"checked_at"`
}

func decodeWalletDomains(t *testing.T, body string) []walletDomainJSON {
	t.Helper()
	var out struct {
		Domains []walletDomainJSON `json:"payment_method_domains"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out.Domains
}

func TestWalletDomains_StripeConfigRegistersPlatformAndSiteDomains(t *testing.T) {
	stub := newWalletStubStripe(t)
	e := newSec1EnvCfg(t, func(c *config.Config) {
		c.StripeAPIBaseURL = stub.server.URL + "/v1"
		c.StripeWalletDomains = []string{"tickets.wallet-test.example"}
	})
	ctx := context.Background()
	org, other := e.org(t, "WD"), e.org(t, "WDX")
	owner := e.user(t, "wd-owner")
	e.member(t, owner, org, "org_admin")
	otherOwner := e.user(t, "wd-other")
	e.member(t, otherOwner, other, "org_admin")
	tok, otherTok := e.token(t, owner), e.token(t, otherOwner)
	base := "/v1/organizations/" + org.String()

	// A Stripe channel with a site host that is already listed.
	var chID uuid.UUID
	if err := e.srv.pgxPool.QueryRow(ctx,
		`INSERT INTO sales_channels (org_id, name, provider) VALUES ($1, 'WD channel', 'stripe') RETURNING id`, org).Scan(&chID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE job_type = 'payment_domains.sync' AND payload->>'org_id' = $1`, org.String())
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM payment_provider_configs WHERE org_id = $1`, org)
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM sales_channels WHERE id = $1`, chID)
	})

	// PUT normalizes, dedupes and queues the sync job.
	code, body := e.do(t, http.MethodPut, base+"/channels/"+chID.String()+"/site-domains", tok, "",
		`{"domains":["https://WWW.Site-One.example/tickets","www.site-one.example","site-two.example:8443"]}`)
	if code != http.StatusOK {
		t.Fatalf("PUT site-domains = %d %s", code, body)
	}
	var put struct {
		Domains []string `json:"domains"`
	}
	if err := json.Unmarshal([]byte(body), &put); err != nil || len(put.Domains) != 2 ||
		put.Domains[0] != "www.site-one.example" || put.Domains[1] != "site-two.example" {
		t.Fatalf("PUT answer %s, want two normalized hosts", body)
	}
	var jobs int
	if err := e.srv.pgxPool.QueryRow(ctx,
		`SELECT count(*) FROM worker_jobs WHERE job_type = 'payment_domains.sync' AND status = 'pending' AND payload->>'org_id' = $1`,
		org.String()).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("pending payment_domains.sync jobs = %d (%v), want 1", jobs, err)
	}
	// A second PUT does not stack another job.
	e.expect(t, http.StatusOK, http.MethodPut, base+"/channels/"+chID.String()+"/site-domains", tok, "", `{"domains":["site-two.example","www.site-one.example"]}`)
	if err := e.srv.pgxPool.QueryRow(ctx,
		`SELECT count(*) FROM worker_jobs WHERE job_type = 'payment_domains.sync' AND status = 'pending' AND payload->>'org_id' = $1`,
		org.String()).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("after a repeat: pending jobs = %d (%v), want 1", jobs, err)
	}

	// GET returns the stored list.
	code, body = e.do(t, http.MethodGet, base+"/channels/"+chID.String()+"/site-domains", tok, "", "")
	if code != http.StatusOK || !strings.Contains(body, `"site-two.example"`) {
		t.Fatalf("GET site-domains = %d %s", code, body)
	}

	// Bad hosts are refused with details and store nothing.
	for _, bad := range []string{`"127.0.0.1"`, `"localhost"`, `"intranet"`, `"-x.example.com"`, `"a_b.example.com"`} {
		code, body = e.do(t, http.MethodPut, base+"/channels/"+chID.String()+"/site-domains", tok, "", `{"domains":["ok.example.com",`+bad+`]}`)
		if code != http.StatusBadRequest || !strings.Contains(body, "channel.invalid_site_domain") || !strings.Contains(body, `"index":1`) {
			t.Errorf("PUT with %s = %d %s, want 400 channel.invalid_site_domain", bad, code, body)
		}
	}
	tooMany := `{"domains":["a1.example.com","a2.example.com","a3.example.com","a4.example.com","a5.example.com","a6.example.com","a7.example.com","a8.example.com","a9.example.com","a10.example.com","a11.example.com"]}`
	e.expect(t, http.StatusBadRequest, http.MethodPut, base+"/channels/"+chID.String()+"/site-domains", tok, "", tooMany)
	code, body = e.do(t, http.MethodGet, base+"/channels/"+chID.String()+"/site-domains", tok, "", "")
	if code != http.StatusOK || !strings.Contains(body, "www.site-one.example") || strings.Contains(body, "ok.example.com") {
		t.Fatalf("list after refusals = %d %s", code, body)
	}

	// Another organization's owner cannot read or write the channel's hosts,
	// and a missing channel is a 404.
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		payload := ""
		if m == http.MethodPut {
			payload = `{"domains":["evil.example.com"]}`
		}
		if code, body := e.do(t, m, base+"/channels/"+chID.String()+"/site-domains", otherTok, "", payload); code != http.StatusForbidden && code != http.StatusNotFound {
			t.Errorf("%s by another org's owner = %d %s, want 403 or 404", m, code, body)
		}
	}
	e.expect(t, http.StatusNotFound, http.MethodGet, base+"/channels/"+uuid.NewString()+"/site-domains", tok, "", "")

	// Creating a Stripe configuration registers the platform host and both site hosts.
	code, body = e.do(t, http.MethodPost, base+"/payment-configs", tok, "",
		`{"provider":"stripe","mode":"test","secrets":{"api_key":"sk_test_walletdomains0001"}}`)
	if code != http.StatusCreated {
		t.Fatalf("create stripe config = %d %s", code, body)
	}
	var created struct {
		Config struct {
			ID                 string `json:"id"`
			VerificationStatus string `json:"verification_status"`
		} `json:"payment_config"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.Config.ID == "" {
		t.Fatalf("create answer %s: %v", body, err)
	}
	if created.Config.VerificationStatus != "ok" {
		t.Fatalf("verification_status = %q, want ok (stub balance answers 200)", created.Config.VerificationStatus)
	}
	if strings.Contains(body, "sk_test_walletdomains0001") {
		t.Fatalf("the secret key leaked into the create answer")
	}
	cfgPath := base + "/payment-configs/" + created.Config.ID + "/payment-method-domains"
	code, body = e.do(t, http.MethodGet, cfgPath, tok, "", "")
	if code != http.StatusOK {
		t.Fatalf("GET payment-method-domains = %d %s", code, body)
	}
	rows := decodeWalletDomains(t, body)
	if len(rows) != 3 {
		t.Fatalf("domains = %s, want platform + two site hosts", body)
	}
	if rows[0].Domain != "tickets.wallet-test.example" || rows[0].Source != "platform" {
		t.Errorf("first row = %+v, want the platform host", rows[0])
	}
	for _, r := range rows {
		if r.Enabled == nil || !*r.Enabled || r.ApplePayStatus == nil || *r.ApplePayStatus != "active" ||
			r.LinkStatus == nil || *r.LinkStatus != "inactive" || r.StatusDetail == nil || r.LastError != nil || r.CheckedAt == nil {
			t.Errorf("row %+v: want enabled, apple active, link inactive with detail, no error, checked_at set", r)
		}
	}
	if rows[1].Source != "site" || rows[2].Source != "site" {
		t.Errorf("site rows = %+v %+v", rows[1], rows[2])
	}

	// Another organization sees nothing of this configuration.
	if code, body := e.do(t, http.MethodGet, cfgPath, otherTok, "", ""); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("another org's owner reads the domains = %d %s, want 403 or 404", code, body)
	}
	e.expect(t, http.StatusNotFound, http.MethodGet, base+"/payment-configs/"+uuid.NewString()+"/payment-method-domains", tok, "", "")

	// Verify answers with the same list.
	code, body = e.do(t, http.MethodPost, base+"/payment-configs/"+created.Config.ID+"/verify", tok, "", "")
	if code != http.StatusOK {
		t.Fatalf("verify = %d %s", code, body)
	}
	if got := decodeWalletDomains(t, body); len(got) != 3 {
		t.Errorf("verify answer carries %d domains, want 3: %s", len(got), body)
	}
}

func TestWalletDomains_StripeFailureNeverBlocksTheSave(t *testing.T) {
	stub := newWalletStubStripe(t)
	stub.setFail(true)
	e := newSec1EnvCfg(t, func(c *config.Config) {
		c.StripeAPIBaseURL = stub.server.URL + "/v1"
		c.StripeWalletDomains = []string{"tickets.wallet-fail.example"}
	})
	org := e.org(t, "WDF")
	owner := e.user(t, "wdf-owner")
	e.member(t, owner, org, "org_admin")
	tok := e.token(t, owner)
	base := "/v1/organizations/" + org.String()
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM payment_provider_configs WHERE org_id = $1`, org)
	})

	code, body := e.do(t, http.MethodPost, base+"/payment-configs", tok, "",
		`{"provider":"stripe","mode":"test","secrets":{"api_key":"sk_test_walletdomains0002"}}`)
	if code != http.StatusCreated {
		t.Fatalf("create with a failing domain endpoint = %d %s, want 201", code, body)
	}
	var created struct {
		Config struct {
			ID string `json:"id"`
		} `json:"payment_config"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cfgPath := base + "/payment-configs/" + created.Config.ID + "/payment-method-domains"
	_, body = e.do(t, http.MethodGet, cfgPath, tok, "", "")
	rows := decodeWalletDomains(t, body)
	if len(rows) != 1 || rows[0].LastError == nil || *rows[0].LastError == "" || rows[0].Enabled != nil {
		t.Fatalf("rows = %s, want one row with last_error and no statuses", body)
	}
	if strings.Contains(*rows[0].LastError, "sk_test_") {
		t.Errorf("last_error carries the key: %s", *rows[0].LastError)
	}

	// The next check with a healthy Stripe clears the error.
	stub.setFail(false)
	code, body = e.do(t, http.MethodPost, base+"/payment-configs/"+created.Config.ID+"/verify", tok, "", "")
	if code != http.StatusOK {
		t.Fatalf("verify = %d %s", code, body)
	}
	rows = decodeWalletDomains(t, body)
	if len(rows) != 1 || rows[0].LastError != nil || rows[0].Enabled == nil || !*rows[0].Enabled {
		t.Fatalf("after recovery rows = %s, want the error cleared", body)
	}
}
