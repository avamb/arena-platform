// postgres_store.go — the gen.Queries-backed Store implementation.
//
// This is the ONLY file in this package that imports the postgres/gen
// wrappers. Callers that already hold a *gen.Queries (usually built with
// gen.New(pool) or q.WithTx(tx)) construct a Store via NewStoreFromQueries
// and hand it to Resolve / Touch / MarkVerified / LinkOrg.

package customers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// NewStoreFromQueries wraps a *gen.Queries as a Store. The returned
// Store executes every query against whatever DBTX q was constructed with
// (pool or tx) — no fresh acquisition happens here.
func NewStoreFromQueries(q *gen.Queries) Store {
	return &queriesStore{q: q}
}

type queriesStore struct {
	q *gen.Queries
}

func (s *queriesStore) GetIdentityByStrong(ctx context.Context, kind IdentityKind, value string) (Identity, error) {
	row, err := s.q.GetCustomerIdentityByStrongKey(ctx, string(kind), value)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Identity{}, ErrNotFound
		}
		return Identity{}, err
	}
	return identityFromRow(row), nil
}

func (s *queriesStore) GetIdentityByWeak(ctx context.Context, kind IdentityKind, value string, channelID uuid.UUID) (Identity, error) {
	row, err := s.q.GetCustomerIdentityByWeakKey(ctx, string(kind), value, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Identity{}, ErrNotFound
		}
		return Identity{}, err
	}
	return identityFromRow(row), nil
}

func (s *queriesStore) InsertCustomer(ctx context.Context, displayName, locale string) (Customer, error) {
	var dn, loc *string
	if displayName != "" {
		dn = &displayName
	}
	if locale != "" {
		loc = &locale
	}
	row, err := s.q.InsertCustomer(ctx, dn, loc)
	if err != nil {
		return Customer{}, err
	}
	return customerFromRow(row), nil
}

func (s *queriesStore) InsertIdentity(
	ctx context.Context,
	customerID uuid.UUID,
	kind IdentityKind,
	value string,
	channelID *uuid.UUID,
	source string,
	verifiedAt *time.Time,
) (Identity, error) {
	// Strong identities MUST NOT have a channel_id; the partial unique
	// index does not scope them. Weak identities require one.
	if kind.IsStrong() && channelID != nil {
		channelID = nil
	}
	if kind.IsWeak() && channelID == nil {
		return Identity{}, ErrChannelRequiredForWeak
	}
	if source == "" {
		source = SourceLive
	}
	row, err := s.q.InsertCustomerIdentity(ctx, customerID, string(kind), value, channelID, verifiedAt, source)
	if err != nil {
		if isIdentityUniqueViolation(err) {
			return Identity{}, fmt.Errorf("%w: %w", ErrIdentityConflict, err)
		}
		return Identity{}, err
	}
	return identityFromRow(row), nil
}

// isIdentityUniqueViolation reports whether err is the Postgres 23505 that
// InsertCustomerIdentity's two partial unique indexes raise:
// customer_identities_strong_uq (email/phone/telegram, platform-wide) and
// customer_identities_weak_uq (device/wc_customer/bil24_user, per channel).
// When the driver surfaces a constraint name it must match one of the two;
// when it does not (some pooling layers strip it), the SQLSTATE alone is
// trusted since this statement has no other statement that could raise a
// 23505. Mirrors ordering.isOpenOrderUniqueViolation.
func isIdentityUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "" ||
		pgErr.ConstraintName == "customer_identities_strong_uq" ||
		pgErr.ConstraintName == "customer_identities_weak_uq"
}

