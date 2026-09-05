// error_mapping.go — kind → (resultCode, English description) mapping for
// Bil24 wire responses.
//
// Feature #477, spec section 6. The mapping is a policy surface so
// individual command handlers do not hard-code result codes and English
// prose in-line. Localisation of the description arrives in feature #478
// (i18n bundle keys `bil24.*`); until then, the helpers return English
// strings so the WP plugin has a stable byte surface to log.
//
// The four buckets mirror the spec's error taxonomy:
//
//	MapDBError         → ResultCodeTransient   (-1)  DB/pool/timeouts (retry-able)
//	MapValidationError → ResultCodeInvalidRequest (-2)  malformed payload / unknown command
//	MapScopeError      → ResultCodeNotFound    (-3)  ID resolves outside channel scope
//	MapBusinessError   → ResultCodeUserVisible (101)  shown to the buyer verbatim
//
// Callers should prefer these helpers over the raw constants so future
// wording tweaks (spec-driven) and description-key wiring (#478) apply
// project-wide from a single spot.

package bil24compat

import "errors"

// English fallback descriptions for the four spec error buckets. These
// strings are stable Bil24-wire byte surface — do not change them without
// updating the WP plugin log expectations. Once feature #478 wires the
// i18n bundle these become the `en` translations for the `bil24.*` keys.
const (
	// DescTransient is the default English description for -1 (transient).
	DescTransient = "temporary service error, please retry"
	// DescInvalidRequest is the default English description for -2.
	DescInvalidRequest = "invalid request"
	// DescNotFound is the default English description for -3 (scope miss).
	DescNotFound = "resource not found in this channel"
	// DescUserVisible is the default English description for 101 when the
	// caller does not supply a more specific business-error message.
	DescUserVisible = "operation refused"
)

// MapDBError converts a database / connection-pool / worker-timeout error
// into a (ResultCodeTransient, description) pair. Pass a wrapped error
// with a human-readable prefix; nil is treated as "unknown transient".
func MapDBError(err error) (int, string) {
	if err == nil {
		return ResultCodeTransient, DescTransient
	}
	return ResultCodeTransient, DescTransient
}

// MapValidationError converts a request-validation failure (missing
// field, malformed value, unknown command name) into a
// (ResultCodeInvalidRequest, description) pair. The description surfaces
// verbatim to the WP plugin log so callers should pass a specific message
// (e.g. "orderId is required"); empty falls back to DescInvalidRequest.
func MapValidationError(desc string) (int, string) {
	if desc == "" {
		return ResultCodeInvalidRequest, DescInvalidRequest
	}
	return ResultCodeInvalidRequest, desc
}

// MapScopeError converts a scope-miss (ID not owned by this channel /
// resource genuinely absent) into a (ResultCodeNotFound, description)
// pair. Empty description falls back to DescNotFound.
func MapScopeError(desc string) (int, string) {
	if desc == "" {
		return ResultCodeNotFound, DescNotFound
	}
	return ResultCodeNotFound, desc
}

// MapBusinessError converts a user-visible business failure (seat taken,
// promo invalid, hold expired, …) into a (ResultCodeUserVisible,
// description) pair. The description key argument is the future i18n
// bundle key (`bil24.seat_taken`, `bil24.promo_invalid`, …) — feature
// #478 will translate it via platform/i18n; in the meantime the English
// fallback wins. Empty descKey and empty english both fall back to
// DescUserVisible.
func MapBusinessError(descKey, english string) (int, string) {
	if english != "" {
		return ResultCodeUserVisible, english
	}
	if descKey != "" {
		// Until #478 wires the i18n bundle, surface the key so log
		// scrapes can pin the specific failure class.
		return ResultCodeUserVisible, descKey
	}
	return ResultCodeUserVisible, DescUserVisible
}

// ErrSessionExpired is the sentinel returned by session-lookup helpers
// when the gateway session is absent or past its expires_at. Handlers
// convert it to ResultCodeSessionExpired via MapSessionError.
var ErrSessionExpired = errors.New("bil24 gateway session expired")

// MapSessionError converts an expired-session lookup into
// (ResultCodeSessionExpired, description). The WP plugin re-creates the
// user session on receiving this code (class-bil24-seat-picker.php:757);
// the description is informational only.
func MapSessionError() (int, string) {
	return ResultCodeSessionExpired, "session expired"
}
