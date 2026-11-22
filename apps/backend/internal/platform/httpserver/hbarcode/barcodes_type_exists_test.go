package hbarcode

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// uniqueViolationDB answers every QueryRow with a row whose Scan fails with
// SQLSTATE 23505, as the unique index on barcode_authorities(type) does.
type uniqueViolationDB struct{}

func (uniqueViolationDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("not used")
}

func (uniqueViolationDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("not used")
}

func (uniqueViolationDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return uniqueViolationRow{}
}

type uniqueViolationRow struct{}

func (uniqueViolationRow) Scan(...any) error {
	return &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}
}

// TestCreateBarcodeAuthority_DuplicateTypeIs409 checks that a second
// authority of a type that already exists is refused with 409
// barcode_authority.type_exists.
func TestCreateBarcodeAuthority_DuplicateTypeIs409(t *testing.T) {
	h := New(gen.New(uniqueViolationDB{}), nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req := httptest.NewRequest(http.MethodPost, "/v1/barcodes/authorities",
		strings.NewReader(`{"type":"platform","label":"Second platform"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleCreateBarcodeAuthority(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate type: want 409, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "barcode_authority.type_exists") {
		t.Fatalf("duplicate type: want barcode_authority.type_exists, got %s", rec.Body.String())
	}
}
