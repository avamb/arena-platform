//go:build integration

package httpserver

// Spec 51 section 2 (decision R-6, migration 0185): the provider's own
// receipt for the buyer. The owner switches it, only a platform superadmin
// sets "always send", Stripe's Checkout Session carries
// payment_intent_data[receipt_email] by that rule, and a paid payment queues
// the archive job that keeps the receipt link and a copy of the receipt.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/paymentreceipts"
)

func receiptSetting(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	profile, _ := out["seller_profile"].(map[string]any)
	pr, ok := profile["provider_receipt"].(map[string]any)
	if !ok {
		t.Fatalf("no provider_receipt in %v", out)
	}
	return pr
}

func TestProviderReceipt_OwnerSwitchesAndOnlyTheOperatorForces(t *testing.T) {
	f := newSellerAccessFixture(t)
	path := "/v1/organizations/" + f.org.String() + "/seller-profile"
	owner := f.member(t, "org_admin")

	st, out := f.do(t, http.MethodGet, path, owner, "", "")
	if st != http.StatusOK {
		t.Fatalf("GET: %d %v", st, out)
	}
	if pr := receiptSetting(t, out); pr["enabled"] != true || pr["forced"] != false || pr["effective"] != true {
		t.Fatalf("default = %v; want enabled, not forced, effective", pr)
	}

	st, out = f.do(t, http.MethodPatch, path, owner, "", `{"provider_receipt_enabled": false}`)
	if st != http.StatusOK {
		t.Fatalf("owner turns off: %d %v", st, out)
	}
	if pr := receiptSetting(t, out); pr["enabled"] != false || pr["effective"] != false {
		t.Fatalf("after off = %v", pr)
	}

	st, out = f.do(t, http.MethodPatch, path, owner, "", `{"provider_receipt_forced": true}`)
	if st != http.StatusForbidden || prom0113Code(out) != "seller.provider_receipt_forced_superadmin_only" {
		t.Fatalf("owner forces: %d %v; want 403", st, out)
	}
	if st, out = f.do(t, http.MethodPatch, path, owner, "", `{"provider_receipt_forced": false}`); st != http.StatusOK {
		t.Fatalf("owner repeats the stored value: %d %v", st, out)
	}

	manager := f.member(t, "organizer")
	if st, out = f.do(t, http.MethodPatch, path, manager, "", `{"provider_receipt_enabled": true}`); st != http.StatusForbidden {
		t.Fatalf("manager: %d %v; want 403", st, out)
	}

	sa := f.member(t, "superadmin")
	if st, out = f.do(t, http.MethodPatch, path, sa, "", `{"provider_receipt_forced": true}`); st != http.StatusBadRequest || prom0113Code(out) != "superadmin.missing_reason" {
		t.Fatalf("superadmin without reason: %d %v", st, out)
	}
	st, out = f.do(t, http.MethodPatch, path, sa, "buyer complaints about missing receipts", `{"provider_receipt_forced": true}`)
	if st != http.StatusOK {
		t.Fatalf("superadmin forces: %d %v", st, out)
	}
	if pr := receiptSetting(t, out); pr["enabled"] != false || pr["forced"] != true || pr["effective"] != true {
		t.Fatalf("after force = %v; want the owner's off overridden", pr)
	}

	var audits int
	if err := f.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events
		WHERE action = 'v1.organization.provider_receipt_update' AND metadata->>'org_id' = $1`, f.org.String()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 3 {
		t.Errorf("audit rows = %d; want 3 (off, repeat, force, and nothing for the refused calls)", audits)
	}
	if st, out = f.do(t, http.MethodPatch, path, owner, "", `{}`); st != http.StatusBadRequest {
		t.Errorf("empty body: %d %v", st, out)
	}
}

// receiptStripeStub answers the payment intent read of the archive job and
// serves the receipt PDF.
func receiptStripeStub(t *testing.T, pi string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/payment_intents/"+pi:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":%q,"latest_charge":{"id":"ch_1","receipt_url":"%s/receipts/payment/rcpt?s=ap","receipt_number":"2071-4421"}}`, pi, srv.URL)
		case r.URL.Path == "/receipts/payment/rcpt/pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4 receipt of " + pi))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProviderReceipt_CheckoutAsksStripeAndThePaymentIsArchived(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "rc")
	defer f.cleanup()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())
	q := gen.New(pool)
	// Three checkouts of two tickets each on the fixture's five places: the
	// places of the previous cart are handed back before the next one.
	freePlaces := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE session_seats SET status = 'available', reservation_id = NULL
			WHERE session_id = $1 AND status = 'held'`, f.sessionID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE inventory_ledger SET capacity_held = 0 WHERE session_id = $1`, f.sessionID); err != nil {
			t.Fatal(err)
		}
	}
	returnURL := hostedTicketsBaseURL + "/shows/receipt"

	start := func(want string) hostedStartResponse {
		t.Helper()
		freePlaces()
		code, body := f.startCheckout(t, srv, returnURL)
		if code != http.StatusCreated {
			t.Fatalf("checkout/start = %d: %s", code, body)
		}
		if got := stripe.lastForm(t).Get("payment_intent_data[receipt_email]"); got != want {
			t.Fatalf("payment_intent_data[receipt_email] = %q; want %q", got, want)
		}
		var resp hostedStartResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Default: on. Off: not sent. Forced by the operator: sent again.
	start(f.buyerMail)
	if _, err := q.SetOrgProviderReceiptEnabled(ctx, f.orgID, false); err != nil {
		t.Fatal(err)
	}
	start("")
	if _, err := q.SetOrgProviderReceiptForced(ctx, f.orgID, true); err != nil {
		t.Fatal(err)
	}
	last := start(f.buyerMail)

	// The last checkout is paid (a new cart of the same buyer cancels the
	// earlier open order): the archive job is queued with the payment.
	csID, err := uuid.Parse(last.CheckoutSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	intents, err := q.ListPaymentIntentsByCheckout(ctx, csID)
	if err != nil || len(intents) != 1 {
		t.Fatalf("payment intents: %v %d", err, len(intents))
	}
	piRow := intents[0]
	chargeRef := "pi_rcpt_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	// The organizer's own webhook route names the configuration that took
	// the payment; the receipt is read through it.
	if code, body := postSignedWebhookTo(t, srv, f.configWebhookPath(), sessionCompletedEvent(*piRow.ProviderPaymentID, chargeRef, "paid"), f.webhookSecret); code != http.StatusOK {
		t.Fatalf("paid webhook: %d %s", code, body)
	}
	var jobs int
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs
		WHERE job_type = $1 AND payload->>'payment_intent_id' = $2`, paymentreceipts.JobType, piRow.ID.String()).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("archive jobs = %d; want 1", jobs)
	}
	// It runs ArchiveDelay after the payment (Stripe numbers a receipt only
	// after it has mailed it) and names the configuration that verified it.
	var delay time.Duration
	var configID string
	if err := pool.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM scheduled_at - now())::bigint * 1000000000,
		    COALESCE(payload->>'config_id', '')
		FROM worker_jobs WHERE job_type = $1 AND payload->>'payment_intent_id' = $2`,
		paymentreceipts.JobType, piRow.ID.String()).Scan(&delay, &configID); err != nil {
		t.Fatal(err)
	}
	if delay < paymentreceipts.ArchiveDelay-time.Minute || delay > paymentreceipts.ArchiveDelay {
		t.Fatalf("archive job runs in %s; want about %s", delay, paymentreceipts.ArchiveDelay)
	}
	if configID != f.configID.String() {
		t.Fatalf("archive job config_id = %q; want the verifying configuration %s", configID, f.configID)
	}
	if err := pool.QueryRow(ctx, `SELECT payload::text::bytea FROM worker_jobs
		WHERE job_type = $1 AND payload->>'payment_intent_id' = $2`, paymentreceipts.JobType, piRow.ID.String()).Scan(&payload); err != nil {
		t.Fatal(err)
	}

	// The worker's handler, against a stub Stripe and a local media store.
	receipts := receiptStripeStub(t, chargeRef)
	st, err := storage.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := mediastore.New(mediastore.Options{Pool: pool, Storage: st})
	if err != nil {
		t.Fatal(err)
	}
	handler := paymentreceipts.NewHandler(paymentreceipts.Options{
		Queries: q,
		Modules: hcheckout.NewRefundModuleSource(q, payments.Options{StripeAPIBaseURL: receipts.URL + "/v1"}),
		Store:   paymentreceipts.MediaStore{Repo: repo},
	})
	if err := handler(ctx, payload); err != nil {
		t.Fatalf("archive: %v", err)
	}
	got, err := q.GetPaymentIntentReceipt(ctx, piRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReceiptURL == nil || !strings.HasSuffix(*got.ReceiptURL, "/receipts/payment/rcpt?s=ap") ||
		got.ReceiptNumber == nil || *got.ReceiptNumber != "2071-4421" || got.ReceiptMediaID == nil || got.ReceiptArchivedAt == nil {
		t.Fatalf("stored receipt = %+v", got)
	}
	obj, err := repo.GetByID(ctx, *got.ReceiptMediaID)
	if err != nil {
		t.Fatal(err)
	}
	if obj.OwnerType != mediastore.OwnerPaymentReceipt || obj.ContentType != "application/pdf" ||
		obj.OwnerID == nil || *obj.OwnerID != piRow.ID || obj.OrgID == nil || *obj.OrgID != f.orgID {
		t.Fatalf("media object = %+v", obj)
	}
	// A second run keeps what is archived and asks Stripe nothing.
	receipts.Close()
	if err := handler(ctx, payload); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