// txBeginner is satisfied by both *pgxpool.Pool (Begin opens a genuine
// top-level transaction) and pgx.Tx (Begin opens a nested transaction —
// a Postgres SAVEPOINT). gen.Queries.DB() returns whichever one this
// Store was constructed with, so WithSavepoint gets the right behaviour
// in either case without needing to know which it has.
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithSavepoint runs fn against a Store scoped to a nested transaction: a
// SAVEPOINT when this Store already executes inside an ambient pgx.Tx (the
// case for every real caller in this repo — hfeed's checkout-confirm tx,
// hbil24's CREATE_ORDER_EXT/PAY_ORDER tx, customerimport's import tx), or a
// fresh top-level transaction when it executes directly against a pool
// (h.customerStore in hbil24/cmd_user.go). On success the nested
// transaction is committed (releasing the savepoint); on any error
// returned by fn it is rolled back and the SAME error is returned,
// leaving this Store — and the caller's own ambient transaction, if any —
// fully usable afterward. This is the AGENTS.md "best-effort writes...
// MUST sit behind a SAVEPOINT" pattern (see hbil24.Handler.payBestEffort),
// generalised so platform/customers can apply it to its own inserts and
// so other callers (hfeed) can reuse it for their own best-effort writes
// on the same Store.
//
// When the underlying DBTX does not support Begin (a test double with no
// real transaction semantics), fn just runs directly against s — safe for
// unit tests that only exercise control flow, but callers that need real
// rollback isolation must supply a DBTX backed by pgx.
func (s *queriesStore) WithSavepoint(ctx context.Context, fn func(Store) error) error {
	beginner, ok := s.q.DB().(txBeginner)
	if !ok {
		return fn(s)
	}
	sp, err := beginner.Begin(ctx)
	if err != nil {
		return err
	}
	nested := &queriesStore{q: gen.New(sp)}
	if err := fn(nested); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	if err := sp.Commit(ctx); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	return nil
}

func (s *queriesStore) UpdateDisplayName(ctx context.Context, customerID uuid.UUID, displayName string) error {
	return s.q.UpdateCustomerDisplayName(ctx, customerID, displayName)
}

func (s *queriesStore) TouchIdentity(ctx context.Context, id uuid.UUID) error {
	return s.q.TouchCustomerIdentityLastSeen(ctx, id)
}

func (s *queriesStore) MarkIdentityVerified(ctx context.Context, id uuid.UUID, at time.Time) error {
	return s.q.MarkCustomerIdentityVerified(ctx, id, at)
}

func (s *queriesStore) UpsertOrgLink(ctx context.Context, customerID, orgID uuid.UUID, source string) error {
	return s.q.UpsertCustomerOrgLink(ctx, customerID, orgID, source)
}

func (s *queriesStore) InsertMergeCandidate(ctx context.Context, a, b uuid.UUID, reason string) error {
	_, err := s.q.InsertCustomerMergeCandidate(ctx, a, b, reason)
	return err
}

func (s *queriesStore) InsertAttribute(ctx context.Context, customerID uuid.UUID, orgID *uuid.UUID, key, valueJSON, source string) error {
	return s.q.InsertCustomerAttribute(ctx, customerID, orgID, key, valueJSON, source)
}

func (s *queriesStore) GetCustomer(ctx context.Context, id uuid.UUID) (Customer, error) {
	row, err := s.q.GetCustomerByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Customer{}, ErrNotFound
		}
		return Customer{}, err
	}
	return customerFromRow(row), nil
}

func customerFromRow(r gen.CustomerRow) Customer {
	c := Customer{
		ID:        r.ID,
		SystemID:  r.SystemID,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
	if r.DisplayName != nil {
		c.DisplayName = *r.DisplayName
	}
	if r.Locale != nil {
		c.Locale = *r.Locale
	}
	return c
}

func identityFromRow(r gen.CustomerIdentityRow) Identity {
	return Identity{
		ID:              r.ID,
		CustomerID:      r.CustomerID,
		Kind:            IdentityKind(r.Kind),
		ValueNormalized: r.ValueNormalized,
		ChannelID:       r.ChannelID,
		VerifiedAt:      r.VerifiedAt,
		FirstSeenAt:     r.FirstSeenAt,
		LastSeenAt:      r.LastSeenAt,
		Source:          r.Source,
	}
}
