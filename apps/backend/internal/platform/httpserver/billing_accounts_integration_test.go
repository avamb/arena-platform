//go:build integration

package httpserver

// billing_accounts_integration_test.go - billing tasks B3-3, B3-4, B4: the
// organizer's balance, credit packs, platform invoices and how their money
// arrives, through the real router, real JWTs and a stub of the platform
// Stripe account.
//
// What it proves:
//   - a pack ordered for a bank transfer is an issued invoice with a payment
//     reference and gives no credits until the money arrives; the bank
//     webhook (HMAC) matches the transfer by reference and amount, pays the
//     invoice and writes the credit lot in the same transaction; a
//     redelivered transfer changes nothing; a wrong reference or amount
//     waits for an operator with a reason;
//   - a pack paid by card carries the payment system fee line, opens a
//     Stripe Checkout page, and the platform Stripe webhook writes the lot
//     once however often the event comes; an unpaid checkout event pays
//     nothing; a bad signature is 401;
//   - the operator generates a usage invoice of a closed month from the
//     uncovered charges, issues it (gapless numbers, card fee line, hosted
//     Stripe invoice), a NET bank transfer (total minus the card fee) pays
//     it, and a void goes to Stripe first and gives the charges back;
//   - the manager and an API key cannot buy, another organization's owner
//     gets 403 on the path organization and 404 for a foreign invoice.
//
// Run against a migrated database (AGENTS.md CI-Integration recipe).

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	acctStripeWebhookSecret = "whsec_billing_accounts_test"
	acctBankSecret          = "bank-secret-billing-accounts-test"
)

// ─── stub of the platform Stripe account ──────────────────────────────────

type billingStripeStub struct {
	mu        sync.Mutex
	ts        *httptest.Server
	calls     []string
	seq       int
	invStatus map[string]string
	byKey     map[string]string // idempotency key -> answer, so a retry gets the same object
}

func newBillingStripeStub(t *testing.T) *billingStripeStub {
	t.Helper()
	s := &billingStripeStub{invStatus: map[string]string{}, byKey: map[string]string{}}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.ts.Close)
	return s
}

