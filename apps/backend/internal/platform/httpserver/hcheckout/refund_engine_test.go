package hcheckout

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// blipDB is a database that is briefly unreachable: every statement fails.
type blipDB struct{}

var errBlip = errors.New("connection reset by peer")

func (blipDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errBlip
}
func (blipDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, errBlip }
func (blipDB) QueryRow(context.Context, string, ...any) pgx.Row        { return blipRow{} }

type blipRow struct{}

func (blipRow) Scan(...any) error { return errBlip }

// TestRefundModuleSource_DBBlipIsNotAConfigRefusal (PAY-03 review H1): a
// configuration that cannot be READ is an unknown outcome the engine
// retries — never a refunds.ConfigError, which the engine records as a
// refusal (and, on a first attempt, as a failed refund that frees the
// ticket for a second refund while the first may still be in flight).
func TestRefundModuleSource_DBBlipIsNotAConfigRefusal(t *testing.T) {
	q := gen.New(blipDB{})
	_, cfgErr := ResolveProviderConfig(context.Background(), q, uuid.New(), "stripe")
	if cfgErr == nil || !cfgErr.Transient {
		t.Fatalf("a database error must be Transient: %+v", cfgErr)
	}
	if cfgErr.Code != ErrCodeProviderNotConfigured {
		t.Fatalf("the checkout code must stay unchanged: %q", cfgErr.Code)
	}

	src := NewRefundModuleSource(q, payments.Options{})
	_, err := src.Build(context.Background(), uuid.New(), "stripe")
	if err == nil {
		t.Fatal("Build succeeded without a database")
	}
	var refusal *refunds.ConfigError
	if errors.As(err, &refusal) {
		t.Fatalf("a database blip was reported as a configuration refusal: %v", err)
	}
}

// TestRefundModuleSource_MissingConfigIsARefusal: no configuration at all
// stays a ConfigError (the organization has nothing to refund through).
func TestRefundModuleSource_MissingConfigIsARefusal(t *testing.T) {
	_, cfgErr := ResolveProviderConfig(context.Background(), gen.New(emptyDB{}), uuid.New(), "stripe")
	if cfgErr == nil || cfgErr.Transient || cfgErr.Code != ErrCodeProviderNotConfigured {
		t.Fatalf("no configuration must be a non-transient not_configured: %+v", cfgErr)
	}
	_, err := NewRefundModuleSource(gen.New(emptyDB{}), payments.Options{}).Build(context.Background(), uuid.New(), "stripe")
	var refusal *refunds.ConfigError
	if !errors.As(err, &refusal) {
		t.Fatalf("want a ConfigError, got %v", err)
	}
}

// emptyDB answers every query with no rows.
type emptyDB struct{}

func (emptyDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (emptyDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return &emptyRows{}, nil }
func (emptyDB) QueryRow(context.Context, string, ...any) pgx.Row        { return noRow{} }

type noRow struct{}

func (noRow) Scan(...any) error { return pgx.ErrNoRows }

type emptyRows struct{}

func (*emptyRows) Close()                                       {}
func (*emptyRows) Err() error                                   { return nil }
func (*emptyRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*emptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (*emptyRows) Next() bool                                   { return false }
func (*emptyRows) Scan(...any) error                            { return nil }
func (*emptyRows) Values() ([]any, error)                       { return nil, nil }
func (*emptyRows) RawValues() [][]byte                          { return nil }
func (*emptyRows) Conn() *pgx.Conn                              { return nil }
