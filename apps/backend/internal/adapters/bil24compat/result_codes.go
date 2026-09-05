// result_codes.go — Bil24 wire-format result codes.
//
// These integer codes are part of the frozen Bil24 wire contract and MUST
// NOT change. Legacy clients (the Vino&Co WordPress plugin and partner
// widgets) inspect the resultCode field of the response envelope to decide
// whether a command succeeded; HTTP status is always 200 for Bil24
// protocol responses.
//
// Canonical mapping (see 08_architecture/18_bil24_compat_wave1_specification_ru.md
// section 6 — "Коды результата, описания, локализация"):
//
//	  0  OK
//	  1  gateway session not found / expired (fid/sessionId absent from
//	     gateway_sessions or past expires_at) → the WP plugin re-creates the
//	     user (class-bil24-seat-picker.php:757).
//	101  user-visible business error — description is shown to the buyer
//	     verbatim (seat taken, category sold out, sales closed, promo
//	     invalid, hold expired, event not in channel catalog).
//	 -1  transient failure — retry-able (DB/pool errors, deadlocks, worker
//	     timeouts). Feature #477 remapped -1 away from "unknown command".
//	 -2  invalid request (JSON parse failure, missing/malformed field,
//	     unknown command name — feature #477).
//	 -3  scope failure — the ID resolves outside this channel's catalog and
//	     the situation is not a user-facing business error.
//	 -4  fid/token authentication failure (platform extension, feature #374).
//	 -5  command recognised but not implemented (platform extension, e.g.
//	     ADD_PROMO_CODES, feature #374).
//	-99  panic-recovery / genuinely unexpected internal error.

package bil24compat

// Bil24 wire result codes. ResultCodeOK (0) indicates success. All other
// values indicate failure with the specific description carried in the
// response envelope's "description" field.
const (
	// ResultCodeOK signals a successful command execution (Bil24 wire: 0).
	ResultCodeOK = 0

	// ResultCodeSessionExpired signals that the caller-supplied
	// userId/sessionId is not present in gateway_sessions or has expired
	// (Bil24 wire: 1). Spec section 6. The legacy WP plugin recreates the
	// user session on receiving this code
	// (class-bil24-seat-picker.php:757, vino-checkout-rest.php:77).
	ResultCodeSessionExpired = 1

	// ResultCodeUserVisible signals a user-visible business failure
	// (Bil24 wire: 101). The value of the envelope's "description" field
	// is shown verbatim to the buyer, so it MUST be localised via the
	// request Locale (localisation lands in #478; #477 emits English).
	// Typical causes: seat already taken, category sold out, sales closed,
	// promo code invalid, hold expired, event not in channel catalog.
	ResultCodeUserVisible = 101

	// ResultCodeTransient signals a temporary failure that the caller
	// should retry: database/pool errors, deadlocks, statement timeouts,
	// upstream worker timeouts (Bil24 wire: -1). Feature #477 moved -1
	// off "unknown command" — unknown commands are now ResultCodeInvalidRequest
	// (-2), per spec section 6.
	ResultCodeTransient = -1

	// ResultCodeUnknownCommand is retained as a deprecated alias for
	// ResultCodeInvalidRequest (Bil24 wire: -2). Its value changed from
	// -1 to -2 in feature #477 to align with spec section 6.
	//
	// Deprecated: use ResultCodeInvalidRequest for new code. Symbol kept
	// so pre-existing references (tests/staticanalysis/
	// bil24compat_layout_188_test.go, cross-package callers) continue to
	// compile.
	ResultCodeUnknownCommand = ResultCodeInvalidRequest

	// ResultCodeInvalidRequest is returned when the request is malformed:
	// JSON parse failure, a required field is missing/invalid, or the
	// command name is not recognised (Bil24 wire: -2). Feature #477
	// widened this to absorb "unknown command".
	ResultCodeInvalidRequest = -2

	// ResultCodeNotFound is returned when the requested resource does not
	// exist in the platform's scope for this channel (Bil24 wire: -3).
	ResultCodeNotFound = -3

	// ResultCodeUnauthorized is returned when the fid/token credential pair
	// is invalid or missing. Platform extension — not in the legacy Bil24
	// wire spec but safe to add (non-zero means failure for all legacy
	// clients regardless of the specific code value). Feature #374.
	ResultCodeUnauthorized = -4

	// ResultCodeNotImplemented is returned for commands that are recognized
	// by the gateway but not yet wired to platform functionality (e.g.
	// CREATE_ORDER_EXT, CANCEL_ORDER). Platform extension. Feature #374.
	ResultCodeNotImplemented = -5

	// ResultCodeInternalError is returned when an unexpected error prevents
	// command execution (Bil24 wire: -99). Reserved for panic-recovery in
	// the top-level dispatcher; ordinary DB/pool failures should use
	// ResultCodeTransient (-1) per spec section 6.
	ResultCodeInternalError = -99
)
