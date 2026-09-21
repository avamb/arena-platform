// secret_formats.go — refuse a credential that cannot possibly be one.
//
// `status` answers "is every required field non-empty", which is a shape
// check on the FORM, not on its contents: any string at all passes. On
// 2026-09-21 a live organization's Stripe config held a twenty-character
// string that was not a key of any kind. Every admin surface showed the
// config as configured; the only party ever told otherwise was a buyer, who
// got a 503 on the pay button. The operator had no way to find out short of
// reading container logs.
//
// So the obvious mistakes are caught where they are made. This is a cheap
// STATIC check and does not replace asking the provider (see
// payments.CredentialVerifier and the verify endpoint) — it exists because
// an error at the moment of pasting is worth far more than a correct verdict
// an hour later, and because it costs no network call.
//
// Deliberately narrow: only patterns the provider itself documents as fixed
// are enforced, and only for providers whose prefixes we actually know. A
// provider absent from the table accepts anything, because guessing at a
// format we have not verified would reject working credentials — a far worse
// failure than the one this file prevents.
package hpayments

import (
	"fmt"
	"sort"
	"strings"
)

// secretFormat describes what a given provider's secret field must look like.
type secretFormat struct {
	// prefixes the value may start with. Empty means "no prefix rule".
	prefixes []string
	// modePrefixes maps a config mode to the prefixes legal in THAT mode, so
	// a live key cannot be filed under test. A mode absent from the map is
	// checked against prefixes alone.
	modePrefixes map[string][]string
	// human is how the field is described in the error, in the provider's
	// own vocabulary, so an operator knows what to go and fetch.
	human string
}

// secretFormats is keyed by provider, then by secret field name.
//
// Stripe is the only entry because it is the only provider whose key shapes
// are both documented as stable and in production here. Restricted keys
// (rk_) are accepted: they are valid API credentials and an organization is
// entitled to scope one down.
var secretFormats = map[string]map[string]secretFormat{
	"stripe": {
		"api_key": {
			prefixes: []string{"sk_test_", "sk_live_", "rk_test_", "rk_live_"},
			modePrefixes: map[string][]string{
				"test": {"sk_test_", "rk_test_"},
				"live": {"sk_live_", "rk_live_"},
			},
			human: "Stripe secret key (Developers → API keys → Secret key)",
		},
		"webhook_secret": {
			prefixes: []string{"whsec_"},
			human:    "Stripe webhook signing secret (Developers → Webhooks → your endpoint → Signing secret)",
		},
	},
}

// NormalizeSecretValue trims surrounding whitespace from a pasted credential.
//
// No provider credential contains leading or trailing whitespace, and a
// value copied out of a web page or an e-mail routinely carries a trailing
// newline or a stray space. Storing it verbatim turns an invisible character
// into a 401 nobody can explain, so the whitespace is dropped rather than
// preserved. Interior characters are never touched.
func NormalizeSecretValue(v string) string {
	return strings.TrimSpace(v)
}

// ValidateSecretFormats checks a patch of secret values against what the
// provider's credentials are known to look like, in the given mode.
//
// It returns one error describing the FIRST offending field, phrased for the
// operator who is about to save. Only fields actually present in the patch
// are checked: a partial update that does not touch the api_key must not be
// refused because of it. An empty value means "delete this field" and is
// left to MergeSecrets.
func ValidateSecretFormats(provider, mode string, patch map[string]string) error {
	formats, known := secretFormats[provider]
	if !known {
		return nil
	}
	for _, field := range sortedKeys(patch) {
		value := patch[field]
		if value == "" {
			continue
		}
		format, checked := formats[field]
		if !checked {
			continue
		}
		allowed := format.prefixes
		if modeAllowed, ok := format.modePrefixes[mode]; ok {
			allowed = modeAllowed
		}
		if len(allowed) == 0 || hasAnyPrefix(value, allowed) {
			continue
		}
		// Never echo the value back: it is a credential, and an error
		// message is the one place a secret reliably ends up in a log.
		if _, legalSomewhere := format.modePrefixes[mode]; legalSomewhere && hasAnyPrefix(value, format.prefixes) {
			return fmt.Errorf(
				"%s: this is a %s key but the configuration is in %s mode — "+
					"use the %s key, or change the configuration's mode",
				field, otherMode(mode), mode, mode,
			)
		}
		return fmt.Errorf(
			"%s does not look like a %s: it must start with %s",
			field, format.human, strings.Join(allowed, " or "),
		)
	}
	return nil
}

func hasAnyPrefix(v string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}

func otherMode(mode string) string {
	if mode == "test" {
		return "live"
	}
	return "test"
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