func (s *billingStripeStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = r.ParseForm()
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	s.calls = append(s.calls, r.Method+" "+path)
	key := r.Header.Get("Idempotency-Key")
	if key != "" {
		if prev, ok := s.byKey[key]; ok {
			_, _ = io.WriteString(w, prev)
			return
		}
	}
	s.seq++
	var out string
	switch {
	case r.Method == http.MethodPost && path == "/customers":
		out = fmt.Sprintf(`{"id":"cus_%d","email":"","name":""}`, s.seq)
	case r.Method == http.MethodPost && path == "/checkout/sessions":
		id := fmt.Sprintf("cs_test_%d", s.seq)
		out = fmt.Sprintf(`{"id":%q,"url":"https://checkout.stripe.test/%s"}`, id, id)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/expire"):
		out = `{"status":"expired"}`
	case r.Method == http.MethodPost && path == "/invoices":
		id := fmt.Sprintf("in_test_%d", s.seq)
		s.invStatus[id] = "draft"
		out = fmt.Sprintf(`{"id":%q,"status":"draft"}`, id)
	case r.Method == http.MethodPost && path == "/invoiceitems":
		out = fmt.Sprintf(`{"id":"ii_%d"}`, s.seq)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/finalize"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/invoices/"), "/finalize")
		s.invStatus[id] = "open"
		out = fmt.Sprintf(`{"id":%q,"status":"open","hosted_invoice_url":"https://invoice.stripe.test/%s"}`, id, id)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/invoices/"):
		id := strings.TrimPrefix(path, "/invoices/")
		out = fmt.Sprintf(`{"id":%q,"status":%q}`, id, s.invStatus[id])
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/void"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/invoices/"), "/void")
		s.invStatus[id] = "void"
		out = fmt.Sprintf(`{"id":%q,"status":"void"}`, id)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"stub: unknown route"}}`)
		return
	}
	if key != "" {
		s.byKey[key] = out
	}
	_, _ = io.WriteString(w, out)
}

func (s *billingStripeStub) count(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// ─── environment ──────────────────────────────────────────────────────────

type acctEnv struct {
	*sec1Env
	stripe        *billingStripeStub
	orgA, orgB    uuid.UUID
	ownerA, mgrA  string
	ownerB, super string
	superID       uuid.UUID
	accA          gen.BillingAccountRow
}

func newAcctEnv(t *testing.T) *acctEnv {
	t.Helper()
	stub := newBillingStripeStub(t)
	e := &acctEnv{stripe: stub}
	e.sec1Env = newSec1EnvCfg(t, func(c *config.Config) {
		c.BillingStripeSecretKey = "sk_test_platform_billing"
		c.BillingStripeWebhookSecret = acctStripeWebhookSecret
		c.BillingBankWebhookSecret = acctBankSecret
		c.EventCenterPublicURL = "https://events.example.test"
		c.StripeAPIBaseURL = stub.ts.URL + "/v1"
	})
	e.orgA, e.orgB = e.org(t, "BACC-A"), e.org(t, "BACC-B")
	ownerA, mgrA, ownerB := e.user(t, "baccownerA"), e.user(t, "baccmgrA"), e.user(t, "baccownerB")
	e.member(t, ownerA, e.orgA, "org_admin")
	e.member(t, mgrA, e.orgA, "organizer")
	e.member(t, ownerB, e.orgB, "org_admin")
	e.ownerA, e.mgrA, e.ownerB = e.token(t, ownerA), e.token(t, mgrA), e.token(t, ownerB)
	e.superID, e.super = e.superadmin(t)
	acc, err := billing.EnsureAccount(context.Background(), e.q, e.orgA, "ES")
	if err != nil {
		t.Fatalf("EnsureAccount: %v", err)
	}
	e.accA = acc
	if _, err := billing.EnsureAccount(context.Background(), e.q, e.orgB, "ES"); err != nil {
		t.Fatalf("EnsureAccount B: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, org := range []uuid.UUID{e.orgA, e.orgB} {
			for _, stmt := range []string{
				`DELETE FROM billing_bank_transactions WHERE invoice_id IN (SELECT id FROM invoices WHERE org_id = $1)`,
				`DELETE FROM billing_stripe_events WHERE invoice_id IN (SELECT id FROM invoices WHERE org_id = $1)`,
				`DELETE FROM credit_ledger WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`,
				`DELETE FROM charge_allocations WHERE charge_id IN (SELECT id FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1))`,
				`DELETE FROM credit_lots WHERE org_id = $1`,
				`DELETE FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`,
				`DELETE FROM billable_events WHERE org_id = $1`,
				`DELETE FROM invoice_lines WHERE invoice_id IN (SELECT id FROM invoices WHERE org_id = $1)`,
				`DELETE FROM invoices WHERE org_id = $1`,
				`DELETE FROM stripe_customers WHERE org_id = $1`,
				`DELETE FROM billing_account_terms WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`,
				`DELETE FROM billing_accounts WHERE org_id = $1`,
			} {
				if _, err := e.srv.pgxPool.Exec(ctx, stmt, org); err != nil {
					t.Logf("cleanup %q: %v", stmt, err)
				}
			}
		}
	})
	return e
}

// cardFee makes sure EUR card payments are priced (1.5 % + 0.25). A version
// that started in the past is written straight into the table: the screen
// only accepts future ones. Versions are shared by every test of the
// database, so a version of the same start is reused.
func (e *acctEnv) cardFee(t *testing.T) {
	t.Helper()
	if _, err := e.srv.pgxPool.Exec(context.Background(), `
		INSERT INTO billing_payment_fees (method, currency, pct_bp, fixed_minor, effective_from, reason)
		VALUES ('stripe_card', 'EUR', 150, 25, '2026-01-01T00:00:00Z', 'integration test')
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("payment fee: %v", err)
	}
}

func (e *acctEnv) req(t *testing.T, method, path, bearer string, hdr map[string]string, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	out["_raw"] = string(raw)
	return resp.StatusCode, out
}

func (e *acctEnv) pack(t *testing.T, tier int) map[string]any {
	t.Helper()
	code, body := e.req(t, http.MethodGet, "/v1/organizations/"+e.orgA.String()+"/billing/packs", e.ownerA, nil, "")
	if code != http.StatusOK {
		t.Fatalf("packs: %d %v", code, body["_raw"])
	}
	for _, p := range body["packs"].([]any) {
		m := p.(map[string]any)
		if m["currency"] == "EUR" && int(m["tier"].(float64)) == tier {
			return m
		}
	}
	t.Fatalf("no EUR pack of tier %d in %v", tier, body["_raw"])
	return nil
}

func (e *acctEnv) order(t *testing.T, bearer, packID, method, key string) (int, map[string]any) {
	t.Helper()
	return e.req(t, http.MethodPost, "/v1/organizations/"+e.orgA.String()+"/billing/packs/"+packID+"/order", bearer,
		map[string]string{"Idempotency-Key": key}, `{"payment_method":"`+method+`"}`)
}

