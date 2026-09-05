// gateway_credential_test.go — pure-unit tests for the helpers behind the
// feature #473 admin endpoint. These do NOT touch the router or the
// database — the route-level tests live at package httpserver
// (gateway_credential_473_test.go).
package hcatalog

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// GenerateGatewayToken must produce a 64-hex-character token whose bcrypt
// hash verifies, and two invocations must produce distinct secrets
// (crypto/rand is really the source, not a fixture).
func TestGenerateGatewayToken_ShapeAndUniqueness(t *testing.T) {
	t.Parallel()
	tok1, hash1, err := GenerateGatewayToken()
	if err != nil {
		t.Fatalf("GenerateGatewayToken: %v", err)
	}
	tok2, hash2, err := GenerateGatewayToken()
	if err != nil {
		t.Fatalf("GenerateGatewayToken (second call): %v", err)
	}

	// 32 raw bytes → 64 hex chars.
	if got := len(tok1); got != gatewayTokenBytes*2 {
		t.Errorf("token length: got %d, want %d", got, gatewayTokenBytes*2)
	}
	// Token must be hex-decodable (asserts the encoding contract the
	// WordPress plugins rely on — they treat the token as an opaque ASCII
	// string, but a hex encoding guarantees no shell-hostile chars).
	if _, err := hex.DecodeString(tok1); err != nil {
		t.Errorf("token not valid hex: %v", err)
	}

	if tok1 == tok2 {
		t.Error("two consecutive calls returned the same token — RNG is broken")
	}
	if hash1 == hash2 {
		t.Error("two consecutive calls returned the same bcrypt hash — RNG is broken")
	}

	// Round-trip: the hash must verify the plaintext.
	if err := bcrypt.CompareHashAndPassword([]byte(hash1), []byte(tok1)); err != nil {
		t.Errorf("bcrypt.CompareHashAndPassword: hash does not verify token: %v", err)
	}
	// Cross-check: hash1 must NOT verify tok2.
	if err := bcrypt.CompareHashAndPassword([]byte(hash1), []byte(tok2)); err == nil {
		t.Error("hash1 unexpectedly verifies tok2 — cross-contamination in RNG")
	}
}

// mergeGatewayIntoSettings must preserve unrelated keys and drop the legacy
// top-level `gateway_token_hash` field (which the pre-W1 write path used and
// which we migrate away from on first PUT — see auth.go doc comment).
func TestMergeGatewayIntoSettings_PreservesSiblingsAndDropsLegacy(t *testing.T) {
	t.Parallel()
	existing := json.RawMessage(`{
		"gateway_token_hash": "legacy-hash",
		"macs": {"api_key":"mk_123"},
		"feature_token": "ft_abc"
	}`)
	gw := gatewaySettingsPersisted{
		Enabled:        true,
		TokenHash:      "$2a$10$NEWHASH",
		TokenRotatedAt: "2026-09-05T10:00:00Z",
		DefaultLocale:  "cs",
	}
	got, err := mergeGatewayIntoSettings(existing, gw)
	if err != nil {
		t.Fatalf("mergeGatewayIntoSettings: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, string(got))
	}

	if _, present := out["gateway_token_hash"]; present {
		t.Error("legacy `gateway_token_hash` key should be dropped on rotation")
	}
	// Sibling settings must survive verbatim.
	macs, ok := out["macs"].(map[string]any)
	if !ok {
		t.Fatalf("macs sibling missing or wrong shape: %v", out["macs"])
	}
	if macs["api_key"] != "mk_123" {
		t.Errorf("macs.api_key clobbered: got %v", macs["api_key"])
	}
	if out["feature_token"] != "ft_abc" {
		t.Errorf("feature_token clobbered: got %v", out["feature_token"])
	}

	// New gateway sub-object must carry every persisted field.
	gwOut, ok := out["gateway"].(map[string]any)
	if !ok {
		t.Fatalf("gateway sub-object missing or wrong shape: %v", out["gateway"])
	}
	for k, want := range map[string]any{
		"enabled":          true,
		"token_hash":       "$2a$10$NEWHASH",
		"token_rotated_at": "2026-09-05T10:00:00Z",
		"default_locale":   "cs",
	} {
		if gwOut[k] != want {
			t.Errorf("gateway.%s: got %v, want %v", k, gwOut[k], want)
		}
	}
}

// mergeGatewayIntoSettings must not crash on a nil/empty existing blob; the
// admin endpoint uses that path when provisioning a channel for the first
// time (settings JSONB defaulted to '{}' in the migration).
func TestMergeGatewayIntoSettings_EmptyExistingIsSafe(t *testing.T) {
	t.Parallel()
	for name, existing := range map[string]json.RawMessage{
		"nil":    nil,
		"empty":  json.RawMessage(""),
		"object": json.RawMessage("{}"),
	} {
		name, existing := name, existing
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := mergeGatewayIntoSettings(existing, gatewaySettingsPersisted{Enabled: true})
			if err != nil {
				t.Fatalf("mergeGatewayIntoSettings(%s): %v", name, err)
			}
			if !strings.Contains(string(got), `"gateway":`) {
				t.Errorf("%s: output missing gateway key: %s", name, string(got))
			}
		})
	}
}

// extractGatewaySettings returns the zero value (enabled=false, empty
// timestamps) whenever the settings JSONB is absent, malformed, or does not
// carry a `gateway` sub-object. That is the read-path guarantee the GET
// endpoint relies on to answer with `enabled=false` on a never-provisioned
// channel.
func TestExtractGatewaySettings_ZeroValueOnMissingOrMalformed(t *testing.T) {
	t.Parallel()
	cases := map[string]json.RawMessage{
		"nil":                 nil,
		"empty":               json.RawMessage(""),
		"garbage":             json.RawMessage(`not json`),
		"object_without_gw":   json.RawMessage(`{"macs":{"api_key":"x"}}`),
		"null_gateway":        json.RawMessage(`{"gateway":null}`),
		"top_level_only_hash": json.RawMessage(`{"gateway_token_hash":"legacy"}`),
	}
	for name, raw := range cases {
		name, raw := name, raw
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := extractGatewaySettings(raw)
			if got.Enabled {
				t.Errorf("%s: expected enabled=false, got true", name)
			}
			if got.TokenHash != "" {
				t.Errorf("%s: expected empty token_hash, got %q", name, got.TokenHash)
			}
			if got.TokenRotatedAt != "" {
				t.Errorf("%s: expected empty rotated_at, got %q", name, got.TokenRotatedAt)
			}
		})
	}
}

// extractGatewaySettings must decode the full persisted shape when present.
func TestExtractGatewaySettings_RoundTripsAllFields(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{
		"gateway": {
			"enabled": true,
			"token_hash": "$2a$10$abc",
			"token_rotated_at": "2026-09-05T12:00:00Z",
			"default_locale": "he"
		}
	}`)
	got := extractGatewaySettings(raw)
	if !got.Enabled {
		t.Error("enabled: expected true")
	}
	if got.TokenHash != "$2a$10$abc" {
		t.Errorf("token_hash: got %q", got.TokenHash)
	}
	if got.TokenRotatedAt != "2026-09-05T12:00:00Z" {
		t.Errorf("rotated_at: got %q", got.TokenRotatedAt)
	}
	if got.DefaultLocale != "he" {
		t.Errorf("default_locale: got %q", got.DefaultLocale)
	}
}
