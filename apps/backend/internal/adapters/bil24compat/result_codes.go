// result_codes.go — Bil24 wire-format result codes.
//
// These integer codes are part of the frozen Bil24 wire contract and MUST
// NOT change. Legacy clients (the Vino&Co WordPress plugin and partner
// widgets) inspect the resultCode field of the response envelope to decide
// whether a command succeeded; HTTP status is always 200 for Bil24
// protocol responses.

package bil24compat

// Bil24 wire result codes. ResultCodeOK (0) indicates success. All other
// values indicate failure with the specific description carried in the
// response envelope's "description" field.
const (
	// ResultCodeOK signals a successful command execution (Bil24 wire: 0).
	ResultCodeOK = 0
	// ResultCodeUnknownCommand is returned when the gateway receives a
	// command name it does not recognise (Bil24 wire: -1).
	ResultCodeUnknownCommand = -1
	// ResultCodeInvalidRequest is returned when a required request field is
	// missing or malformed (Bil24 wire: -2).
	ResultCodeInvalidRequest = -2
	// ResultCodeNotFound is returned when the requested resource does not
	// exist in the platform (Bil24 wire: -3).
	ResultCodeNotFound = -3
	// ResultCodeInternalError is returned when an unexpected error prevents
	// command execution (Bil24 wire: -99).
	ResultCodeInternalError = -99
)
