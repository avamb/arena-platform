// wid_0d_321_test.go — unit tests for feature #321 (WID-0d):
// Buyer-field flags in public payload.
//
// Test coverage:
//   - Static analysis: migration adds collect_name / collect_phone columns
//   - Static analysis: public_feed.sql.go has GetFeedTokenBuyerFlags
//   - Static analysis: querier.go has GetFeedTokenBuyerFlags in interface
//   - Static analysis: public_feed.go has BuyerFields + BuildBuyerFields
//   - Static analysis: public_feed_checkout.go has PublicBuyerInfo struct
//   - Static analysis: checkout handler validates collect_name/collect_phone
//   - Compile-time: *gen.Queries still satisfies gen.Querier (includes new method)
//   - Unit: BuildBuyerFields returns correct buyer_fields list for flags off
//   - Unit: BuildBuyerFields returns correct buyer_fields list for name flag on
//   - Unit: BuildBuyerFields returns correct buyer_fields list for both flags on
//   - HTTP: buyer.email supersedes holder_email
//   - HTTP: flags off — name/phone omitted → request passes validation (reaches DB)
//   - HTTP: buyer.name + buyer.phone present → passes validation (reaches DB)
//   - Backward compat: existing holder_email-only request still accepted
//   - OpenAPI: BuyerFieldItem and PublicBuyerInfo schemas present
//   - SQL: public_feed.sql has GetFeedTokenBuyerFlags query
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factory (re-uses dbDownPool defined in checkout_132_test.go)
// ─────────────────────────────────────────────────────────────────────────────