func (e *acctEnv) bank(t *testing.T, provider, body, secret string) (int, map[string]any) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return e.req(t, http.MethodPost, "/v1/billing/bank/webhook/"+provider, "",
		map[string]string{"X-Arena-Signature": "sha256=" + hex.EncodeToString(mac.Sum(nil))}, body)
}

func (e *acctEnv) stripeEvent(t *testing.T, secret, body string) (int, map[string]any) {
	t.Helper()
	return e.req(t, http.MethodPost, "/v1/billing/stripe/webhook", "",
		map[string]string{"Stripe-Signature": signStripe(secret, []byte(body))}, body)
}

func (e *acctEnv) lots(t *testing.T, invoiceID string) int {
	t.Helper()
	var n int
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT count(*) FROM credit_lots WHERE source = 'invoice' AND source_ref = $1`, invoiceID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func transferBody(id, ref, currency string, amount int64) string {
	return fmt.Sprintf(`{"transactions":[{"id":%q,"booked_at":"2026-10-20T09:00:00Z","amount_minor":%d,"currency":%q,"reference":%q,"counterparty_name":"Org A s.r.o."}]}`,
		id, amount, currency, ref)
}

// ─── tests ────────────────────────────────────────────────────────────────

func TestBillingAccounts_BankTransferPackIsPaidByTheBankFeed(t *testing.T) {
	e := newAcctEnv(t)
	p := e.pack(t, 1)
	packID := str(p["id"])
	key := "order-" + uuid.NewString()

	// The manager cannot buy, an API key cannot even hold the permission.
	if code, body := e.order(t, e.mgrA, packID, "bank_transfer", key); code != http.StatusForbidden {
		t.Fatalf("manager order: %d %v", code, body["_raw"])
	}
	if code, body := e.order(t, e.ownerA, packID, "bank_transfer", ""); code != http.StatusBadRequest {
		t.Fatalf("order without Idempotency-Key: %d %v", code, body["_raw"])
	}

	code, inv := e.order(t, e.ownerA, packID, "bank_transfer", key)
	if code != http.StatusCreated {
		t.Fatalf("order: %d %v", code, inv["_raw"])
	}
	invID, ref := str(inv["id"]), str(inv["payment_reference"])
	if inv["state"] != "issued" || inv["kind"] != "credit_pack" || ref == "" || str(inv["number"]) == "" {
		t.Fatalf("issued pack invoice expected, got %v", inv["_raw"])
	}
	if num(inv["total_minor"]) != num(p["face_minor"]) || num(inv["card_fee_minor"]) != 0 {
		t.Fatalf("bank transfer invoice = face, no card fee: %v", inv["_raw"])
	}
	if code, again := e.order(t, e.ownerA, packID, "bank_transfer", key); code != http.StatusOK || str(again["id"]) != invID {
		t.Fatalf("repeat order: %d %v", code, again["_raw"])
	}
	if e.lots(t, invID) != 0 {
		t.Fatal("credits before the money arrived")
	}

	// A foreign owner: 403 on our path, 404 for our invoice on their path.
	if code, _ := e.req(t, http.MethodGet, "/v1/organizations/"+e.orgA.String()+"/billing/account-invoices/"+invID, e.ownerB, nil, ""); code != http.StatusForbidden {
		t.Fatalf("foreign owner on our path: %d", code)
	}
	if code, _ := e.req(t, http.MethodGet, "/v1/organizations/"+e.orgB.String()+"/billing/account-invoices/"+invID, e.ownerB, nil, ""); code != http.StatusNotFound {
		t.Fatalf("our invoice on a foreign path: %d", code)
	}

	// Bad signature: nothing stored.
	if code, _ := e.bank(t, "testbank", transferBody("tx-bad-"+uuid.NewString(), ref, "EUR", num(inv["total_minor"])), "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	// An unknown reference and a wrong amount wait for an operator.
	code, res := e.bank(t, "testbank", transferBody("tx-unk-"+uuid.NewString(), "payment ARN-ZZZZ-ZZZZ", "EUR", 100), acctBankSecret)
	if code != http.StatusOK || num(res["unmatched"]) != 1 {
		t.Fatalf("unknown reference: %d %v", code, res["_raw"])
	}
	code, res = e.bank(t, "testbank", transferBody("tx-amt-"+uuid.NewString(), "Invoice "+ref, "EUR", num(inv["total_minor"])-1), acctBankSecret)
	item := res["items"].([]any)[0].(map[string]any)
	if code != http.StatusOK || item["unmatched_reason"] != "amount_mismatch" {
		t.Fatalf("wrong amount: %d %v", code, res["_raw"])
	}

	// The real transfer: matched, paid, one lot.
	body := transferBody("tx-ok-"+uuid.NewString(), "Arena pack "+strings.ToLower(ref), "EUR", num(inv["total_minor"]))
	code, res = e.bank(t, "testbank", body, acctBankSecret)
	if code != http.StatusOK || num(res["matched"]) != 1 {
		t.Fatalf("matching transfer: %d %v", code, res["_raw"])
	}
	code, res = e.bank(t, "testbank", body, acctBankSecret)
	if code != http.StatusOK || num(res["duplicate"]) != 1 {
		t.Fatalf("redelivered transfer: %d %v", code, res["_raw"])
	}
	if n := e.lots(t, invID); n != 1 {
		t.Fatalf("lots after payment: %d, want 1", n)
	}
	_, got := e.req(t, http.MethodGet, "/v1/organizations/"+e.orgA.String()+"/billing/account-invoices/"+invID, e.ownerA, nil, "")
	if got["state"] != "paid" || got["paid_via"] != "bank_transfer" {
		t.Fatalf("invoice after transfer: %v", got["_raw"])
	}
	_, bal := e.req(t, http.MethodGet, "/v1/organizations/"+e.orgA.String()+"/billing/balance", e.ownerA, nil, "")
	var balance int64
	for _, a := range bal["accounts"].([]any) {
		m := a.(map[string]any)
		if m["currency"] == "EUR" {
			balance = num(m["purchased_minor"])
		}
	}
	if balance != num(p["face_minor"]) {
		t.Fatalf("balance %d, want the pack face %d: %v", balance, num(p["face_minor"]), bal["_raw"])
	}
	_, led := e.req(t, http.MethodGet, "/v1/organizations/"+e.orgA.String()+"/billing/ledger?account_id="+e.accA.ID.String(), e.ownerA, nil, "")
	entries := led["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["entry_type"] != "purchase" {
		t.Fatalf("ledger: %v", led["_raw"])
	}
}

func TestBillingAccounts_CardPackViaStripeCheckout(t *testing.T) {
	e := newAcctEnv(t)
	e.cardFee(t)
	p := e.pack(t, 2)
	if p["card_fee_minor"] == nil || p["card_payment_available"] != true {
		t.Fatalf("pack offer without a card fee: %v", p)
	}
	code, inv := e.order(t, e.ownerA, str(p["id"]), "stripe_card", "card-"+uuid.NewString())
	if code != http.StatusCreated {
		t.Fatalf("order: %d %v", code, inv["_raw"])
	}
	invID := str(inv["id"])
	fee := num(inv["card_fee_minor"])
	if fee <= 0 || num(inv["total_minor"]) != num(p["face_minor"])+fee || fee != num(p["card_fee_minor"]) {
		t.Fatalf("card invoice must carry the fee line: %v", inv["_raw"])
	}
	if !strings.HasPrefix(str(inv["payment_url"]), "https://checkout.stripe.test/") {
		t.Fatalf("no checkout page: %v", inv["_raw"])
	}
	var session string
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT stripe_checkout_session_id FROM invoices WHERE id = $1`, invID).Scan(&session); err != nil {
		t.Fatal(err)
	}
	ev := func(id, status string, amount int64) string {
		return fmt.Sprintf(`{"id":%q,"type":"checkout.session.completed","data":{"object":{"id":%q,"payment_status":%q,"amount_total":%d,"currency":"eur","payment_intent":"pi_1","metadata":{"arena_invoice_id":%q}}}}`,
			id, session, status, amount, invID)
	}
	if code, _ := e.stripeEvent(t, "whsec_wrong", ev("evt_bad", "paid", num(inv["total_minor"]))); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	code, res := e.stripeEvent(t, acctStripeWebhookSecret, ev("evt_unpaid_"+uuid.NewString(), "unpaid", num(inv["total_minor"])))
	if code != http.StatusOK || res["outcome"] != "not_paid_yet" || e.lots(t, invID) != 0 {
		t.Fatalf("unpaid checkout: %d %v", code, res["_raw"])
	}
	paidEvent := ev("evt_paid_"+uuid.NewString(), "paid", num(inv["total_minor"]))
	code, res = e.stripeEvent(t, acctStripeWebhookSecret, paidEvent)
	if code != http.StatusOK || res["outcome"] != "paid" {
		t.Fatalf("paid checkout: %d %v", code, res["_raw"])
	}
	// The same event twice more, in parallel: still one lot.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.stripeEvent(t, acctStripeWebhookSecret, paidEvent)
		}()
	}
	wg.Wait()
	if n := e.lots(t, invID); n != 1 {
		t.Fatalf("lots: %d, want 1", n)
	}
	var paidVia string
	var paidAmount int64
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT paid_via, paid_amount_minor FROM invoices WHERE id = $1`, invID).Scan(&paidVia, &paidAmount); err != nil {
		t.Fatal(err)
	}
	if paidVia != "stripe_card" || paidAmount != num(inv["total_minor"]) {
		t.Fatalf("paid via %s amount %d", paidVia, paidAmount)
	}
	// The lot is the pack face, never the fee.
	var face int64
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT face_minor FROM credit_lots WHERE source_ref = $1`, invID).Scan(&face); err != nil {
		t.Fatal(err)
	}
	if face != num(p["face_minor"]) {
		t.Fatalf("lot face %d, want %d", face, num(p["face_minor"]))
	}
}

