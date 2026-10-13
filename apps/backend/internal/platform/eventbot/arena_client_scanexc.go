package eventbot

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// ScanException is a platform operator's permission for ONE refund of a
// ticket that was already scanned at the gate.
type ScanException struct {
	ID        string    `json:"id"`
	TicketID  string    `json:"ticket_id"`
	ExpiresAt time.Time `json:"expires_at"`
	// Replayed: the same operator asked again while the exception was live
	// and unused; nothing new was written or announced.
	Replayed bool `json:"replayed"`
}

type scanExceptionRequest struct {
	Hours int    `json:"hours"`
	Note  string `json:"note"`
}

// GrantScanException allows one refund of a scanned ticket
// (POST .../tickets/{ticket_id}/scan-refund-exception, platform superadmin
// only). The note goes to the organization's owners and its sales group; the
// X-Admin-Reason header is the bot's own (AdminReason), as on every call.
func (c *ArenaClient) GrantScanException(ctx context.Context, jwt string, orgID, ticketID uuid.UUID, hours int, note string) (ScanException, error) {
	var out struct {
		ScanException ScanException `json:"scan_exception"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/tickets/"+ticketID.String()+"/scan-refund-exception",
		jwt, scanExceptionRequest{Hours: hours, Note: note}, &out)
	return out.ScanException, err
}