func buildWID0dServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	return New(Options{
		Config:             cfg,
		PublicFeedQueries:  gen.New(nil),
		CheckoutQueries:    gen.New(nil),
		ReservationQueries: gen.New(nil),
		InventoryQueries:   gen.New(nil),
		TierQueries:        gen.New(nil),
		PromoQueries:       gen.New(nil),
		Pool:               &dbDownPool{},
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration adds buyer-field flag columns
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_Step1_MigrationAddsCollectNameColumn(t *testing.T) {
	content := findFileByName(t, "0061_buyer_field_flags.sql")
	if !strings.Contains(content, "collect_name") {
		t.Error("0061_buyer_field_flags.sql: expected 'collect_name' column")
	}
	if !strings.Contains(content, "collect_phone") {
		t.Error("0061_buyer_field_flags.sql: expected 'collect_phone' column")
	}
	if !strings.Contains(content, "DEFAULT false") {
		t.Error("0061_buyer_field_flags.sql: expected 'DEFAULT false' for new columns")
	}
}

func TestWID0d321_Step1_MigrationHasDownSection(t *testing.T) {
	content := findFileByName(t, "0061_buyer_field_flags.sql")
	if !strings.Contains(content, "goose Down") {
		t.Error("0061_buyer_field_flags.sql: expected goose Down section")
	}
	if !strings.Contains(content, "DROP COLUMN") {
		t.Error("0061_buyer_field_flags.sql: expected DROP COLUMN in Down section")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: public_feed.sql has GetFeedTokenBuyerFlags query
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_Step2_SQLQueryFileHasGetFeedTokenBuyerFlags(t *testing.T) {
	content := findFileByName(t, "public_feed.sql")
	if !strings.Contains(content, "GetFeedTokenBuyerFlags") {
		t.Error("public_feed.sql: expected 'GetFeedTokenBuyerFlags' query")
	}
	if !strings.Contains(content, "collect_name") {
		t.Error("public_feed.sql: expected 'collect_name' in GetFeedTokenBuyerFlags query")
	}
	if !strings.Contains(content, "collect_phone") {
		t.Error("public_feed.sql: expected 'collect_phone' in GetFeedTokenBuyerFlags query")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: public_feed.sql.go has GetFeedTokenBuyerFlags implementation
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_Step3_GenFileHasGetFeedTokenBuyerFlags(t *testing.T) {
	content := findFileByName(t, "public_feed.sql.go")
	if !strings.Contains(content, "GetFeedTokenBuyerFlags") {
		t.Error("public_feed.sql.go: expected 'GetFeedTokenBuyerFlags' function")
	}
	if !strings.Contains(content, "FeedTokenBuyerFlagsRow") {
		t.Error("public_feed.sql.go: expected 'FeedTokenBuyerFlagsRow' type")
	}
	if !strings.Contains(content, "CollectName") {
		t.Error("public_feed.sql.go: expected 'CollectName' field in FeedTokenBuyerFlagsRow")
	}
	if !strings.Contains(content, "CollectPhone") {
		t.Error("public_feed.sql.go: expected 'CollectPhone' field in FeedTokenBuyerFlagsRow")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: querier.go has GetFeedTokenBuyerFlags in interface
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_Step4_QuerierInterfaceHasGetFeedTokenBuyerFlags(t *testing.T) {
	content := findFileByName(t, "querier.go")
	if !strings.Contains(content, "GetFeedTokenBuyerFlags") {
		t.Error("querier.go: expected 'GetFeedTokenBuyerFlags' in Querier interface")
	}
}

// Compile-time check: *gen.Queries must satisfy gen.Querier (includes GetFeedTokenBuyerFlags).
var _ gen.Querier = (*gen.Queries)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: public_feed.go exposes buyer_fields in session payload
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_Step5_PublicFeedHasBuyerFields(t *testing.T) {
	content := findFileByName(t, "public_feed.go")
	if !strings.Contains(content, "BuyerFields") {
		t.Error("public_feed.go: expected 'BuyerFields' field in session response")
	}
	if !strings.Contains(content, "BuyerFieldItem") {
		t.Error("public_feed.go: expected 'BuyerFieldItem' type")
	}
	if !strings.Contains(content, "BuildBuyerFields") {
		t.Error("public_feed.go: expected 'BuildBuyerFields' exported helper function")
	}
	if !strings.Contains(content, "buyer_fields") {
		t.Error("public_feed.go: expected 'buyer_fields' json tag")
	}
}

func TestWID0d321_Step5_PublicFeedFetchesBuyerFlagsFromToken(t *testing.T) {
	content := findFileByName(t, "public_feed.go")
	if !strings.Contains(content, "GetFeedTokenBuyerFlags") {
		t.Error("public_feed.go: expected call to 'GetFeedTokenBuyerFlags'")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: checkout handler has PublicBuyerInfo and validates flags
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_Step6_CheckoutHandlerHasPublicBuyerInfo(t *testing.T) {
	content := findFileByName(t, "public_feed_checkout.go")
	if !strings.Contains(content, "PublicBuyerInfo") {
		t.Error("public_feed_checkout.go: expected 'PublicBuyerInfo' type")
	}
	if !strings.Contains(content, "Buyer *PublicBuyerInfo") {
		t.Error("public_feed_checkout.go: expected 'Buyer *PublicBuyerInfo' field")
	}
}

func TestWID0d321_Step6_CheckoutHandlerValidatesBuyerFlags(t *testing.T) {
	content := findFileByName(t, "public_feed_checkout.go")
	if !strings.Contains(content, "checkout.buyer_name_required") {
		t.Error("public_feed_checkout.go: expected 'checkout.buyer_name_required' error code")
	}
	if !strings.Contains(content, "checkout.buyer_phone_required") {
		t.Error("public_feed_checkout.go: expected 'checkout.buyer_phone_required' error code")
	}
	if !strings.Contains(content, "collectName") {
		t.Error("public_feed_checkout.go: expected 'collectName' variable")
	}
	if !strings.Contains(content, "collectPhone") {
		t.Error("public_feed_checkout.go: expected 'collectPhone' variable")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Unit: BuildBuyerFields logic
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_BuildBuyerFields_FlagsOff(t *testing.T) {
	fields := hfeed.BuildBuyerFields(false, false)
	if len(fields) != 3 {
		t.Fatalf("expected 3 buyer_fields items, got %d", len(fields))
	}
	// email: always required + enabled
	if fields[0].Key != "email" {
		t.Errorf("fields[0].Key: expected 'email', got %q", fields[0].Key)
	}
	if !fields[0].Required {
		t.Error("fields[0] (email): expected Required=true")
	}
	if !fields[0].Enabled {
		t.Error("fields[0] (email): expected Enabled=true")
	}
	// name: disabled when collect_name=false
	if fields[1].Key != "name" {
		t.Errorf("fields[1].Key: expected 'name', got %q", fields[1].Key)
	}
	if fields[1].Enabled {
		t.Error("fields[1] (name): expected Enabled=false when collect_name=false")
	}
	if fields[1].Required {
		t.Error("fields[1] (name): expected Required=false when collect_name=false")
	}
	// phone: disabled when collect_phone=false
	if fields[2].Key != "phone" {
		t.Errorf("fields[2].Key: expected 'phone', got %q", fields[2].Key)
	}
	if fields[2].Enabled {
		t.Error("fields[2] (phone): expected Enabled=false when collect_phone=false")
	}
	if fields[2].Required {
		t.Error("fields[2] (phone): expected Required=false when collect_phone=false")
	}
}

func TestWID0d321_BuildBuyerFields_NameEnabled(t *testing.T) {
	fields := hfeed.BuildBuyerFields(true, false)
	if len(fields) != 3 {
		t.Fatalf("expected 3 buyer_fields items, got %d", len(fields))
	}
	if !fields[1].Enabled {
		t.Error("fields[1] (name): expected Enabled=true when collect_name=true")
	}
	if !fields[1].Required {
		t.Error("fields[1] (name): expected Required=true when collect_name=true")
	}
	if fields[2].Enabled {
		t.Error("fields[2] (phone): expected Enabled=false when collect_phone=false")
	}
}

func TestWID0d321_BuildBuyerFields_BothEnabled(t *testing.T) {
	fields := hfeed.BuildBuyerFields(true, true)
	if len(fields) != 3 {
		t.Fatalf("expected 3 buyer_fields items, got %d", len(fields))
	}
	for _, f := range fields {
		if !f.Enabled {
			t.Errorf("field %q: expected Enabled=true when both flags on, got false", f.Key)
		}
		if !f.Required {
			t.Errorf("field %q: expected Required=true when both flags on, got false", f.Key)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP: buyer.email supersedes holder_email
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_BuyerEmailSupersedes_HolderEmail(t *testing.T) {
	// With gen.New(nil), DB calls panic → handler returns 503/500. But the
	// request must not be rejected with 400 "missing_holder_email" since
	// buyer.email is provided — implying the merge works.
	s := buildWID0dServer(t)
	w := httptest.NewRecorder()
	body := `{
		"session_id":   "00000000-0000-0000-0000-000000000002",
		"ga_items":     [{"tier_id": "00000000-0000-0000-0000-000000000001", "quantity": 1}],
		"buyer":        {"email": "buyer@example.com"}
	}`
	// Note: no holder_email — buyer.email should be used instead.
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	// Must not be 400 with missing_holder_email.
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "missing_holder_email") {
		t.Fatalf("buyer.email should supersede holder_email; got 400 missing_holder_email: %s", w.Body.String())
	}
	// Must not be 401 (public endpoint).
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401 — public endpoint, no auth needed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP: flags off — request without buyer name/phone passes validation
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_FlagsOff_NoNameOrPhone_PassesValidation(t *testing.T) {
	// With DB nil, GetFeedTokenBuyerFlags returns an error. The handler treats
	// that as "flags all off", so buyer.name/phone are optional.
	s := buildWID0dServer(t)
	w := httptest.NewRecorder()
	body := `{
		"session_id":   "00000000-0000-0000-0000-000000000002",
		"holder_email": "buyer@example.com",
		"ga_items":     [{"tier_id": "00000000-0000-0000-0000-000000000001", "quantity": 1}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	// Must not be 422 (buyer field required).
	if w.Code == http.StatusUnprocessableEntity {
		t.Fatalf("expected not 422 when flags are off; got: %s", w.Body.String())
	}
	// Must not be a 400 due to buyer field validation.
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "buyer") {
		t.Fatalf("expected not buyer-related 400 when flags off; got: %s", w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP: buyer object with name+phone present → passes validation
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_BuyerWithNameAndPhone_PassesValidation(t *testing.T) {
	s := buildWID0dServer(t)
	w := httptest.NewRecorder()
	body := `{
		"session_id":   "00000000-0000-0000-0000-000000000002",
		"holder_email": "alice@example.com",
		"ga_items":     [{"tier_id": "00000000-0000-0000-0000-000000000001", "quantity": 1}],
		"buyer": {
			"email": "alice@example.com",
			"name":  "Alice Smith",
			"phone": "+1234567890"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	// Must not be 422 or a buyer-related 400.
	if w.Code == http.StatusUnprocessableEntity {
		t.Fatalf("expected not 422 for request with name+phone; got: %s", w.Body.String())
	}
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "buyer") {
		t.Fatalf("expected not buyer-related 400; got: %s", w.Body.String())
	}
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401 — public endpoint")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP: backward compat — holder_email-only still works
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_BackwardCompat_HolderEmailOnly(t *testing.T) {
	s := buildWID0dServer(t)
	w := httptest.NewRecorder()
	body := `{
		"session_id":   "00000000-0000-0000-0000-000000000002",
		"holder_email": "legacy@example.com",
		"ga_items":     [{"tier_id": "00000000-0000-0000-0000-000000000001", "quantity": 2}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/test-token/checkout/start",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code == http.StatusBadRequest {
		t.Fatalf("backward-compat holder_email-only request rejected with 400: %s", w.Body.String())
	}
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("expected not 401 — public endpoint")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time: PublicBuyerInfo fields accessible from hfeed package
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_PublicBuyerInfoFields(t *testing.T) {
	name := "Alice"
	phone := "+1234567890"
	buyer := hfeed.PublicBuyerInfo{
		Email: "alice@example.com",
		Name:  &name,
		Phone: &phone,
	}
	if buyer.Email != "alice@example.com" {
		t.Errorf("PublicBuyerInfo.Email: expected 'alice@example.com', got %q", buyer.Email)
	}
	if buyer.Name == nil || *buyer.Name != "Alice" {
		t.Error("PublicBuyerInfo.Name: expected 'Alice'")
	}
	if buyer.Phone == nil || *buyer.Phone != "+1234567890" {
		t.Error("PublicBuyerInfo.Phone: expected '+1234567890'")
	}
}

func TestWID0d321_RequestStruct_HasBuyerField(t *testing.T) {
	name := "Bob"
	buyer := hfeed.PublicBuyerInfo{Email: "bob@example.com", Name: &name}
	req := publicFeedCheckoutStartRequest{
		SessionID:   "00000000-0000-0000-0000-000000000002",
		HolderEmail: "bob@example.com",
		GaItems:     []publicGAItem{{TierID: "00000000-0000-0000-0000-000000000001", Quantity: 1}},
		Buyer:       &buyer,
	}
	if req.SessionID == "" || req.HolderEmail == "" || len(req.GaItems) != 1 {
		t.Error("existing request fields must remain settable alongside the Buyer field")
	}
	if req.Buyer == nil {
		t.Error("publicFeedCheckoutStartRequest.Buyer field not accessible")
	}
	if req.Buyer.Email != "bob@example.com" {
		t.Errorf("Buyer.Email: expected 'bob@example.com', got %q", req.Buyer.Email)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// OpenAPI: new schemas present
// ─────────────────────────────────────────────────────────────────────────────

func TestWID0d321_OpenAPIHasBuyerFieldItem(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	if !strings.Contains(content, "BuyerFieldItem") {
		t.Error("openapi.yaml: expected 'BuyerFieldItem' schema")
	}
	if !strings.Contains(content, "PublicBuyerInfo") {
		t.Error("openapi.yaml: expected 'PublicBuyerInfo' schema")
	}
	if !strings.Contains(content, "collect_name") {
		t.Error("openapi.yaml: expected 'collect_name' reference in buyer flag schemas")
	}
}

// feed_shims.go keeps the unexported request-type aliases live in package
// httpserver so the older feed tests compile without importing hfeed.
// (The former publicBuyerInfo alias was removed as unused — tests use
// hfeed.PublicBuyerInfo directly.)
func TestWID0d321_FeedShimsAlias(t *testing.T) {
	content := findFileByName(t, "feed_shims.go")
	if !strings.Contains(content, "publicFeedCheckoutStartRequest") {
		t.Error("feed_shims.go: expected 'publicFeedCheckoutStartRequest' alias")
	}
}
