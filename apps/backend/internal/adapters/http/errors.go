// errors.go — canonical HTTP error types and helpers for arena_new.
//
// Feature #90 — Error envelope & panic recovery:
//
//  1. ErrorEnvelope is the canonical JSON error response type used by every
//     arena_new HTTP endpoint.  It matches the OpenAPI ErrorEnvelope schema
//     defined in openapi/openapi.yaml (§components.schemas.ErrorEnvelope).
//
//  2. WriteError is a one-call helper that serialises an ErrorEnvelope and
//     writes it to the ResponseWriter with the correct status code and
//     Content-Type: application/json header.  Callers MUST NOT call
//     w.WriteHeader after WriteError.
//
//  3. DomainErrStatus is the extension-point function that maps well-known
//     domain error interface values to HTTP status codes.  Handlers call it
//     to translate business-layer errors into appropriate 4xx/5xx responses
//     without coupling the domain layer to net/http.  New categories are
//     added by implementing the relevant marker interface (NotFoundErr,
//     ConflictErr, ValidationErr, StatusCoder) — no change to callers needed.
//
// Panic recovery is owned by panicRecoverer in router.go.  This file owns
// the data types and helpers that all error responses share so the shapes
// stay consistent regardless of which middleware or handler produced the error.
package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ErrorEnvelope is the canonical JSON error response body shape used by every
// arena_new HTTP endpoint, as documented in §API Rules of the project spec and
// in the OpenAPI ErrorEnvelope schema component.
//
// JSON wire shape (with the outer "error" wrapper applied by WriteError):
//
//	{
//	  "error": {
//	    "code":       "http.not_found",
//	    "message":    "the requested resource does not exist",
//	    "request_id": "018fae1c-...",
//	    "trace_id":   "4bf92f3577b34da6...",
//	    "details":    {"field": "id"}   // omitted when nil
//	  }
//	}
type ErrorEnvelope struct {
	// Code is the dotted-namespace machine-readable error identifier, e.g.
	// "http.not_found", "auth.token_expired", "internal.unexpected".
	// Must match the pattern ^[a-z][a-z0-9_]+\.[a-z0-9_.]+$ as declared in
	// the OpenAPI schema (enforces namespace.subcode convention).
	Code string `json:"code"`

	// Message is a human-readable description of the error intended for
	// logging and developer tooling.  It MUST NOT include internal
	// implementation details such as stack traces, SQL queries, or raw
	// error strings from third-party libraries.
	Message string `json:"message"`

	// RequestID is the per-request UUID copied from the slog context
	// (set by requestContext middleware from the X-Request-Id header).
	// Clients should quote this value in support requests so operators can
	// find the corresponding server-side log records.
	RequestID string `json:"request_id"`

	// TraceID is the distributed trace identifier copied from the slog
	// context (set by tracerMiddleware from the OTel SpanContext /
	// X-Trace-Id header).  Correlates the HTTP error to the backend span.
	TraceID string `json:"trace_id"`

	// Details carries optional structured metadata about the error — for
	// example which request field failed validation, or which resource was
	// not found.  Omitted from the response body when nil (json:omitempty).
	Details map[string]any `json:"details,omitempty"`
}

// WriteError serialises code+message+details as the project-standard JSON
// error envelope and writes it to w with the given HTTP status code.
//
// The outer "error" wrapper is applied automatically:
//
//	{"error": {"code": code, "message": message, "request_id": "...", ...}}
//
// If r is non-nil, request_id and trace_id are resolved from the slog context
// attached to r (populated by requestContext + tracerMiddleware).
//
// Content-Type is always set to "application/json; charset=utf-8".
// Callers MUST NOT call w.WriteHeader after WriteError.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	env := ErrorEnvelope{
		Code:    code,
		Message: message,
		Details: details,
	}
	if r != nil {
		env.RequestID = logging.RequestID(r.Context())
		env.TraceID = logging.TraceID(r.Context())
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": env})
}

// -----------------------------------------------------------------------------
// Domain error → HTTP status mapping (extension points)
// -----------------------------------------------------------------------------

// StatusCoder is the interface that domain errors MAY implement to advertise
// the HTTP status code that best represents their semantics.
//
// The application/domain layer typically does NOT import net/http so it
// cannot reference http.Status* constants directly.  By implementing
// StatusCoder the domain error communicates its preferred HTTP mapping to
// the HTTP adapter layer without creating a cross-layer import dependency.
type StatusCoder interface {
	HTTPStatus() int
}

// NotFoundErr is the interface that domain errors MAY implement to indicate
// that the requested resource does not exist.  Errors satisfying this
// interface are mapped to HTTP 404 Not Found by DomainErrStatus.
type NotFoundErr interface {
	NotFound() bool
}

// ConflictErr is the interface that domain errors MAY implement to indicate
// that the operation conflicts with existing state (e.g. duplicate key,
// optimistic-lock failure, stale version).  Errors satisfying this interface
// are mapped to HTTP 409 Conflict by DomainErrStatus.
type ConflictErr interface {
	Conflict() bool
}

// ValidationErr is the interface that domain errors MAY implement to indicate
// that a client-supplied value failed domain validation (e.g. invalid enum
// member, quantity out of range, referential integrity failure in business
// rules).  Errors satisfying this interface are mapped to HTTP 422
// Unprocessable Entity by DomainErrStatus.
type ValidationErr interface {
	Validation() bool
}

// DomainErrStatus returns the HTTP status code that best represents err.
//
// Resolution order (first match wins):
//
//  1. nil error → 200 OK.
//  2. err (or any wrapped error via errors.As) implements StatusCoder →
//     StatusCoder.HTTPStatus().
//  3. err implements NotFoundErr and NotFound() == true → 404 Not Found.
//  4. err implements ConflictErr and Conflict() == true → 409 Conflict.
//  5. err implements ValidationErr and Validation() == true →
//     422 Unprocessable Entity.
//  6. Default → 500 Internal Server Error.
//
// This function is the designated extension point: new error categories
// (e.g. ForbiddenErr, RateLimitErr) can be added by declaring a new marker
// interface and extending the resolution order here without changing any
// existing caller code.
func DomainErrStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}

	var sc StatusCoder
	if errors.As(err, &sc) {
		return sc.HTTPStatus()
	}

	var nf NotFoundErr
	if errors.As(err, &nf) && nf.NotFound() {
		return http.StatusNotFound
	}

	var ce ConflictErr
	if errors.As(err, &ce) && ce.Conflict() {
		return http.StatusConflict
	}

	var ve ValidationErr
	if errors.As(err, &ve) && ve.Validation() {
		return http.StatusUnprocessableEntity
	}

	return http.StatusInternalServerError
}
