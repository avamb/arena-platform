//go:build integration

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/disputes"
)

// stubStripeDisputes is Stripe's dispute API: one dispute, its charge, file
// uploads and the evidence posts (spec 51.3).
type stubStripeDisputes struct {
	server *httptest.Server
	mu     sync.Mutex

	disputeID, chargeID, paymentIntent string
	status                             string
	dueBy                              int64
	submissions                        int
	uploads                            int
	evidence                           []url.Values
	closed                             int
}

func newStubStripeDisputes(t *testing.T, pi string) *stubStripeDisputes {
	t.Helper()
	s := &stubStripeDisputes{
		disputeID:     "dp_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		chargeID:      "ch_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		paymentIntent: pi, status: "needs_response",
		dueBy: time.Now().Add(9 * 24 * time.Hour).Unix(),
	}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/files"):
			if err := r.ParseMultipartForm(10 << 20); err != nil || r.FormValue("purpose") != "dispute_evidence" {
				t.Errorf("stub: file upload is not a dispute_evidence multipart: %v", err)
			}
			s.uploads++
			_, _ = fmt.Fprintf(w, `{"id":"file_%d","purpose":"dispute_evidence"}`, s.uploads)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/charges/"+s.chargeID):
			_, _ = fmt.Fprintf(w, `{"id":%q,"payment_intent":%q,"calculated_statement_descriptor":"ARENA*TEST","payment_method_details":{"card":{"brand":"visa","last4":"2685","three_d_secure":{"result":"authenticated"}}}}`,
				s.chargeID, s.paymentIntent)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/disputes/"+s.disputeID+"/close"):
			s.closed++
			s.status = "lost"
			_, _ = fmt.Fprint(w, s.object())
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/disputes/"+s.disputeID):
			raw, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(raw))
			s.evidence = append(s.evidence, form)
			if form.Get("submit") == "true" {
				s.submissions++
				s.status = "under_review"
			}
			_, _ = fmt.Fprint(w, s.object())
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/disputes/"+s.disputeID):
			_, _ = fmt.Fprint(w, s.object())
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"no such object"}}`)
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stubStripeDisputes) object() string {
	return fmt.Sprintf(`{"id":%q,"object":"dispute","amount":5000,"currency":"eur","charge":%q,"payment_intent":%q,"reason":"fraudulent","status":%q,"is_charge_refundable":false,"created":%d,"evidence_details":{"due_by":%d,"has_evidence":%t,"submission_count":%d}}`,
		s.disputeID, s.chargeID, s.paymentIntent, s.status, time.Now().Add(-time.Hour).Unix(), s.dueBy, len(s.evidence) > 0, s.submissions)
}

func (s *stubStripeDisputes) event(typ string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []byte(fmt.Sprintf(`{"id":"evt_%s_%s","type":%q,"data":{"object":%s}}`, strings.ReplaceAll(typ, ".", "_"), uuid.NewString()[:8], typ, s.object()))
}

type disputeAnnouncer struct {
	mu    sync.Mutex
	texts []string
}

func (a *disputeAnnouncer) AnnounceToOrg(_ context.Context, _ string, text string) (int, int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.texts = append(a.texts, text)
	return 1, 1, nil
}

func cleanupDisputes(pool *pgxpool.Pool, orgs ...uuid.UUID) {
	ctx := context.Background()
	for _, org := range orgs {
		_, _ = pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type IN ('dispute.build_package','dispute.send_email')
			AND payload->>'dispute_id' IN (SELECT id::text FROM payment_disputes WHERE org_id = $1)`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM payment_dispute_notices WHERE dispute_id IN (SELECT id FROM payment_disputes WHERE org_id = $1)`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM payment_dispute_files WHERE package_id IN (SELECT id FROM payment_dispute_packages WHERE org_id = $1)`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM payment_dispute_packages WHERE org_id = $1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM payment_dispute_events WHERE dispute_id IN (SELECT id FROM payment_disputes WHERE org_id = $1)`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM payment_disputes WHERE org_id = $1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM dispute_settings WHERE org_id = $1`, org)
	}
}

