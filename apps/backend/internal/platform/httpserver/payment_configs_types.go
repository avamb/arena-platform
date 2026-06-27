// payment_configs_types.go holds the pure-data + helper layer for the
// payment-provider-config CRUD surface (feature #237). Keeping these
// out of payment_configs.go keeps the handler file under the
// internal/platform/httpserver/ size budget (feature #175).
//
// Contents:
//   * Provider catalogue and required-secret rules.
//   * Response struct and the row-to-response mapper.
//   * Pure helpers for status derivation, secret extraction, and the
//     secret-patch merge logic used by PATCH.
package httpserver

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Provider catalogue + required-secret rules
// ─────────────────────────────────────────────────────────────────────────────

// supportedPaymentProviders lists the provider slugs the platform knows
// how to talk to. Anything else is rejected at create-time so we never
// store credentials for a provider we cannot wire to.
var supportedPaymentProviders = map[string]bool{
	"stripe":        true,
	"allpay":        true,
	"cloudpayments": true,
	"yookassa":      true,
	"manual":        true,
}

// requiredSecretFields lists the secret jsonb keys that MUST be
// populated for a given provider before the config can be marked
// "configured". The lists are intentionally minimal — adapters can
// still surface their own runtime errors when extra optional secrets
// are missing.
var requiredSecretFields = map[string][]string{
	"stripe":        {"api_key", "webhook_secret"},
	"allpay":        {"merchant_id", "secret_key"},
	"cloudpayments": {"public_id", "api_secret"},
	"yookassa":      {"shop_id", "secret_key"},
	"manual":        {}, // manual provider has no credentials
}

// supportedModes is the set of legal `mode` values.
var supportedModes = map[string]bool{
	"test": true,
	"live": true,
}

// supportedProviderList returns a sorted slice of supported provider
// slugs for inclusion in error details.
func supportedProviderList() []string {
	out := make([]string, 0, len(supportedPaymentProviders))
	for p := range supportedPaymentProviders {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Response shape
// ─────────────────────────────────────────────────────────────────────────────

// paymentConfigResponse is the JSON representation of a
// payment_provider_config row in HTTP responses.
//
// `secrets` is intentionally absent. `secret_fields_set` lists the
// secret keys currently stored (no values), so the UI can show
// "[STORED]" markers. `missing_required_fields` lists the secret keys
// still expected for the chosen provider; populated only when status =
// "missing_required_fields".
type paymentConfigResponse struct {
	ID                    string          `json:"id"`
	OrgID                 string          `json:"org_id"`
	Provider              string          `json:"provider"`
	Mode                  string          `json:"mode"`
	ProviderAccountID     *string         `json:"provider_account_id"`
	PublicConfig          json.RawMessage `json:"public_config"`
	SecretFieldsSet       []string        `json:"secret_fields_set"`
	Status                string          `json:"status"`
	MissingRequiredFields []string        `json:"missing_required_fields"`
	IsActive              bool            `json:"is_active"`
	CreatedAt             string          `json:"created_at"`
	UpdatedAt             string          `json:"updated_at"`
}

// paymentConfigFromRow renders a row into the response shape, stripping
// secret values and deriving the missing-field list.
func paymentConfigFromRow(p gen.PaymentProviderConfigRow) paymentConfigResponse {
	resp := paymentConfigResponse{
		ID:                    p.ID.String(),
		OrgID:                 p.OrgID.String(),
		Provider:              p.Provider,
		Mode:                  p.Mode,
		ProviderAccountID:     p.ProviderAccountID,
		PublicConfig:          publicConfigForResponse(p.PublicConfig),
		SecretFieldsSet:       extractStoredSecretKeys(p.Secrets),
		Status:                p.Status,
		MissingRequiredFields: computeMissingRequiredFields(p.Provider, p.Secrets),
		IsActive:              p.IsActive,
		CreatedAt:             p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:             p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	// MissingRequiredFields should be an empty slice (never nil) so JSON
	// callers always see a deterministic [] shape.
	if resp.MissingRequiredFields == nil {
		resp.MissingRequiredFields = []string{}
	}
	if resp.SecretFieldsSet == nil {
		resp.SecretFieldsSet = []string{}
	}
	return resp
}

// publicConfigForResponse defaults nil/empty jsonb to "{}".
func publicConfigForResponse(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

// extractStoredSecretKeys returns the keys present in the secrets jsonb
// whose corresponding string value is non-empty. The function never
// returns the values themselves.
func extractStoredSecretKeys(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// computeMissingRequiredFields returns the required-secret keys for the
// given provider that are either missing from the secrets jsonb or are
// present with an empty/whitespace value.
func computeMissingRequiredFields(provider string, secrets json.RawMessage) []string {
	required, ok := requiredSecretFields[provider]
	if !ok || len(required) == 0 {
		return nil
	}
	var stored map[string]any
	if len(secrets) > 0 {
		_ = json.Unmarshal(secrets, &stored)
	}
	missing := make([]string, 0, len(required))
	for _, key := range required {
		v, present := stored[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

// deriveStatus computes the status column value for the given provider
// and the secrets that will be stored on the row.
func deriveStatus(provider string, secrets json.RawMessage) string {
	if len(computeMissingRequiredFields(provider, secrets)) == 0 {
		return "configured"
	}
	return "missing_required_fields"
}

// mergeSecrets layers the patch map on top of the existing secrets
// jsonb. Empty-string values in the patch DELETE the corresponding
// key (so admins can clear a secret without removing the whole row).
// Returns the merged jsonb plus a flag indicating whether the patch
// changed anything.
func mergeSecrets(existing json.RawMessage, patch map[string]string) (json.RawMessage, bool, error) {
	current := map[string]any{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &current); err != nil {
			return nil, false, fmt.Errorf("existing secrets jsonb is corrupt: %w", err)
		}
	}
	changed := false
	for k, v := range patch {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if v == "" {
			if _, present := current[k]; present {
				delete(current, k)
				changed = true
			}
			continue
		}
		if prev, present := current[k]; !present || prev != v {
			current[k] = v
			changed = true
		}
	}
	out, err := json.Marshal(current)
	if err != nil {
		return nil, false, fmt.Errorf("marshal merged secrets: %w", err)
	}
	return out, changed, nil
}