// usageCharge seeds one sold ticket rated at amount in September 2026.
func (e *acctEnv) usageCharge(t *testing.T, day int, amount int64) {
	t.Helper()
	ctx := context.Background()
	cur, unit := "EUR", int64(2000)
	id, ok, err := e.q.InsertBillableEvent(ctx, gen.InsertBillableEventParams{
		Kind: "ticket_sold", SubjectID: uuid.New(), OccurredAt: time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC),
		OrgID: e.orgA, AccountID: e.accA.ID, Currency: &cur, UnitPriceMinor: &unit, Source: "collect",
	})
	if err != nil || !ok {
		t.Fatalf("event: %v", err)
	}
	if _, err := e.srv.pgxPool.Exec(ctx, `INSERT INTO charges (billable_event_id, account_id, currency, amount_minor, rule_type)
	        VALUES ($1, $2, 'EUR', $3, 'fixed_per_ticket')`, id, e.accA.ID, amount); err != nil {
		t.Fatalf("charge: %v", err)
	}
}

func TestBillingAccounts_UsageInvoiceNumbersCardFeeNetTransferAndVoid(t *testing.T) {
	e := newAcctEnv(t)
	e.cardFee(t)
	for d := 1; d <= 3; d++ {
		e.usageCharge(t, d, 59)
	}
	reason := map[string]string{"X-Admin-Reason": "integration test"}
	gen1 := "/v1/admin/billing/accounts/" + e.accA.ID.String() + "/invoices"
	if code, _ := e.req(t, http.MethodPost, gen1, e.ownerA, reason, `{"period":"2026-09"}`); code != http.StatusForbidden {
		t.Fatalf("owner on the admin route: %d", code)
	}
	if code, body := e.req(t, http.MethodPost, gen1, e.super, reason, `{"period":"2099-01"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("open month: %d %v", code, body["_raw"])
	}
	code, res := e.req(t, http.MethodPost, gen1, e.super, reason, `{"period":"2026-09"}`)
	if code != http.StatusCreated {
		t.Fatalf("generate: %d %v", code, res["_raw"])
	}
	draft := res["invoice"].(map[string]any)
	if draft["state"] != "draft" || num(draft["subtotal_minor"]) != 177 {
		t.Fatalf("draft: %v", res["_raw"])
	}
	// Nothing left to bill for the month.
	if code, res := e.req(t, http.MethodPost, gen1, e.super, reason, `{"period":"2026-09"}`); code != http.StatusOK || res["invoice"] != nil {
		t.Fatalf("second generate: %d %v", code, res["_raw"])
	}
	// The owner does not see a draft.
	if code, _ := e.req(t, http.MethodGet, "/v1/organizations/"+e.orgA.String()+"/billing/account-invoices/"+str(draft["id"]), e.ownerA, nil, ""); code != http.StatusNotFound {
		t.Fatalf("draft visible to the owner: %d", code)
	}
	code, issued := e.req(t, http.MethodPost, "/v1/admin/billing/invoices/"+str(draft["id"])+"/issue", e.super, reason, `{"payment_method":"stripe_card"}`)
	if code != http.StatusOK {
		t.Fatalf("issue: %d %v", code, issued["_raw"])
	}
	fee := num(issued["card_fee_minor"])
	if fee <= 0 || num(issued["total_minor"]) != 177+fee || !strings.HasPrefix(str(issued["payment_url"]), "https://invoice.stripe.test/") {
		t.Fatalf("issued card invoice: %v", issued["_raw"])
	}
	if e.stripe.count("POST /invoices/") < 1 || e.stripe.count("POST /invoiceitems") != 2 {
		t.Fatalf("stripe calls: %v", e.stripe.calls)
	}
	// A net bank transfer (no card fee) with the reference pays it.
	ref := str(issued["payment_reference"])
	code, bres := e.bank(t, "testbank", transferBody("tx-net-"+uuid.NewString(), ref, "EUR", 177), acctBankSecret)
	if code != http.StatusOK || num(bres["matched"]) != 1 {
		t.Fatalf("net transfer: %d %v", code, bres["_raw"])
	}
	// Voiding a paid invoice is refused.
	if code, _ := e.req(t, http.MethodPost, "/v1/admin/billing/invoices/"+str(draft["id"])+"/void", e.super, reason, `{"reason":"test"}`); code != http.StatusConflict {
		t.Fatalf("void a paid invoice: %d", code)
	}

	// Gapless: a second invoice gets the next number of the series.
	e.usageCharge(t, 20, 59)
	code, res = e.req(t, http.MethodPost, gen1, e.super, reason, `{"period":"2026-09"}`)
	if code != http.StatusCreated {
		t.Fatalf("generate 2: %d %v", code, res["_raw"])
	}
	draft2 := str(res["invoice"].(map[string]any)["id"])
	code, issued2 := e.req(t, http.MethodPost, "/v1/admin/billing/invoices/"+draft2+"/issue", e.super, reason, `{"payment_method":"stripe_card"}`)
	if code != http.StatusOK {
		t.Fatalf("issue 2: %d %v", code, issued2["_raw"])
	}
	n1, n2 := str(issued["number"]), str(issued2["number"])
	if !consecutive(n1, n2) {
		// Another test of the shared database may issue in between; only
		// the order is guaranteed then.
		if n2 <= n1 {
			t.Fatalf("numbers %s then %s", n1, n2)
		}
	}
	// Void: Stripe first, then the charge goes back to the next invoice.
	code, voided := e.req(t, http.MethodPost, "/v1/admin/billing/invoices/"+draft2+"/void", e.super, reason, `{"reason":"wrong month"}`)
	if code != http.StatusOK || voided["state"] != "void" {
		t.Fatalf("void: %d %v", code, voided["_raw"])
	}
	if e.stripe.count("POST /invoices/") < 3 { // finalize x2 + void
		t.Fatalf("void did not reach Stripe: %v", e.stripe.calls)
	}
	code, res = e.req(t, http.MethodPost, gen1, e.super, reason, `{"period":"2026-09"}`)
	if code != http.StatusCreated || num(res["invoice"].(map[string]any)["subtotal_minor"]) != 59 {
		t.Fatalf("charge not given back after the void: %d %v", code, res["_raw"])
	}
	// The audit rows exist.
	var n int
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'v1.admin.billing.invoice.void' AND resource_id = $1`, draft2).Scan(&n); err != nil || n != 1 {
		t.Fatalf("void audit rows: %d %v", n, err)
	}
}

