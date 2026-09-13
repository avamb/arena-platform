// cmd_cart_deadlock_502_test.go — writeCartHoldError's default-branch
// resultCode mapping for infrastructure failures (HIGH-severity fix: a
// Postgres deadlock/serialization failure surviving hcheckout's bounded
// retry used to answer -99 ResultCodeInternalError, which the WordPress
// plugin treats as non-retryable; it must answer -1 ResultCodeTransient so
// the plugin retries instead of surfacing a hard error to the buyer).
//
// See cmd_cart_view.go's writeCartHoldError default branch and AGENTS.md's
// hold-mutation lock order / retry gotcha.
package hbil24

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func decodeResultCode(t *testing.T, body []byte) float64 {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, body)
	}
	rc, ok := out["resultCode"]
	if !ok {
		t.Fatalf("response has no resultCode field: %s", body)
	}
	f, ok := rc.(float64)
	if !ok {
		t.Fatalf("resultCode is not a number: %T (%v)", rc, rc)
	}
	return f
}

// TestWriteCartHoldError_WrappedDeadlock_MapsToTransient proves a wrapped
// 40P01 deadlock error reaches the wire as resultCode -1 (ResultCodeTransient),
// not -99.
func TestWriteCartHoldError_WrappedDeadlock_MapsToTransient(t *testing.T) {
	h := &Handler{logger: discardLogger()}
	w := httptest.NewRecorder()
	req := bil24Request{Command: "RESERVATION"}
	cc := cartCtx{}

	err := fmt.Errorf("hcheckout: bump seat_status_version: %w", &pgconn.PgError{
		Code: "40P01", Message: "deadlock detected",
	})
	h.writeCartHoldError(w, req, cc, nil, cartPricing{}, err)

	got := decodeResultCode(t, w.Body.Bytes())
	if got != float64(ResultCodeTransient) {
		t.Fatalf("resultCode = %v, want %v (ResultCodeTransient)", got, ResultCodeTransient)
	}
}

// TestWriteCartHoldError_WrappedSerializationFailure_MapsToTransient is the
// 40001 counterpart of the deadlock test above — the class of error
// hcheckout's retry helper surfaces once its own attempts are exhausted.
func TestWriteCartHoldError_WrappedSerializationFailure_MapsToTransient(t *testing.T) {
	h := &Handler{logger: discardLogger()}
	w := httptest.NewRecorder()
	req := bil24Request{Command: "RESERVATION"}
	cc := cartCtx{}

	err := fmt.Errorf("hcheckout: reserve capacity: %w", &pgconn.PgError{
		Code: "40001", Message: "could not serialize access due to concurrent update",
	})
	if !hcheckout.IsSerializationFailure(err) {
		t.Fatal("sanity check: hcheckout must classify this as a serialization failure")
	}
	h.writeCartHoldError(w, req, cc, nil, cartPricing{}, err)

	got := decodeResultCode(t, w.Body.Bytes())
	if got != float64(ResultCodeTransient) {
		t.Fatalf("resultCode = %v, want %v (ResultCodeTransient)", got, ResultCodeTransient)
	}
}

// TestWriteCartHoldError_OtherPgError_MapsToTransient proves the mapping is
// not narrowly scoped to 40P01/40001: ANY *pgconn.PgError reaching the
// default branch (a dropped connection, a statement timeout, …) is treated
// as an infrastructure failure and answers -1, per the task's "prefer -1 for
// pgconn/pgx errors specifically" rule.
func TestWriteCartHoldError_OtherPgError_MapsToTransient(t *testing.T) {
	h := &Handler{logger: discardLogger()}
	w := httptest.NewRecorder()
	req := bil24Request{Command: "RESERVATION"}
	cc := cartCtx{}

	err := fmt.Errorf("hcheckout: reserve capacity: %w", &pgconn.PgError{
		Code: "57014", Message: "canceling statement due to statement timeout",
	})
	h.writeCartHoldError(w, req, cc, nil, cartPricing{}, err)

	got := decodeResultCode(t, w.Body.Bytes())
	if got != float64(ResultCodeTransient) {
		t.Fatalf("resultCode = %v, want %v (ResultCodeTransient)", got, ResultCodeTransient)
	}
}

// TestWriteCartHoldError_PlainUntypedError_StaysInternal proves the -99
// fallback is preserved for a genuinely unexpected, non-Postgres error —
// the "programming error" bucket the task asks to keep at -99.
func TestWriteCartHoldError_PlainUntypedError_StaysInternal(t *testing.T) {
	h := &Handler{logger: discardLogger()}
	w := httptest.NewRecorder()
	req := bil24Request{Command: "RESERVATION"}
	cc := cartCtx{}

	err := errors.New("hcheckout: something unexpected happened")
	h.writeCartHoldError(w, req, cc, nil, cartPricing{}, err)

	got := decodeResultCode(t, w.Body.Bytes())
	if got != float64(ResultCodeInternalError) {
		t.Fatalf("resultCode = %v, want %v (ResultCodeInternalError)", got, ResultCodeInternalError)
	}
}

// TestWriteCartHoldError_TypedHoldErrors_Unaffected is a regression guard:
// the pre-existing typed-error branches (capacity, seat conflicts, …) must
// keep answering their own codes, not fall into the new pgconn.PgError
// carve-out.
func TestWriteCartHoldError_TypedHoldErrors_Unaffected(t *testing.T) {
	h := &Handler{logger: discardLogger()}
	req := bil24Request{Command: "RESERVATION"}
	cc := cartCtx{}

	w := httptest.NewRecorder()
	h.writeCartHoldError(w, req, cc, nil, cartPricing{}, &hcheckout.CapacityError{Requested: 3})
	if got := decodeResultCode(t, w.Body.Bytes()); got != float64(ResultCodeUserVisible) {
		t.Fatalf("CapacityError: resultCode = %v, want %v (ResultCodeUserVisible)", got, ResultCodeUserVisible)
	}

	w2 := httptest.NewRecorder()
	h.writeCartHoldError(w2, req, cc, nil, cartPricing{}, hcheckout.ErrHoldInvalidInput)
	if got := decodeResultCode(t, w2.Body.Bytes()); got != float64(ResultCodeInvalidRequest) {
		t.Fatalf("ErrHoldInvalidInput: resultCode = %v, want %v (ResultCodeInvalidRequest)", got, ResultCodeInvalidRequest)
	}
}
