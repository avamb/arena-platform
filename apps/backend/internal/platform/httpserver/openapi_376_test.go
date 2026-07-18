package httpserver

// Feature #376 — PR2-20: Restore OpenAPI-first contract and add a client-drift CI gate
//
// Acceptance criteria:
//   Step 1: All admin and public paths that are mounted by the Go server
//           are documented in openapi.yaml.
//   Step 2: The generated TypeScript client (index.d.ts) is committed and
//           up-to-date (it must be non-empty).
//   Step 3: The CI workflow contains a client-drift step that runs
//           generate-clients.sh and fails on any diff.
//   Step 4: The openapi-check CI job verifies client drift in addition to
//           generated Go types drift.

import (
	"strings"
	"testing"
)

// openAPIContent returns the content of the openapi.yaml spec.
func openAPIContent(t *testing.T) string {
	t.Helper()
	return findFileByName(t, "apps/backend/openapi/openapi.yaml")
}

// tsClientContent returns the content of the generated TypeScript client.
func tsClientContent(t *testing.T) string {
	t.Helper()
	return findFileByName(t, "apps/backend/openapi/clients/ts/index.d.ts")
}

// ─── Step 1: Missing paths now documented in openapi.yaml ─────────────────────

// Channels
func TestOpenAPI376_ChannelsRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/organizations/{org_id}/channels:") {
		t.Error("openapi.yaml missing /v1/organizations/{org_id}/channels path")
	}
}

func TestOpenAPI376_ChannelByIDRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/organizations/{org_id}/channels/{id}:") {
		t.Error("openapi.yaml missing /v1/organizations/{org_id}/channels/{id} path")
	}
}

// Feed tokens
func TestOpenAPI376_FeedTokensRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/organizations/{org_id}/channels/{channel_id}/feed-tokens:") {
		t.Error("openapi.yaml missing /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens path")
	}
}

func TestOpenAPI376_FeedTokenByIDRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}:") {
		t.Error("openapi.yaml missing /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id} path")
	}
}

func TestOpenAPI376_PublicFeedReadRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/feeds/{token}:") {
		t.Error("openapi.yaml missing /v1/feeds/{token} path (public agent feed read)")
	}
}

// GDPR / consent
func TestOpenAPI376_GDPRDataRequestsRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/me/data-requests:") {
		t.Error("openapi.yaml missing /v1/me/data-requests path")
	}
}

func TestOpenAPI376_GDPRDataExportRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/me/data-export:") {
		t.Error("openapi.yaml missing /v1/me/data-export path")
	}
}

func TestOpenAPI376_GDPRDataDeleteRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/me/data-delete:") {
		t.Error("openapi.yaml missing /v1/me/data-delete path")
	}
}

func TestOpenAPI376_GDPRConsentRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/me/consent:") {
		t.Error("openapi.yaml missing /v1/me/consent path")
	}
}

// Stripe Connect
func TestOpenAPI376_StripeConnectAuthorizeRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/stripe/connect/authorize:") {
		t.Error("openapi.yaml missing /v1/stripe/connect/authorize path")
	}
}

func TestOpenAPI376_StripeConnectCallbackRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/stripe/connect/callback:") {
		t.Error("openapi.yaml missing /v1/stripe/connect/callback path")
	}
}

// Barcode batches
func TestOpenAPI376_BarcodeBatchesRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/barcode-batches:") {
		t.Error("openapi.yaml missing /v1/barcode-batches path")
	}
}

func TestOpenAPI376_BarcodeBatchByIDRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/barcode-batches/{id}:") {
		t.Error("openapi.yaml missing /v1/barcode-batches/{id} path")
	}
}

func TestOpenAPI376_BarcodeBatchApproveRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/barcode-batches/{id}/approve:") {
		t.Error("openapi.yaml missing /v1/barcode-batches/{id}/approve path")
	}
}

func TestOpenAPI376_BarcodeBatchRejectRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/barcode-batches/{id}/reject:") {
		t.Error("openapi.yaml missing /v1/barcode-batches/{id}/reject path")
	}
}

// Reconciliation
func TestOpenAPI376_ReconciliationReportsRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/reconciliation/reports:") {
		t.Error("openapi.yaml missing /v1/reconciliation/reports path")
	}
}