func consecutive(a, b string) bool {
	var sa, sb int
	ia, ib := strings.LastIndex(a, "-"), strings.LastIndex(b, "-")
	if ia < 0 || ib < 0 || a[:ia] != b[:ib] {
		return false
	}
	_, _ = fmt.Sscanf(a[ia+1:], "%d", &sa)
	_, _ = fmt.Sscanf(b[ib+1:], "%d", &sb)
	return sb == sa+1
}

func TestBillingAccounts_PaymentFeeAndBankAdmin(t *testing.T) {
	e := newAcctEnv(t)
	reason := map[string]string{"X-Admin-Reason": "integration test"}
	past := `{"method":"stripe_card","currency":"CHF","pct_bp":290,"fixed_minor":30,"effective_from":"2020-01-01T00:00:00Z"}`
	if code, _ := e.req(t, http.MethodPost, "/v1/admin/billing/payment-fees", e.super, reason, past); code != http.StatusUnprocessableEntity {
		t.Fatalf("fee in the past: %d", code)
	}
	if code, _ := e.req(t, http.MethodPost, "/v1/admin/billing/payment-fees", e.super, nil,
		strings.Replace(past, "2020", "2099", 1)); code != http.StatusBadRequest {
		t.Fatalf("fee without a reason: %d", code)
	}
	// A pack ordered for a transfer, paid without its reference: the operator matches it.
	p := e.pack(t, 1)
	_, inv := e.order(t, e.ownerA, str(p["id"]), "bank_transfer", "op-"+uuid.NewString())
	invID := str(inv["id"])
	code, res := e.req(t, http.MethodPost, "/v1/admin/billing/bank-transactions", e.super, reason,
		transferBody("stmt-"+uuid.NewString(), "membership", "EUR", num(inv["total_minor"])))
	if code != http.StatusOK || num(res["unmatched"]) != 1 {
		t.Fatalf("import: %d %v", code, res["_raw"])
	}
	txID := str(res["items"].([]any)[0].(map[string]any)["transaction_id"])
	if code, body := e.req(t, http.MethodPost, "/v1/admin/billing/bank-transactions/"+txID+"/match", e.super, reason,
		`{"invoice_id":"`+invID+`"}`); code != http.StatusOK || body["status"] != "matched" {
		t.Fatalf("match: %d %v", code, body["_raw"])
	}
	if n := e.lots(t, invID); n != 1 {
		t.Fatalf("lots after a manual match: %d", n)
	}
	if code, _ := e.req(t, http.MethodPost, "/v1/admin/billing/bank-transactions/"+txID+"/ignore", e.super, reason,
		`{"reason":"x"}`); code != http.StatusConflict {
		t.Fatalf("ignore a matched payment: %d", code)
	}
}

