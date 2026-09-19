package observability_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// The payment-webhook counters are fed by an UNAUTHENTICATED endpoint that
// anyone on the internet can POST to, carrying a provider-chosen event type.
// A label that passes that string through unbounded is a metrics-backend
// outage waiting to happen, so the bounding is the thing worth testing.

func TestPaymentWebhookEventLabel_KeepsKnownTypes(t *testing.T) {
	for _, known := range []string{
		"checkout.session.completed",
		"checkout.session.expired",
		"checkout.session.async_payment_succeeded",
		"checkout.session.async_payment_failed",
		"payment_intent.succeeded",
		"payment_intent.payment_failed",
		"mock.succeeded",
	} {
		if got := observability.PaymentWebhookEventLabel(known); got != known {
			t.Errorf("PaymentWebhookEventLabel(%q) = %q; a handled event type must keep its own label", known, got)
		}
	}
}

func TestPaymentWebhookEventLabel_CollapsesEverythingElse(t *testing.T) {
	// Stripe publishes well over a hundred event types and an organizer's
	// account is shared with their other sites, so genuinely unknown ones
	// arrive constantly — alongside anything an attacker cares to invent.
	for _, unknown := range []string{
		"",
		"invoice.paid",
		"customer.subscription.deleted",
		"radar.early_fraud_warning.created",
		"CHECKOUT.SESSION.COMPLETED", // case differs: not the same type
		"checkout.session.completed.extra",
		strings.Repeat("a", 4096),
		"\x00\x01",
	} {
		if got := observability.PaymentWebhookEventLabel(unknown); got != observability.PaymentWebhookEventOther {
			t.Errorf("PaymentWebhookEventLabel(%q) = %q; want %q so the label stays bounded",
				unknown, got, observability.PaymentWebhookEventOther)
		}
	}
}

func TestPaymentWebhookCounters_AreRegisteredAndLabelled(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := observability.New(reg)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	if m.PaymentWebhookSignatureFailuresTotal == nil {
		t.Fatal("PaymentWebhookSignatureFailuresTotal is nil")
	}
	if m.PaymentWebhookEventsTotal == nil {
		t.Fatal("PaymentWebhookEventsTotal is nil")
	}

	m.PaymentWebhookSignatureFailuresTotal.WithLabelValues(observability.RouteKindLegacy).Inc()
	m.PaymentWebhookSignatureFailuresTotal.WithLabelValues(observability.RouteKindConfig).Inc()
	m.PaymentWebhookEventsTotal.WithLabelValues(
		observability.PaymentWebhookEventLabel("checkout.session.completed"), "processed").Inc()
	m.PaymentWebhookEventsTotal.WithLabelValues(
		observability.PaymentWebhookEventLabel("invoice.paid"), "not_ours").Inc()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("registry.Gather: %v", err)
	}

	want := map[string]bool{
		"arena_payment_webhook_signature_failures_total": false,
		"arena_payment_webhook_events_total":             false,
	}
	for _, f := range families {
		if _, tracked := want[f.GetName()]; tracked {
			want[f.GetName()] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("metric %q was not gathered from the registry", name)
		}
	}
}

// TestPaymentWebhookRouteKinds_AreTheOnlyTwo guards the promise that these
// labels never carry an org or config id: there are exactly two routes, and
// both values are compile-time constants.
func TestPaymentWebhookRouteKinds_AreTheOnlyTwo(t *testing.T) {
	if observability.RouteKindLegacy != "legacy" {
		t.Errorf("RouteKindLegacy = %q; want legacy", observability.RouteKindLegacy)
	}
	if observability.RouteKindConfig != "config" {
		t.Errorf("RouteKindConfig = %q; want config", observability.RouteKindConfig)
	}
	if observability.RouteKindLegacy == observability.RouteKindConfig {
		t.Error("the two route kinds must be distinguishable")
	}
}
