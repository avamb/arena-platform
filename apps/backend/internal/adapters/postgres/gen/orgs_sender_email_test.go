package gen

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type senderCaptureDB struct {
	sql  string
	args []any
}

func (d *senderCaptureDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (d *senderCaptureDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (d *senderCaptureDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	d.sql, d.args = sql, args
	return senderErrRow{}
}

type senderErrRow struct{}

func (senderErrRow) Scan(...any) error { return pgx.ErrNoRows }

// Regression: PG answered 42P08 "could not determine data type of parameter $2"
// because $2 appeared both as a column assignment and in "$2 IS NULL".
func TestUpdateOrganizationSenderEmail_ParamIsTyped(t *testing.T) {
	addr := "no-reply@example.com"
	for _, v := range []*string{&addr, nil} {
		db := &senderCaptureDB{}
		_, _, _ = New(db).UpdateOrganizationSenderEmail(context.Background(), uuid.New(), v)
		if strings.Contains(db.sql, "$2 IS NULL") || !strings.Contains(db.sql, "$2::text IS NULL") {
			t.Fatalf("untyped $2 in SQL: %s", db.sql)
		}
		if len(db.args) != 2 {
			t.Fatalf("args = %d, want 2", len(db.args))
		}
	}
}
