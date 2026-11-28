package hcatalog

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// The triggers of migration 0228 raise 23514 with a message naming
// control_only; only that refusal becomes 409 event.control_only.
func TestIsControlOnlyViolation(t *testing.T) {
	t.Parallel()
	trigger := &pgconn.PgError{Code: "23514", Message: "event x holds control_only sessions, a platform session cannot be added to it"}
	if !isControlOnlyViolation(trigger) {
		t.Error("trigger refusal not recognised")
	}
	if !isControlOnlyViolation(fmt.Errorf("insert: %w", trigger)) {
		t.Error("wrapped trigger refusal not recognised")
	}
	if isControlOnlyViolation(&pgconn.PgError{Code: "23514", Message: "new row violates check constraint sessions_capacity_check"}) {
		t.Error("another check violation taken for control_only")
	}
	if isControlOnlyViolation(&pgconn.PgError{Code: "23505", Message: "control_only duplicate"}) {
		t.Error("another SQLSTATE taken for control_only")
	}
	if isControlOnlyViolation(errors.New("control_only")) || isControlOnlyViolation(nil) {
		t.Error("a plain error taken for control_only")
	}
}

func TestWriteControlOnlyViolation(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeControlOnlyViolation(rec, httptest.NewRequest(http.MethodPost, "/", nil), uuid.New())
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if code := envelopeCode(t, rec); code != CodeEventControlOnly {
		t.Fatalf("code = %s, want %s", code, CodeEventControlOnly)
	}
}
