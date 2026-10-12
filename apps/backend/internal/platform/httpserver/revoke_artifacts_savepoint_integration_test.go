//go:build integration

package httpserver

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
)

// A barcode whose revocation fails must not leave the other tickets'
// barcodes scannable: each statement of RevokeTicketArtifactsTx runs in its
// own savepoint, so the transaction survives one failure.
func TestRevokeTicketArtifacts_OneFailureDoesNotKeepOtherBarcodesScannable(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	ctx := context.Background()
	pool := srv.pgxPool
	org, err := gen.New(pool).InsertOrganization(ctx, "Revoke SP "+uuid.NewString()[:8], "revoke-sp-"+uuid.NewString()[:8], "CZ", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, org.ID) })
	f := newPay03Order(t, pool, org.ID, "stripe")

	var authority uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM barcode_authorities WHERE type = 'platform'`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	codes := [2]uuid.UUID{}
	for i, tk := range f.tickets {
		ref := "SP" + strings.ReplaceAll(uuid.NewString(), "-", "")[:11]
		if err := pool.QueryRow(ctx, `INSERT INTO barcodes (authority_id, external_ref, ticket_id) VALUES ($1, $2, $3) RETURNING id`,
			authority, ref, tk).Scan(&codes[i]); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DROP TRIGGER IF EXISTS revoke_sp_fail ON barcodes`)
		_, _ = pool.Exec(c, `DROP FUNCTION IF EXISTS revoke_sp_fail()`)
		_, _ = pool.Exec(c, `DELETE FROM barcodes WHERE id = ANY($1)`, codes[:])
	})
	// The first ticket's barcode refuses to be revoked.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION revoke_sp_fail() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF OLD.id = '`+codes[0].String()+`'::uuid THEN RAISE EXCEPTION 'refuse'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TRIGGER revoke_sp_fail BEFORE UPDATE ON barcodes FOR EACH ROW EXECUTE FUNCTION revoke_sp_fail()`); err != nil {
		t.Fatal(err)
	}

	tickets := make([]gen.TicketRow, 0, 2)
	for _, tk := range f.tickets {
		row, err := gen.New(pool).GetTicketByID(ctx, tk)
		if err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, row)
	}
	q := gen.New(pool)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		htickets.RevokeTicketArtifactsTx(ctx, slog.Default(), q, q, tx, tickets)
		return nil
	}); err != nil {
		t.Fatalf("the transaction did not survive one failed revocation: %v", err)
	}
	var first, second string
	_ = pool.QueryRow(ctx, `SELECT status FROM barcodes WHERE id = $1`, codes[0]).Scan(&first)
	_ = pool.QueryRow(ctx, `SELECT status FROM barcodes WHERE id = $1`, codes[1]).Scan(&second)
	if first != "active" || second != "revoked" {
		t.Fatalf("barcodes = %s / %s; want active (refused) / revoked", first, second)
	}
}