func TestOpenAPI376_ReconciliationReportByIDRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/reconciliation/reports/{id}:") {
		t.Error("openapi.yaml missing /v1/reconciliation/reports/{id} path")
	}
}

func TestOpenAPI376_ReconciliationReportReviewRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/reconciliation/reports/{id}/review:") {
		t.Error("openapi.yaml missing /v1/reconciliation/reports/{id}/review path")
	}
}

func TestOpenAPI376_ReconciliationExceptionsRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/reconciliation/exceptions:") {
		t.Error("openapi.yaml missing /v1/reconciliation/exceptions path")
	}
}

func TestOpenAPI376_ReconciliationReportLineLookupRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/reconciliation/reports/{id}/lines/{line_id}:") {
		t.Error("openapi.yaml missing /v1/reconciliation/reports/{id}/lines/{line_id} path")
	}
}

// External allocations
func TestOpenAPI376_ExternalAllocationsRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/organizations/{org_id}/external-allocations:") {
		t.Error("openapi.yaml missing /v1/organizations/{org_id}/external-allocations path")
	}
}

func TestOpenAPI376_ExternalAllocationByIDRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/organizations/{org_id}/external-allocations/{id}:") {
		t.Error("openapi.yaml missing /v1/organizations/{org_id}/external-allocations/{id} path")
	}
}

// Complimentary
func TestOpenAPI376_ComplimentaryRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/organizations/{org_id}/complimentary:") {
		t.Error("openapi.yaml missing /v1/organizations/{org_id}/complimentary path")
	}
}

func TestOpenAPI376_ComplimentaryByIDRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/organizations/{org_id}/complimentary/{id}:") {
		t.Error("openapi.yaml missing /v1/organizations/{org_id}/complimentary/{id} path")
	}
}

func TestOpenAPI376_ComplimentaryRevokeRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/complimentary/{id}/revoke:") {
		t.Error("openapi.yaml missing /v1/complimentary/{id}/revoke path")
	}
}

// Reports
func TestOpenAPI376_EventReportRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/events/{event_id}/report:") {
		t.Error("openapi.yaml missing /v1/events/{event_id}/report path")
	}
}

// Billing
func TestOpenAPI376_BillingTariffsActiveRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/billing/tariffs/active:") {
		t.Error("openapi.yaml missing /v1/billing/tariffs/active path")
	}
}

func TestOpenAPI376_BillingTariffsCreateRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/billing/tariffs:") {
		t.Error("openapi.yaml missing /v1/billing/tariffs path")
	}
}

func TestOpenAPI376_BillingInvoiceByIDRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/billing/invoices/{id}:") {
		t.Error("openapi.yaml missing /v1/billing/invoices/{id} path")
	}
}

func TestOpenAPI376_BillingInvoiceGenerateRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/billing/invoices/generate:") {
		t.Error("openapi.yaml missing /v1/billing/invoices/generate path")
	}
}

func TestOpenAPI376_BillingInvoiceActionsRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	// issue, pay, void
	if !strings.Contains(spec, "/v1/billing/invoices/{id}/issue:") {
		t.Error("openapi.yaml missing /v1/billing/invoices/{id}/issue path")
	}
}

func TestOpenAPI376_BillingStripeWebhookRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/billing/stripe/webhook:") {
		t.Error("openapi.yaml missing /v1/billing/stripe/webhook path")
	}
}

func TestOpenAPI376_OrgBillingUsageRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/organizations/{org_id}/billing/usage:") {
		t.Error("openapi.yaml missing /v1/organizations/{org_id}/billing/usage path")
	}
}

func TestOpenAPI376_OrgBillingInvoicesRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/organizations/{org_id}/billing/invoices:") {
		t.Error("openapi.yaml missing /v1/organizations/{org_id}/billing/invoices path")
	}
}

// Superadmin cross-tenant read
func TestOpenAPI376_AdminOrdersRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/admin/orders:") {
		t.Error("openapi.yaml missing /v1/admin/orders path")
	}
}

func TestOpenAPI376_AdminTicketsRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/admin/tickets:") {
		t.Error("openapi.yaml missing /v1/admin/tickets path")
	}
}

func TestOpenAPI376_AdminRefundsRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/admin/refunds:") {
		t.Error("openapi.yaml missing /v1/admin/refunds path")
	}
}

