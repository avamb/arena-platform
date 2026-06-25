// reservation_ttl_177_test.go — unit tests for feature #177
// (resolveReservationTTL precedence: channel override → org default → fallback).
//
// These tests close the TODOs that previously hard-coded a 1200-second TTL in
// reservations.go. They cover every branch of the resolver using in-memory
// fakes for the channel and organization lookups, so no live PostgreSQL is
// required.
package httpserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fakes
// ─────────────────────────────────────────────────────────────────────────────

type fakeChannelTTLLookup struct {
	row gen.SalesChannelRow
	err error
}

func (f *fakeChannelTTLLookup) GetSalesChannelByID(_ context.Context, _, _ uuid.UUID) (gen.SalesChannelRow, error) {
	return f.row, f.err
}

type fakeOrgTTLLookup struct {
	row gen.OrganizationRow
	err error
}

func (f *fakeOrgTTLLookup) GetOrganizationByID(_ context.Context, _ uuid.UUID) (gen.OrganizationRow, error) {
	return f.row, f.err
}

// helper: build a *int32 in one expression
func i32ptr(v int32) *int32 { return &v }

// ─────────────────────────────────────────────────────────────────────────────
// Tier 1 — channel override wins
// ─────────────────────────────────────────────────────────────────────────────

func TestReservationTTL177_ChannelOverrideWins(t *testing.T) {
	t.Parallel()
	channelQ := &fakeChannelTTLLookup{
		row: gen.SalesChannelRow{ReservationTTLOverride: i32ptr(300)},
	}
	orgQ := &fakeOrgTTLLookup{
		// Org default would be 900 s; channel override must win.
		row: gen.OrganizationRow{ReservationTTLSeconds: 900},
	}
	got := resolveReservationTTL(context.Background(), channelQ, orgQ, uuid.New(), uuid.New())
	if got != 300*time.Second {
		t.Fatalf("channel override: want 300s, got %v", got)
	}
}

// A non-positive override (0 or negative) must be ignored and the resolver
// must fall through to the org default. This guards against operators clearing
// the column with 0 by mistake.
func TestReservationTTL177_ChannelOverrideZeroFallsThrough(t *testing.T) {
	t.Parallel()
	channelQ := &fakeChannelTTLLookup{
		row: gen.SalesChannelRow{ReservationTTLOverride: i32ptr(0)},
	}
	orgQ := &fakeOrgTTLLookup{
		row: gen.OrganizationRow{ReservationTTLSeconds: 900},
	}
	got := resolveReservationTTL(context.Background(), channelQ, orgQ, uuid.New(), uuid.New())
	if got != 900*time.Second {
		t.Fatalf("zero override fallthrough: want 900s, got %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier 2 — org default
// ─────────────────────────────────────────────────────────────────────────────

func TestReservationTTL177_OrgDefaultWhenNoChannelOverride(t *testing.T) {
	t.Parallel()
	channelQ := &fakeChannelTTLLookup{
		// nil override = use org default.
		row: gen.SalesChannelRow{ReservationTTLOverride: nil},
	}
	orgQ := &fakeOrgTTLLookup{
		row: gen.OrganizationRow{ReservationTTLSeconds: 600},
	}
	got := resolveReservationTTL(context.Background(), channelQ, orgQ, uuid.New(), uuid.New())
	if got != 600*time.Second {
		t.Fatalf("org default: want 600s, got %v", got)
	}
}

// A channel lookup error (e.g. ErrNoRows for a stale channel reference) must
// not abort TTL resolution — it should fall through to the org default.
func TestReservationTTL177_ChannelErrorFallsThroughToOrg(t *testing.T) {
	t.Parallel()
	channelQ := &fakeChannelTTLLookup{err: pgx.ErrNoRows}
	orgQ := &fakeOrgTTLLookup{
		row: gen.OrganizationRow{ReservationTTLSeconds: 720},
	}
	got := resolveReservationTTL(context.Background(), channelQ, orgQ, uuid.New(), uuid.New())
	if got != 720*time.Second {
		t.Fatalf("channel error fallthrough: want 720s, got %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier 3 — system fallback
// ─────────────────────────────────────────────────────────────────────────────

func TestReservationTTL177_FallbackWhenBothLookupsNil(t *testing.T) {
	t.Parallel()
	got := resolveReservationTTL(context.Background(), nil, nil, uuid.New(), uuid.New())
	if got != defaultReservationTTL {
		t.Fatalf("nil lookups fallback: want %v, got %v", defaultReservationTTL, got)
	}
}

func TestReservationTTL177_FallbackWhenOrgErrorAndNoChannelOverride(t *testing.T) {
	t.Parallel()
	channelQ := &fakeChannelTTLLookup{
		row: gen.SalesChannelRow{ReservationTTLOverride: nil},
	}
	orgQ := &fakeOrgTTLLookup{err: errors.New("transient db error")}
	got := resolveReservationTTL(context.Background(), channelQ, orgQ, uuid.New(), uuid.New())
	if got != defaultReservationTTL {
		t.Fatalf("org error fallback: want %v, got %v", defaultReservationTTL, got)
	}
}

func TestReservationTTL177_FallbackWhenOrgValueZero(t *testing.T) {
	t.Parallel()
	channelQ := &fakeChannelTTLLookup{
		row: gen.SalesChannelRow{ReservationTTLOverride: nil},
	}
	orgQ := &fakeOrgTTLLookup{
		row: gen.OrganizationRow{ReservationTTLSeconds: 0},
	}
	got := resolveReservationTTL(context.Background(), channelQ, orgQ, uuid.New(), uuid.New())
	if got != defaultReservationTTL {
		t.Fatalf("zero org value fallback: want %v, got %v", defaultReservationTTL, got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Interface conformance — *gen.Queries must satisfy both lookup interfaces.
// Compile-time guard: if sqlc regeneration changes the method signatures,
// this assertion fails to compile.
// ─────────────────────────────────────────────────────────────────────────────

func TestReservationTTL177_GenQueriesSatisfiesLookupInterfaces(t *testing.T) {
	t.Parallel()
	var _ channelTTLLookup = (*gen.Queries)(nil)
	var _ orgTTLLookup = (*gen.Queries)(nil)
}
