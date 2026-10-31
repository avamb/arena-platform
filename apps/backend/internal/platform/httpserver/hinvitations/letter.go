package hinvitations

// letter.go — the state of the guest's letter on a list item (spec 46,
// section 3.4): the delivery_jobs of the order's tickets folded into one word.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Letter states of a list item. Skipped and disabled mirror delivery_jobs.
const (
	letterPending  = "pending"
	letterSent     = "sent"
	letterFailed   = "failed"
	letterSkipped  = "skipped"
	letterDisabled = "disabled"
)

// letterResponse is the letter of an invitation as a list item shows it. The
// raw error text and the address never leave the API: LastErrorCode is a
// short category.
type letterResponse struct {
	Status        string     `json:"status"`
	SentAt        *time.Time `json:"sent_at"`
	LastErrorCode *string    `json:"last_error_code"`
}

// letterFromRow folds the job counts of one order into its letter state.
func letterFromRow(r gen.InvitationLetterRow) *letterResponse {
	if r.Jobs == 0 {
		return nil
	}
	out := &letterResponse{}
	switch {
	case r.Failed > 0:
		out.Status = letterFailed
	case r.Pending > 0:
		out.Status = letterPending
	case r.Sent > 0:
		out.Status = letterSent
		out.SentAt = r.SentAt
	case r.Skipped > 0:
		out.Status = letterSkipped
	default:
		out.Status = letterDisabled
	}
	if out.Status != letterSent && out.Status != letterPending && r.LastError != nil {
		if code := letterErrorCode(out.Status, *r.LastError); code != "" {
			out.LastErrorCode = &code
		}
	}
	return out
}

// letterErrorCode classifies a stored error text into a short category.
// Codes: address_rejected, address_expired, no_recipient, sender_disabled,
// send_failed.
func letterErrorCode(status, text string) string {
	t := strings.ToLower(text)
	switch status {
	case letterDisabled:
		return "sender_disabled"
	case letterSkipped:
		switch {
		case strings.Contains(t, "expired"):
			return "address_expired"
		case strings.Contains(t, "no email"):
			return "no_recipient"
		}
		return ""
	}
	for _, k := range []string{"550", "551", "552", "553", "554", "invalid", "mailbox", "recipient", "rejected", "bounce", "does not exist", "unknown user"} {
		if strings.Contains(t, k) {
			return "address_rejected"
		}
	}
	return "send_failed"
}

// pageExtras are the per-order facts a list page adds to its rows.
type pageExtras struct {
	places  map[uuid.UUID][]lineResponse
	letters map[uuid.UUID]*letterResponse
}

// loadPageExtras reads the places and the letter state of a page of orders:
// two statements for the whole page, shared by the session list and the
// organization-wide list.
func (h *Handler) loadPageExtras(ctx context.Context, ids []uuid.UUID) (pageExtras, error) {
	ex := pageExtras{places: map[uuid.UUID][]lineResponse{}, letters: map[uuid.UUID]*letterResponse{}}
	if len(ids) == 0 {
		return ex, nil
	}
	lines, err := h.queries.ListInvitationOrderLines(ctx, ids)
	if err != nil {
		return ex, err
	}
	for _, l := range lines {
		ex.places[l.OrderID] = append(ex.places[l.OrderID], toLine(l))
	}
	letters, err := h.queries.ListInvitationLetters(ctx, ids)
	if err != nil {
		return ex, err
	}
	for _, r := range letters {
		ex.letters[r.OrderID] = letterFromRow(r)
	}
	return ex, nil
}