func TestBillingAccounts_ServiceFeeQuoteIsTheTicketRate(t *testing.T) {
	e := newAcctEnv(t)
	base := "/v1/organizations/" + e.orgA.String() + "/billing/service-fee-quote"
	code, q := e.req(t, http.MethodGet, base+"?currency=EUR&price_minor=2000", e.ownerA, nil, "")
	if code != http.StatusOK || num(q["service_fee_minor"]) <= 0 || q["fee_mode"] != "organizer_pays" {
		t.Fatalf("quote: %d %v", code, q["_raw"])
	}
	// The quote equals what the rating engine bills for the same ticket.
	terms, err := billing.TermsAt(context.Background(), e.q, e.accA.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	class, _ := billing.ClassifyLowPrice(terms, "EUR", 2000)
	c, err := billing.Rate(terms, billing.Event{Kind: billing.KindTicketSold, OccurredAt: terms.At, Currency: "EUR", UnitPriceMinor: 2000, PricingClass: class})
	if err != nil || c.AmountMinor != num(q["service_fee_minor"]) {
		t.Fatalf("rate %d (%v), quote %d", c.AmountMinor, err, num(q["service_fee_minor"]))
	}
	if code, _ := e.req(t, http.MethodGet, base+"?currency=EUR&price_minor=-1", e.ownerA, nil, ""); code != http.StatusBadRequest {
		t.Fatalf("negative price: %d", code)
	}
	if code, _ := e.req(t, http.MethodGet, base+"?currency=EUR&price_minor=2000", e.mgrA, nil, ""); code != http.StatusForbidden {
		t.Fatalf("manager: %d", code)
	}
}

// invoiceAdminAuditRoutes are the write routes of the invoice, payment-fee
// and bank screens. TestBillingAdmin_EveryWriteIsAudited leaves them to
// TestBillingAccounts_EveryInvoiceAdminWriteIsAudited, which needs this
// file's environment (a billing account, packs, the Stripe stub) and checks
// the same thing: one audit row, by the operator, with the reason.
var invoiceAdminAuditRoutes = map[string]bool{
	"POST /v1/admin/billing/accounts/{account_id}/invoices":            true,
	"POST /v1/admin/billing/invoices/{invoice_id}/issue":               true,
	"POST /v1/admin/billing/invoices/{invoice_id}/mark-paid":           true,
	"POST /v1/admin/billing/invoices/{invoice_id}/void":                true,
	"POST /v1/admin/billing/payment-fees":                              true,
	"POST /v1/admin/billing/bank-transactions":                         true,
	"POST /v1/admin/billing/bank-transactions/{transaction_id}/match":  true,
	"POST /v1/admin/billing/bank-transactions/{transaction_id}/ignore": true,
}

func TestBillingAccounts_EveryInvoiceAdminWriteIsAudited(t *testing.T) {
	e := newAcctEnv(t)
	ctx := context.Background()
	covered := map[string]bool{}
	call := func(route, method, path, body string, want int, resource func(map[string]any) string) map[string]any {
		t.Helper()
		reason := "b4 audit " + uuid.NewString()[:8]
		code, out := e.req(t, method, path, e.super, map[string]string{"X-Admin-Reason": reason}, body)
		if code != want {
			t.Fatalf("%s: %d %v", route, code, out["_raw"])
		}
		action := billingAdminWriteRoutes[route]
		res := resource(out)
		var n int
		var actor, gotReason string
		rows, err := e.srv.pgxPool.Query(ctx,
			`SELECT COALESCE(actor_id::text, ''), COALESCE(metadata->>'reason', '') FROM audit_events WHERE action = $1 AND resource_id = $2`,
			action, res)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			if err := rows.Scan(&actor, &gotReason); err != nil {
				t.Fatal(err)
			}
			if gotReason == reason {
				n++
			}
		}
		rows.Close()
		if n != 1 || actor != e.superID.String() {
			t.Errorf("%s: %d audit rows with the reason (action %s, resource %s), actor %q; want 1, actor %s", route, n, action, res, actor, e.superID)
		}
		covered[route] = true
		return out
	}
	field := func(k string) func(map[string]any) string { return func(m map[string]any) string { return str(m[k]) } }

	// A usage invoice: generate, issue for a bank transfer, mark paid by hand.
	e.usageCharge(t, 5, 59)
	gen1 := call("POST /v1/admin/billing/accounts/{account_id}/invoices", http.MethodPost,
		"/v1/admin/billing/accounts/"+e.accA.ID.String()+"/invoices", `{"period":"2026-09"}`, http.StatusCreated,
		func(m map[string]any) string { return str(m["invoice"].(map[string]any)["id"]) })
	invID := str(gen1["invoice"].(map[string]any)["id"])
	issued := call("POST /v1/admin/billing/invoices/{invoice_id}/issue", http.MethodPost,
		"/v1/admin/billing/invoices/"+invID+"/issue", `{"payment_method":"bank_transfer"}`, http.StatusOK, field("id"))
	paidAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	call("POST /v1/admin/billing/invoices/{invoice_id}/mark-paid", http.MethodPost,
		"/v1/admin/billing/invoices/"+invID+"/mark-paid",
		fmt.Sprintf(`{"amount_minor":%d,"paid_at":%q,"reference":"statement line 7"}`, num(issued["total_minor"]), paidAt),
		http.StatusOK, field("id"))

	// A pack ordered for a transfer and then voided.
	p := e.pack(t, 1)
	_, toVoid := e.order(t, e.ownerA, str(p["id"]), "bank_transfer", "audit-void-"+uuid.NewString())
	call("POST /v1/admin/billing/invoices/{invoice_id}/void", http.MethodPost,
		"/v1/admin/billing/invoices/"+str(toVoid["id"])+"/void", `{"reason":"ordered by mistake"}`, http.StatusOK, field("id"))

	// A card fee version of the ISO test currency, after the latest one.
	var latest *time.Time
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT max(effective_from) FROM billing_payment_fees WHERE method = 'stripe_card' AND currency = 'XTS'`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	from := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	if latest != nil && !from.After(*latest) {
		from = latest.UTC().Add(24 * time.Hour).Truncate(time.Second)
	}
	call("POST /v1/admin/billing/payment-fees", http.MethodPost, "/v1/admin/billing/payment-fees",
		fmt.Sprintf(`{"method":"stripe_card","currency":"XTS","pct_bp":150,"fixed_minor":25,"effective_from":%q}`, from.Format(time.RFC3339)),
		http.StatusCreated, field("id"))

	// Two statement lines without a reference: one is matched by hand, one ignored.
	_, toMatch := e.order(t, e.ownerA, str(p["id"]), "bank_transfer", "audit-match-"+uuid.NewString())
	lineID := func(m map[string]any) string { return str(m["items"].([]any)[0].(map[string]any)["transaction_id"]) }
	imp := call("POST /v1/admin/billing/bank-transactions", http.MethodPost, "/v1/admin/billing/bank-transactions",
		transferBody("audit-"+uuid.NewString(), "membership", "EUR", num(toMatch["total_minor"])), http.StatusOK, lineID)
	call("POST /v1/admin/billing/bank-transactions/{transaction_id}/match", http.MethodPost,
		"/v1/admin/billing/bank-transactions/"+lineID(imp)+"/match", `{"invoice_id":"`+str(toMatch["id"])+`"}`,
		http.StatusOK, func(map[string]any) string { return lineID(imp) })
	_, imp2 := e.req(t, http.MethodPost, "/v1/admin/billing/bank-transactions", e.super,
		map[string]string{"X-Admin-Reason": "second line"}, transferBody("audit-"+uuid.NewString(), "refund from a supplier", "EUR", 4200))
	call("POST /v1/admin/billing/bank-transactions/{transaction_id}/ignore", http.MethodPost,
		"/v1/admin/billing/bank-transactions/"+lineID(imp2)+"/ignore", `{"reason":"not ours"}`,
		http.StatusOK, func(map[string]any) string { return lineID(imp2) })

	for route := range invoiceAdminAuditRoutes {
		if !covered[route] {
			t.Errorf("the invoice audit test does not exercise %s", route)
		}
	}
	if _, err := e.srv.pgxPool.Exec(ctx, `DELETE FROM billing_bank_transactions WHERE provider = 'manual' AND external_id LIKE 'audit-%'`); err != nil {
		t.Logf("cleanup bank lines: %v", err)
	}
}
