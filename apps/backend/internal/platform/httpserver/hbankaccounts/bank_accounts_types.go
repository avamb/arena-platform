// bank_accounts_types.go holds the pure-data + helper layer for the
// organization bank-accounts CRUD surface (feature #255): the response
// struct, the row-to-response mapper, the strict body decoder, and the
// field validators shared by POST and PATCH.
package hbankaccounts

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response shape
// ─────────────────────────────────────────────────────────────────────────────

// BankAccountResponse is the JSON representation of an
// organization_bank_accounts row in HTTP responses (BankAccountItem in
// openapi.yaml).
//
// Sensitive numbers (iban / account_number) are returned VERBATIM: per the
// spec, every bank-accounts endpoint already requires the `org.update`
// permission on the owning organization, and actors holding that permission
// are entitled to the raw values. No payment_provider_configs secrets ever
// pass through this table.
type BankAccountResponse struct {
	ID            string  `json:"id"`
	OrgID         string  `json:"org_id"`
	HolderName    string  `json:"holder_name"`
	Currency      string  `json:"currency"`
	Country       string  `json:"country"`
	BankName      *string `json:"bank_name"`
	Iban          *string `json:"iban"`
	Bic           *string `json:"bic"`
	AccountNumber *string `json:"account_number"`
	RoutingNumber *string `json:"routing_number"`
	IsPrimary     bool    `json:"is_primary"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// BankAccountFromRow renders a row into the response shape.
func BankAccountFromRow(b gen.OrganizationBankAccountRow) BankAccountResponse {
	return BankAccountResponse{
		ID:            b.ID.String(),
		OrgID:         b.OrgID.String(),
		HolderName:    b.HolderName,
		Currency:      b.Currency,
		Country:       b.Country,
		BankName:      b.BankName,
		Iban:          b.Iban,
		Bic:           b.Bic,
		AccountNumber: b.AccountNumber,
		RoutingNumber: b.RoutingNumber,
		IsPrimary:     b.IsPrimary,
		CreatedAt:     b.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     b.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Strict body decoding (additionalProperties: false)
// ─────────────────────────────────────────────────────────────────────────────

// bankAccountFields lists every property accepted by
// CreateBankAccountRequest / UpdateBankAccountRequest. Both schemas declare
// additionalProperties: false, so any other key is rejected with 400.
var bankAccountFields = map[string]bool{
	"holder_name":    true,
	"currency":       true,
	"country":        true,
	"bank_name":      true,
	"iban":           true,
	"bic":            true,
	"account_number": true,
	"routing_number": true,
	"is_primary":     true,
}

// decodeBankAccountBody parses body into a key → raw-value map after
// verifying it is a JSON object containing only known keys. The map keeps
// key PRESENCE observable, which the PATCH handler needs to distinguish
// "field omitted" (keep) from "field null / empty" (clear). On failure the
// (code, message) pair describes the 400 error envelope to write.
func decodeBankAccountBody(body []byte) (fields map[string]json.RawMessage, code, message string) {
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, "bank_account.invalid_json", "request body is not a valid JSON object"
	}
	for key := range fields {
		if !bankAccountFields[key] {
			return nil, "bank_account.unknown_field", "unknown field " + quoteKey(key)
		}
	}
	return fields, "", ""
}

// quoteKey quotes a key for error messages without importing fmt.
func quoteKey(s string) string {
	return `"` + s + `"`
}

// stringField extracts fields[key] as a trimmed string. Returns
// (value, present, ok): present is false when the key is absent; value is
// nil when the key is present but null or empty (the "clear" signal for
// PATCH); ok is false when the value is not a string or null.
func stringField(fields map[string]json.RawMessage, key string) (value *string, present, ok bool) {
	raw, exists := fields[key]
	if !exists {
		return nil, false, true
	}
	if string(raw) == "null" {
		return nil, true, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, true, false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, true, true
	}
	return &s, true, true
}

// boolField extracts fields[key] as a bool. Same present/ok contract as
// stringField; null is rejected (is_primary is typed plain boolean).
func boolField(fields map[string]json.RawMessage, key string) (value bool, present, ok bool) {
	raw, exists := fields[key]
	if !exists {
		return false, false, true
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, true, false
	}
	return b, true, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Field validators
// ─────────────────────────────────────────────────────────────────────────────

// isValidCurrency reports whether s matches the schema pattern ^[A-Z]{3}$
// (ISO 4217 alpha-3).
func isValidCurrency(s string) bool {
	return isUpperAlpha(s, 3)
}

// isValidCountry reports whether s matches the schema pattern ^[A-Z]{2}$
// (ISO 3166-1 alpha-2).
func isValidCountry(s string) bool {
	return isUpperAlpha(s, 2)
}

// isUpperAlpha reports whether s is exactly n uppercase ASCII letters.
func isUpperAlpha(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < n; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}

// hasBankIdentifier enforces the CreateBankAccountRequest invariant: at
// least one of `iban` or (`account_number` + `routing_number`) must be
// populated. The same rule is re-checked post-merge on PATCH so a clear
// can never strip the last identifier off a row.
func hasBankIdentifier(iban, accountNumber, routingNumber *string) bool {
	if iban != nil {
		return true
	}
	return accountNumber != nil && routingNumber != nil
}

// normalizeBankIdentifier canonicalises an identifier for duplicate
// comparison: spaces removed, uppercased. IBANs are commonly written with
// grouping spaces and lowercase letters; those variants must collide.
func normalizeBankIdentifier(s string) string {
	return strings.ToUpper(strings.ReplaceAll(s, " ", ""))
}

// duplicateIdentifier reports whether the candidate identifiers collide
// with an existing active row: same IBAN, or same
// account_number+routing_number pair.
func duplicateIdentifier(existing gen.OrganizationBankAccountRow, iban, accountNumber, routingNumber *string) bool {
	if iban != nil && existing.Iban != nil &&
		normalizeBankIdentifier(*iban) == normalizeBankIdentifier(*existing.Iban) {
		return true
	}
	if accountNumber != nil && routingNumber != nil &&
		existing.AccountNumber != nil && existing.RoutingNumber != nil &&
		normalizeBankIdentifier(*accountNumber) == normalizeBankIdentifier(*existing.AccountNumber) &&
		normalizeBankIdentifier(*routingNumber) == normalizeBankIdentifier(*existing.RoutingNumber) {
		return true
	}
	return false
}
