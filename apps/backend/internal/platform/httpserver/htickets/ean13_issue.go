package htickets

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/mint"
)

// mintTicketEAN13s gives every ticket in ticketIDs a platform-minted EAN-13
// barcode: a barcodes row (authority 'platform') won through mint.EAN13, then
// the ticket_credentials row (type 'ean13') carrying the same code.
//
// This is the ONE place issuance mints. IssueTicketsForCheckout (purchased
// tickets) and HandleCreateComplimentaryIssuance (invitations) both call it,
// so an invitation is scannable at the gate exactly like a bought ticket.
// Until U-3 the invitation path minted nothing: the PDF was printed without a
// barcode and SCAN_TICKET did not find the ticket.
//
// txQ MUST be bound to the caller's open transaction. mint.EAN13 answers a
// lost draw with zero rows, never a raw 23505, so no SAVEPOINT is needed
// inside a money transaction. The credential is written only after the
// barcodes row is won.
func mintTicketEAN13s(ctx context.Context, txQ *gen.Queries, ticketIDs []uuid.UUID) error {
	if len(ticketIDs) == 0 {
		return nil
	}
	platformAuthority, err := txQ.GetBarcodeAuthorityByType(ctx, "platform")
	if err != nil {
		return fmt.Errorf("get platform barcode authority: %w", err)
	}
	for _, id := range ticketIDs {
		ticketID := id
		code, err := mint.EAN13(ctx, txQ, platformAuthority.ID, &ticketID, nil)
		if err != nil {
			return fmt.Errorf("mint ean13 barcode for ticket %s: %w", id, err)
		}
		if _, err := txQ.InsertTicketCredential(ctx, id, "ean13", code); err != nil {
			return fmt.Errorf("insert ean13 credential for ticket %s: %w", id, err)
		}
	}
	return nil
}
