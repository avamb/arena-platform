// auth_471_test.go — unit tests for feature #471 (W1-A1b): settings.gateway
// helper, fid = display_number resolution, token gate on every command, and
// org-scoped read isolation.
//
// These tests exercise the auth surface with in-memory fakes only — no live
// PostgreSQL required. The integration harness in tests/compat/bil24 covers
// the golden isolation.json against a real database when the parent scenario
// features (#497, etc.) land.

package hbil24

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// parseFIDInt64
// ─────────────────────────────────────────────────────────────────────────────

func TestParseFIDInt64(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		want   int64
		wantOK bool
	}{
		{"decimal_number", "1271", 1271, true},
		{"decimal_with_whitespace", "  42 ", 42, true},
		{"string_with_zero_padding", "007", 7, true},
		{"large_int64", "9223372036854775807", 9223372036854775807, true},
		{"empty", "", 0, false},
		{"whitespace_only", "   ", 0, false},
		{"zero_rejected", "0", 0, false},
		{"negative_rejected", "-1", 0, false},
		{"uuid_rejected", "9f7a4d1b-3b53-4b6c-9b0f-3e77d8b8ee0c", 0, false},
		{"garbage_rejected", "abc", 0, false},
		{"decimal_fraction_rejected", "1.5", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseFIDInt64(tc.raw)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("parseFIDInt64(%q) = (%d, %v); want (%d, %v)",
					tc.raw, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// parseGatewaySettings — precedence + legacy fallback
// ─────────────────────────────────────────────────────────────────────────────

func TestParseGatewaySettings_Nested(t *testing.T) {
	raw := json.RawMessage(`{"gateway":{"enabled":true,"token_hash":"h","default_locale":"cs"}}`)
	got := parseGatewaySettings(raw)
	if !got.Enabled || got.TokenHash != "h" || got.DefaultLocale != "cs" {
		t.Errorf("nested: got %+v; want enabled=true hash=h locale=cs", got)
	}
	if got.LegacyOnly {
		t.Errorf("nested: LegacyOnly must be false when gateway object present")
	}
}

func TestParseGatewaySettings_NestedDisabled(t *testing.T) {
	raw := json.RawMessage(`{"gateway":{"enabled":false,"token_hash":"h"}}`)
	got := parseGatewaySettings(raw)
	if got.Enabled {
		t.Errorf("nested-disabled: Enabled must reflect explicit false; got %+v", got)
	}
	if got.TokenHash != "h" {
		t.Errorf("nested-disabled: hash lost; got %+v", got)
	}
}

func TestParseGatewaySettings_LegacyFallback(t *testing.T) {
	raw := json.RawMessage(`{"gateway_token_hash":"legacy"}`)
	got := parseGatewaySettings(raw)
	if !got.Enabled || got.TokenHash != "legacy" || !got.LegacyOnly {
		t.Errorf("legacy: got %+v; want enabled=true hash=legacy legacy=true", got)
	}
}

func TestParseGatewaySettings_NestedWinsOverLegacy(t *testing.T) {
	raw := json.RawMessage(`{"gateway":{"enabled":true,"token_hash":"new"},"gateway_token_hash":"old"}`)
	got := parseGatewaySettings(raw)
	if got.TokenHash != "new" {
		t.Errorf("precedence: nested must win; got %+v", got)
	}
	if got.LegacyOnly {
		t.Errorf("precedence: LegacyOnly false when nested present")
	}
}

func TestParseGatewaySettings_EmptyOrMalformed(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, {}, json.RawMessage(`{}`), json.RawMessage(`not json`)} {
		got := parseGatewaySettings(raw)
		if got.Enabled || got.TokenHash != "" {
			t.Errorf("empty/malformed input %q must yield zero-value; got %+v", string(raw), got)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// authenticateCommand + resolveChannelByFID
// ─────────────────────────────────────────────────────────────────────────────

// fakeChannelLookup satisfies ChannelLookupQuerier with in-memory maps.
type fakeChannelLookup struct {
	byDisplayNumber map[int64]gen.SalesChannelRow
	byUUID          map[uuid.UUID]gen.SalesChannelRow
}

func (f *fakeChannelLookup) GetSalesChannelByDisplayNumber(_ context.Context, dn int64) (gen.SalesChannelRow, error) {
	if ch, ok := f.byDisplayNumber[dn]; ok {
		return ch, nil
	}
	return gen.SalesChannelRow{}, pgx.ErrNoRows
}

func (f *fakeChannelLookup) GetSalesChannelByIDGlobal(_ context.Context, id uuid.UUID) (gen.SalesChannelRow, error) {
	if ch, ok := f.byUUID[id]; ok {
		return ch, nil
	}
	return gen.SalesChannelRow{}, pgx.ErrNoRows
}

func buildAuthHandler(t *testing.T, ch gen.SalesChannelRow, requireToken bool) *Handler {
	t.Helper()
	lookup := &fakeChannelLookup{
		byDisplayNumber: map[int64]gen.SalesChannelRow{ch.DisplayNumber: ch},
		byUUID:          map[uuid.UUID]gen.SalesChannelRow{ch.ID: ch},
	}
	h := New(nil, nil, nil, nil, nil, nil, nil, nil, ReservationDeps{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	h = h.WithChannelLookup(lookup).WithRequireToken(requireToken)
	return h
}

func gatewaySettingsBlob(t *testing.T, enabled bool, tokenHash string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"gateway": map[string]any{"enabled": enabled, "token_hash": tokenHash},
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	return b
}

func TestAuthenticateCommand_Success_DisplayNumber(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	ch := gen.SalesChannelRow{
		ID: uuid.New(), OrgID: uuid.New(), DisplayNumber: 1271,
		Settings: gatewaySettingsBlob(t, true, string(hash)),
	}
	h := buildAuthHandler(t, ch, true)
	got, ok := h.authenticateCommand(context.Background(), httptest.NewRecorder(),
		bil24Request{Command: "GET_ALL_ACTIONS", FID: "1271", Token: "secret"})
	if !ok {
		t.Fatalf("authenticateCommand: expected success")
	}
	if got.ID != ch.ID {
		t.Errorf("authenticateCommand: wrong channel; got %s want %s", got.ID, ch.ID)
	}
}

func TestAuthenticateCommand_UnknownFID_Returns4(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	ch := gen.SalesChannelRow{
		ID: uuid.New(), OrgID: uuid.New(), DisplayNumber: 1271,
		Settings: gatewaySettingsBlob(t, true, string(hash)),
	}
	h := buildAuthHandler(t, ch, true)
	w := httptest.NewRecorder()
	_, ok := h.authenticateCommand(context.Background(), w,
		bil24Request{Command: "GET_ALL_ACTIONS", FID: "9999", Token: "secret"})
	if ok {
		t.Fatalf("expected auth to fail for unknown fid")
	}
	if code := mustResultCodeFromBytes(t, w.Body.Bytes()); code != ResultCodeUnauthorized {
		t.Errorf("expected resultCode=%d (Unauthorized), got %d", ResultCodeUnauthorized, code)
	}
}

func TestAuthenticateCommand_DisabledChannel_Returns4(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	ch := gen.SalesChannelRow{
		ID: uuid.New(), OrgID: uuid.New(), DisplayNumber: 42,
		Settings: gatewaySettingsBlob(t, false, string(hash)), // enabled=false
	}
	h := buildAuthHandler(t, ch, true)
	w := httptest.NewRecorder()
	_, ok := h.authenticateCommand(context.Background(), w,
		bil24Request{Command: "GET_ALL_ACTIONS", FID: "42", Token: "secret"})
	if ok {
		t.Fatalf("expected disabled channel to be rejected")
	}
	if code := mustResultCodeFromBytes(t, w.Body.Bytes()); code != ResultCodeUnauthorized {
		t.Errorf("expected -4 for disabled channel; got %d", code)
	}
}

func TestAuthenticateCommand_NoHash_Returns4(t *testing.T) {
	ch := gen.SalesChannelRow{
		ID: uuid.New(), OrgID: uuid.New(), DisplayNumber: 42,
		Settings: json.RawMessage(`{"gateway":{"enabled":true}}`),
	}
	h := buildAuthHandler(t, ch, true)
	w := httptest.NewRecorder()
	_, ok := h.authenticateCommand(context.Background(), w,
		bil24Request{Command: "GET_ALL_ACTIONS", FID: "42", Token: "secret"})
	if ok {
		t.Fatalf("expected no-hash channel to be rejected")
	}
	if code := mustResultCodeFromBytes(t, w.Body.Bytes()); code != ResultCodeUnauthorized {
		t.Errorf("expected -4 for no-hash channel; got %d", code)
	}
}

func TestAuthenticateCommand_WrongToken_Returns4(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	ch := gen.SalesChannelRow{
		ID: uuid.New(), OrgID: uuid.New(), DisplayNumber: 42,
		Settings: gatewaySettingsBlob(t, true, string(hash)),
	}
	h := buildAuthHandler(t, ch, true)
	w := httptest.NewRecorder()
	_, ok := h.authenticateCommand(context.Background(), w,
		bil24Request{Command: "GET_ALL_ACTIONS", FID: "42", Token: "wrong"})
	if ok {
		t.Fatalf("expected wrong-token to be rejected")
	}
	if code := mustResultCodeFromBytes(t, w.Body.Bytes()); code != ResultCodeUnauthorized {
		t.Errorf("expected -4 for wrong token; got %d", code)
	}
}

func TestAuthenticateCommand_LegacyFallback_Accepts(t *testing.T) {
	// Backward compat: pre-W1 admin plumbing wrote gateway_token_hash at
	// the top level of `settings`. Feature #471 continues to accept that
	// shape for one more wave.
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	settings, _ := json.Marshal(map[string]string{"gateway_token_hash": string(hash)})
	ch := gen.SalesChannelRow{
		ID: uuid.New(), OrgID: uuid.New(), DisplayNumber: 55,
		Settings: settings,
	}
	h := buildAuthHandler(t, ch, true)
	w := httptest.NewRecorder()
	got, ok := h.authenticateCommand(context.Background(), w,
		bil24Request{Command: "GET_ALL_ACTIONS", FID: "55", Token: "secret"})
	if !ok {
		t.Fatalf("expected legacy fallback to accept the request; body=%s", w.Body.Bytes())
	}
	if got.ID != ch.ID {
		t.Errorf("got wrong channel back: %s vs %s", got.ID, ch.ID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

func mustResultCodeFromBytes(t *testing.T, body []byte) int {
	t.Helper()
	var env struct {
		ResultCode int `json:"resultCode"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, body)
	}
	return env.ResultCode
}

// silence io imports if unused.
var _ = io.Discard