func TestOpenAPI376_AdminTicketScansRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	if !strings.Contains(spec, "/v1/admin/tickets/{id}/scans:") {
		t.Error("openapi.yaml missing /v1/admin/tickets/{id}/scans path")
	}
}

// Public feed funnel events (POST)
func TestOpenAPI376_PublicFeedFunnelEventsRouteDocumented(t *testing.T) {
	spec := openAPIContent(t)
	// The path /v1/public/feeds/{feed_token}/events supports both GET and POST
	// GET is already in the spec; verify POST is also documented (funnel telemetry)
	feedEventsIdx := strings.Index(spec, "/v1/public/feeds/{feed_token}/events:")
	if feedEventsIdx < 0 {
		t.Fatal("openapi.yaml missing /v1/public/feeds/{feed_token}/events path entirely")
	}
	// Find the next path header to bound the window for this path's methods
	pathSection := spec[feedEventsIdx:]
	nextPathIdx := strings.Index(pathSection[1:], "\n  /v1/")
	if nextPathIdx > 0 {
		pathSection = pathSection[:nextPathIdx+1]
	}
	if !strings.Contains(pathSection, "post:") {
		t.Error("openapi.yaml /v1/public/feeds/{feed_token}/events is missing the POST method (funnel telemetry sink)")
	}
}

// ─── Step 2: TypeScript client is non-empty and up-to-date ────────────────────

func TestOpenAPI376_TSClientFileExists(t *testing.T) {
	content := tsClientContent(t)
	if len(content) == 0 {
		t.Error("apps/backend/openapi/clients/ts/index.d.ts is empty or missing")
	}
}

func TestOpenAPI376_TSClientHasPathsType(t *testing.T) {
	content := tsClientContent(t)
	if !strings.Contains(content, "export interface paths") {
		t.Error("TypeScript client missing 'export interface paths' — file may not be generated from openapi-typescript")
	}
}

func TestOpenAPI376_TSClientHasComponentsType(t *testing.T) {
	content := tsClientContent(t)
	if !strings.Contains(content, "export interface components") {
		t.Error("TypeScript client missing 'export interface components'")
	}
}

// ─── Step 3 & 4: CI gate for client-drift ─────────────────────────────────────

func TestOpenAPI376_CIGateRunsGenerateClients(t *testing.T) {
	content := ciWorkflowContent(t)
	// The openapi-check job must invoke generate-clients.sh (or make gen-ts-client)
	hasGenClients := strings.Contains(content, "generate-clients.sh") ||
		strings.Contains(content, "gen-ts-client") ||
		strings.Contains(content, "gen_ts_client")
	if !hasGenClients {
		t.Error("ci.yml openapi-check job does not call generate-clients.sh or make gen-ts-client")
	}
}

func TestOpenAPI376_CIGateChecksClientDrift(t *testing.T) {
	content := ciWorkflowContent(t)
	// Must use git diff to detect drift in the TS client
	if !strings.Contains(content, "git diff") {
		t.Error("ci.yml openapi-check job missing git diff to detect client drift")
	}
	// Must reference the TS client path
	hasTSCheck := strings.Contains(content, "index.d.ts") ||
		strings.Contains(content, "clients/ts") ||
		strings.Contains(content, "openapi/clients")
	if !hasTSCheck {
		t.Error("ci.yml openapi-check job does not check the TypeScript client for drift")
	}
}

// ─── Full verification ─────────────────────────────────────────────────────────

func TestOpenAPI376_FullVerification(t *testing.T) {
	spec := openAPIContent(t)
	tsClient := tsClientContent(t)
	ci := ciWorkflowContent(t)

	// Count paths documented in spec (should be significantly more than 121)
	pathCount := strings.Count(spec, "\n  /v1/")
	if pathCount < 130 {
		t.Errorf("openapi.yaml only has %d /v1/ paths; expected >= 130 (many missing paths not yet documented)", pathCount)
	}

	// TS client must be generated
	if !strings.Contains(tsClient, "export interface paths") {
		t.Error("TypeScript client is not generated from openapi.yaml")
	}

	// CI drift gate must exist
	hasGenClients := strings.Contains(ci, "generate-clients.sh") ||
		strings.Contains(ci, "gen-ts-client")
	if !hasGenClients {
		t.Error("ci.yml missing generate-clients.sh step in openapi-check job")
	}
}
