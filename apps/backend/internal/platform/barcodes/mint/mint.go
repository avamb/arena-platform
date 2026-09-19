// Package mint is the single place that turns an ean13.Random draw into a
// won barcodes row ("platform EAN-13 codes are random" change).
//
// Uniqueness of a platform-minted code must hold FOREVER and across ALL
// barcode authorities, not just 'platform': the owner plans to import
// already-sold tickets of live clients into the 'legacy_bil24' authority,
// and the scanner's GetBarcodeByExternalRefAny looks across every
// authority in one round-trip (spec §7.14), so a cross-authority duplicate
// external_ref would be ambiguous. The barcodes table's own UNIQUE
// constraint is scoped to (authority_id, external_ref) and cannot catch
// that on its own, so this package draws a candidate, asks the database to
// atomically claim it (gen.Queries.InsertBarcodeIfUnique — a WHERE NOT
// EXISTS across all authorities, ON CONFLICT DO NOTHING for the
// same-authority race), and redraws on a loss.
//
// Per AGENTS.md ("best effort writes inside a money transaction MUST sit
// behind a SAVEPOINT"): a losing attempt must never surface as a raw 23505
// inside the caller's transaction, because that would abort the whole tx
// (ticket issuance, PAY_ORDER, etc.) on a coin-flip. InsertBarcodeIfUnique
// is written so a collision is an ordinary "zero rows returned" result, not
// an error — there is nothing to roll back, and EAN13's loop simply draws
// again.
package mint

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// MaxAttempts bounds the retry loop in EAN13. Each attempt is a single
// atomic statement (see package doc), so a bounded loop can never leave a
// half-written row; it only ever fails to mint at all, which EAN13 reports
// as a clear error instead of retrying forever.
const MaxAttempts = 8

// Inserter is the narrow query surface EAN13 needs to atomically claim a
// candidate barcode. Satisfied by *gen.Queries — both a bare pool handle
// and one built over an in-flight pgx.Tx via gen.New(tx) or gen.Queries.DB
// — so callers pass whatever *gen.Queries they already have (including
// one scoped to the caller's own transaction, which is required: minting
// must happen inside the ticket-issuance transaction per the task spec).
type Inserter interface {
	// InsertBarcodeIfUnique attempts to insert one barcodes row for
	// (authorityID, externalRef, ticketID). ok=false, err=nil means the
	// candidate collided — under authorityID or under any OTHER authority
	// — and nothing was written; the caller should draw a new candidate.
	// A non-nil err is a real failure (connection, constraint other than
	// the uniqueness ones this function guards, etc.).
	InsertBarcodeIfUnique(ctx context.Context, authorityID uuid.UUID, externalRef string, ticketID *uuid.UUID) (ok bool, err error)
}

// Generator draws one barcode candidate. Production callers pass nil to
// EAN13, which defaults to ean13.Random. Tests inject a Generator that
// returns a colliding candidate first (and a fresh one on the next call) to
// prove the retry loop actually redraws instead of trusting the first draw.
type Generator func() (string, error)

// EAN13 mints a new platform-authority EAN-13 barcode that is unique across
// every barcode authority. It draws candidates via gen (ean13.Random when
// gen is nil) and atomically claims one through ins, retrying up to
// MaxAttempts times whenever a draw collides. On success it returns the won
// code; the caller is then free to write dependent rows (e.g. the
// ticket_credentials type='ean13' row) using that same value — those
// writes must happen AFTER EAN13 returns, never before, so nothing
// references a code that was never actually won.
//
// Returns a clear error if every attempt collides (exceptionally unlikely
// with a 10^10 random space; more likely a sign of a real bug, e.g. gen
// always returning the same candidate) or if a draw/claim itself errors.
func EAN13(ctx context.Context, ins Inserter, authorityID uuid.UUID, ticketID *uuid.UUID, gen Generator) (string, error) {
	if gen == nil {
		gen = ean13.Random
	}
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		code, err := gen()
		if err != nil {
			return "", fmt.Errorf("mint: draw ean13 candidate (attempt %d/%d): %w", attempt, MaxAttempts, err)
		}
		ok, err := ins.InsertBarcodeIfUnique(ctx, authorityID, code, ticketID)
		if err != nil {
			return "", fmt.Errorf("mint: claim ean13 candidate %q (attempt %d/%d): %w", code, attempt, MaxAttempts, err)
		}
		if ok {
			return code, nil
		}
	}
	return "", fmt.Errorf("mint: exhausted %d attempts drawing a unique ean13 code", MaxAttempts)
}