// TestDisputes_StripeFlowFromWebhookToTheBank walks a Stripe dispute from its
// webhook to the bank: the dispute is recorded once, the package is built
// and saved at Stripe as a DRAFT (submit=false), nothing more leaves until
// the owner presses Submit, the manager reads but cannot answer, another
// organization sees nothing, and the closing webhook records the outcome.
func TestDisputes_StripeFlowFromWebhookToTheBank(t *testing.T) {
	ctx := context.Background()
	pi := "pi_disp_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	stub := newStubStripeDisputes(t, pi)
	srv, secret := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.server.URL + "/v1" })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pool := srv.pgxPool
	q := gen.New(pool)

	mkOrg := func(label string) uuid.UUID {
		suffix := uuid.NewString()[:8]
		o, err := q.InsertOrganization(ctx, "Disputes "+label+" "+suffix, "disputes-"+strings.ToLower(label)+"-"+suffix, "ES", "ru", 1200)
		if err != nil {
			t.Fatal(err)
		}
		return o.ID
	}
	orgA, orgB := mkOrg("A"), mkOrg("B")
	mkUser := func(org uuid.UUID, role string) string {
		u, err := q.InsertUser(ctx, "disp-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.InsertMembership(ctx, u.ID, org, role); err != nil {
			t.Fatal(err)
		}
		tok, _, err := auth.IssueJWT(secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	ownerA, managerA, ownerB := mkUser(orgA, "org_admin"), mkUser(orgA, "organizer"), mkUser(orgB, "org_admin")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM memberships WHERE org_id = ANY($1)`, []uuid.UUID{orgA, orgB})
	})
	f := newPay03Order(t, pool, orgA, "stripe")
	t.Cleanup(func() { cleanupDisputes(pool, orgA, orgB) })
	if _, err := pool.Exec(ctx, `UPDATE payment_intents SET provider_charge_ref = $2 WHERE id = $1`, f.pay, pi); err != nil {
		t.Fatal(err)
	}
	var configID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM payment_provider_configs WHERE org_id = $1 AND provider = 'stripe'`, orgA).Scan(&configID); err != nil {
		t.Fatal(err)
	}
	hook := func(body []byte) map[string]any {
		t.Helper()
		code, raw := postSignedWebhookTo(t, srv, "/v1/payment-intents/webhook/"+configID.String(), body, "whsec_pay03")
		if code != http.StatusOK {
			t.Fatalf("webhook = %d %s", code, raw)
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}
	call := func(method, path, token, body string) (int, map[string]any) {
		t.Helper()
		resp := integDoRequest(t, ts.Client(), method, ts.URL+path, token, body)
		raw := integReadBody(t, resp)
		out := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &out)
		return resp.StatusCode, out
	}

	// 1. The opening webhook records the dispute and queues the package.
	if m := hook(stub.event("charge.dispute.created")); m["processed"] != true {
		t.Fatalf("created: %v", m)
	}
	var disputeID uuid.UUID
	var state, channel, source string
	if err := pool.QueryRow(ctx, `SELECT id, state, channel, due_by_source FROM payment_disputes WHERE org_id = $1`, orgA).Scan(&disputeID, &state, &channel, &source); err != nil {
		t.Fatalf("dispute row: %v", err)
	}
	if state != "needs_response" || channel != "api" || source != "provider" {
		t.Fatalf("dispute = %s/%s/%s", state, channel, source)
	}
	// a replay writes nothing more
	hook(stub.event("charge.dispute.created"))
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM payment_disputes WHERE org_id = $1`, orgA).Scan(&n)
	if n != 1 {
		t.Fatalf("after a replay there are %d disputes", n)
	}
	var jobs int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'dispute.build_package' AND payload->>'dispute_id' = $1`, disputeID.String()).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("build jobs = %d", jobs)
	}

	// 2. The job builds the package and saves a DRAFT at Stripe.
	svc := srv.disputeService()
	payload, _ := json.Marshal(map[string]string{"dispute_id": disputeID.String()})
	if err := svc.BuildJobHandler()(ctx, payload); err != nil {
		t.Fatalf("build: %v", err)
	}
	stub.mu.Lock()
	if stub.submissions != 0 || len(stub.evidence) != 1 || stub.evidence[0].Get("submit") != "false" || stub.uploads == 0 {
		t.Fatalf("after the build: submissions=%d posts=%d uploads=%d", stub.submissions, len(stub.evidence), stub.uploads)
	}
	if stub.evidence[0].Get("evidence[customer_email_address]") != "" && !strings.Contains(stub.evidence[0].Encode(), "evidence%5Breceipt%5D=file_") {
		t.Fatalf("draft has no receipt file: %s", stub.evidence[0].Encode())
	}
	stub.mu.Unlock()

	base := "/v1/organizations/" + orgA.String() + "/disputes"
	code, card := call(http.MethodGet, base+"/"+disputeID.String(), ownerA, "")
	if code != http.StatusOK {
		t.Fatalf("card: %d %v", code, card)
	}
	d := card["dispute"].(map[string]any)
	pkg, _ := card["package"].(map[string]any)
	if d["state"] != "draft_ready" || pkg == nil || pkg["uploaded"] != true || card["test_outcome_available"] != true {
		t.Fatalf("card = %v", card)
	}
	files := pkg["files"].([]any)
	fileID := files[0].(map[string]any)["id"].(string)

	// 3. Roles and organizations.
	if code, out := call(http.MethodGet, base, managerA, ""); code != http.StatusOK || out["open_count"] != float64(1) {
		t.Fatalf("manager list: %d %v", code, out)
	}
	if code, _ := call(http.MethodPost, base+"/"+disputeID.String()+"/submit", managerA, `{}`); code != http.StatusForbidden {
		t.Fatalf("manager submit = %d; want 403", code)
	}
	if code, _ := call(http.MethodGet, base+"/"+disputeID.String(), ownerB, ""); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("foreign owner on org A = %d", code)
	}
	if code, out := call(http.MethodGet, "/v1/organizations/"+orgB.String()+"/disputes/"+disputeID.String(), ownerB, ""); code != http.StatusNotFound || prom0113Code(out) != "dispute.not_found" {
		t.Fatalf("foreign dispute through own org = %d %v", code, out)
	}
	resp := integDoRequest(t, ts.Client(), http.MethodGet, ts.URL+base+"/"+disputeID.String()+"/files/"+fileID, ownerA, "")
	pdf := integReadBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(pdf, "%PDF-") || resp.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("file: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// 4. The owner's one press sends the answer (test outcome: win).
	code, out := call(http.MethodPost, base+"/"+disputeID.String()+"/submit", ownerA, `{"test_outcome":"win"}`)
	if code != http.StatusOK || out["dispute"].(map[string]any)["state"] != "submitted" {
		t.Fatalf("submit: %d %v", code, out)
	}
	stub.mu.Lock()
	last := stub.evidence[len(stub.evidence)-1]
	if stub.submissions != 1 || last.Get("submit") != "true" || last.Get("evidence[uncategorized_text]") != "winning_evidence" {
		t.Fatalf("submit at stripe: submissions=%d form=%s", stub.submissions, last.Encode())
	}
	stub.mu.Unlock()
	if code, out := call(http.MethodPost, base+"/"+disputeID.String()+"/submit", ownerA, `{}`); code != http.StatusConflict || prom0113Code(out) != "dispute.already_submitted" {
		t.Fatalf("second submit: %d %v", code, out)
	}

	// 5. The bank decides; the closing webhook records the outcome.
	stub.mu.Lock()
	stub.status = "won"
	stub.mu.Unlock()
	hook(stub.event("charge.dispute.closed"))
	var outcome string
	if err := pool.QueryRow(ctx, `SELECT state, COALESCE(outcome,'') FROM payment_disputes WHERE id = $1`, disputeID).Scan(&state, &outcome); err != nil || state != "closed" || outcome != "won" {
		t.Fatalf("after close: %s/%s %v", state, outcome, err)
	}

	// 6. The watch tells the organization once per notice.
	ann := &disputeAnnouncer{}
	notifier := disputes.Notifier{Announcer: ann, EventCenterURL: "https://events.example.test"}
	if _, err := svc.RunWatch(ctx, notifier, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunWatch(ctx, notifier, time.Now()); err != nil {
		t.Fatal(err)
	}
	ann.mu.Lock()
	defer ann.mu.Unlock()
	joined := strings.Join(ann.texts, "\n----\n")
	if !strings.Contains(joined, "Открыт спор") || !strings.Contains(joined, "Спор выигран") || strings.Count(joined, "Спор выигран") != 1 {
		t.Fatalf("notices:\n%s", joined)
	}
	if strings.Contains(joined, "buyer@example.com") {
		t.Fatal("a chat notice carries the buyer's address")
	}
}

// TestDisputes_ForeignPaymentAndAPIKey: a dispute of a payment arena does
// not own is acknowledged with nothing written, and an organization API key
// cannot read disputes.
func TestDisputes_ForeignPaymentAndAPIKey(t *testing.T) {
	ctx := context.Background()
	stub := newStubStripeDisputes(t, "pi_somebody_elses_"+uuid.NewString()[:6])
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.server.URL + "/v1" })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(ctx, "dispkey-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "DISPKEY")
	newPay03Order(t, srv.pgxPool, org, "stripe")
	t.Cleanup(func() { cleanupDisputes(srv.pgxPool, org) })
	var configID uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx, `SELECT id FROM payment_provider_configs WHERE org_id = $1`, org).Scan(&configID); err != nil {
		t.Fatal(err)
	}
	code, raw := postSignedWebhookTo(t, srv, "/v1/payment-intents/webhook/"+configID.String(), stub.event("charge.dispute.created"), "whsec_pay03")
	if code != http.StatusOK || !bytes.Contains(raw, []byte(`"processed":false`)) {
		t.Fatalf("foreign dispute: %d %s", code, raw)
	}
	var n int
	_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM payment_disputes WHERE org_id = $1`, org).Scan(&n)
	if n != 0 {
		t.Fatalf("a foreign dispute wrote %d rows", n)
	}
	key := pf.key(t, org, "dispute.read")
	if code, out := pf.do(t, http.MethodGet, "/v1/organizations/"+org.String()+"/disputes", key, ""); code != http.StatusForbidden || prom0113Code(out) != "dispute.api_key_forbidden" {
		t.Fatalf("api key: %d %v", code, out)
	}
}
