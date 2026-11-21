//go:build integration

// legacy_webhook_org_binding_integration_test.go — the legacy un-suffixed
// POST /v1/payment-intents/webhook binds the organization whose own secret
// verified a delivery to the payment row it moves. The organization is
// resolved from the same provider payment id (data.object.id) the processing
// looks the intent up by, never from arena ids carried elsewhere in the body.
// A body verified with an organization secret must be the provider's event
// envelope: the flat shape and a body-supplied target_state stay with the mock
// provider in dev.
//
// Prerequisites: DATABASE_URL against a migrated database.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// legacyBindingSession starts a hosted checkout for f and returns the
// checkout session id, the payment intent's arena id and its provider id.
func legacyBindingSession(t *testing.T, ctx context.Context, srv *Server, q *gen.Queries, f *hostedOrgFixture) (csID, intentID uuid.UUID, providerID string) {
	t.Helper()
	code, body := f.startCheckout(t, srv, hostedTicketsBaseURL+"/shows")
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode checkout/start: %v", err)
	}
	id, err := uuid.Parse(start.CheckoutSession.ID)
	if err != nil {
		t.Fatalf("checkout_session.id is not a UUID: %v", err)
	}
	intents, err := q.ListPaymentIntentsByCheckout(ctx, id)
	if err != nil || len(intents) != 1 || intents[0].ProviderPaymentID == nil {
		t.Fatalf("expected exactly one payment intent with a provider id; got %v (err %v)", intents, err)
	}
	return id, intents[0].ID, *intents[0].ProviderPaymentID
}

// withTopLevel adds top-level fields to a JSON object body.
func withTopLevel(t *testing.T, body []byte, fields map[string]any) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("withTopLevel: %v", err)
	}
	for k, v := range fields {
		m[k] = v
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("withTopLevel: %v", err)
	}
	return out
}

func intentStateByProviderID(t *testing.T, ctx context.Context, q *gen.Queries, providerID string) string {
	t.Helper()
	pi, err := q.GetPaymentIntentByProviderID(ctx, providerID)
	if err != nil {
		t.Fatalf("GetPaymentIntentByProviderID(%s): %v", providerID, err)
	}
	return pi.State
}

func issueTicketJobs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, csID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM worker_jobs WHERE job_type = 'checkout.issue_tickets' AND payload->>'checkout_session_id' = $1`,
		csID.String()).Scan(&n); err != nil {
		t.Fatalf("count checkout.issue_tickets jobs: %v", err)
	}
	return n
}

// TestLegacyWebhook_OrgSecretMovesOnlyItsOwnPayment: an event naming org A's
// provider payment id, with org B's arena payment intent id at the top level,
// signed with org B's own secret, leaves A's intent unchanged. The same event
// signed with A's secret moves A's intent to succeeded.
func TestLegacyWebhook_OrgSecretMovesOnlyItsOwnPayment(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	orgA := newHostedOrgFixture(t, ctx, pool, "la")
	defer orgA.cleanup()
	orgB := newHostedOrgFixture(t, ctx, pool, "lb")
	defer orgB.cleanup()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())
	q := gen.New(pool)

	csA, _, providerA := legacyBindingSession(t, ctx, srv, q, orgA)
	_, intentB, providerB := legacyBindingSession(t, ctx, srv, q, orgB)

	event := withTopLevel(t, sessionCompletedEvent(providerA, "pi_binding_"+uuid.NewString()[:8], "paid"),
		map[string]any{"payment_intent_id": intentB.String()})

	code, body := postSignedWebhook(t, srv, event, orgB.webhookSecret)
	if code < 400 || code >= 500 {
		t.Fatalf("org A's payment signed with org B's secret = %d, want 4xx; body: %s", code, body)
	}
	if st := intentStateByProviderID(t, ctx, q, providerA); st != "created" {
		t.Errorf("org A's intent state = %q after org B's delivery; want created", st)
	}
	if st := intentStateByProviderID(t, ctx, q, providerB); st != "created" {
		t.Errorf("org B's intent state = %q; want created", st)
	}
	if n := issueTicketJobs(t, ctx, pool, csA); n != 0 {
		t.Errorf("org A has %d checkout.issue_tickets jobs after org B's delivery; want 0", n)
	}

	code, body = postSignedWebhook(t, srv, event, orgA.webhookSecret)
	if code != http.StatusOK {
		t.Fatalf("org A's event signed with org A's secret = %d, want 200; body: %s", code, body)
	}
	var resp map[string]any
	_ = json.Unmarshal(body, &resp)
	if processed, _ := resp["processed"].(bool); !processed {
		t.Fatalf("org A's own event was not processed; body: %s", body)
	}
	if st := intentStateByProviderID(t, ctx, q, providerA); st != "succeeded" {
		t.Errorf("org A's intent state = %q; want succeeded", st)
	}
	if st := intentStateByProviderID(t, ctx, q, providerB); st != "created" {
		t.Errorf("org B's intent state = %q after org A's payment; want created", st)
	}
}

// TestLegacyWebhook_OrgSignedBodyMustBeTheEnvelope: with a platform secret
// configured, a flat body naming org A's payment is not verified by A's own
// secret, and an envelope verified by A's secret that also carries a
// target_state is refused. A's intent stays unchanged either way.
func TestLegacyWebhook_OrgSignedBodyMustBeTheEnvelope(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	orgA := newHostedOrgFixture(t, ctx, pool, "lf")
	defer orgA.cleanup()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL(), func(c *config.Config) {
		c.StripeWebhookSecret = "whsec_platform_" + orgA.orgID.String()[:8]
	})
	q := gen.New(pool)

	csA, intentA, providerA := legacyBindingSession(t, ctx, srv, q, orgA)

	flat, _ := json.Marshal(map[string]any{
		"provider_payment_id": providerA,
		"payment_intent_id":   intentA.String(),
		"event_type":          "payment_intent.succeeded",
		"target_state":        "succeeded",
	})
	code, body := postSignedWebhook(t, srv, flat, orgA.webhookSecret)
	if code != http.StatusUnauthorized {
		t.Fatalf("flat body signed with the org secret = %d, want 401; body: %s", code, body)
	}

	envelope := withTopLevel(t, sessionCompletedEvent(providerA, "pi_shape_"+uuid.NewString()[:8], "paid"),
		map[string]any{"target_state": "succeeded"})
	code, body = postSignedWebhook(t, srv, envelope, orgA.webhookSecret)
	if code != http.StatusBadRequest {
		t.Fatalf("envelope with target_state signed with the org secret = %d, want 400; body: %s", code, body)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	if env.Error.Code != "webhook.envelope_required" {
		t.Errorf("error code = %q; want webhook.envelope_required; body: %s", env.Error.Code, body)
	}

	if st := intentStateByProviderID(t, ctx, q, providerA); st != "created" {
		t.Errorf("org A's intent state = %q; want created", st)
	}
	if n := issueTicketJobs(t, ctx, pool, csA); n != 0 {
		t.Errorf("org A has %d checkout.issue_tickets jobs; want 0", n)
	}
}
