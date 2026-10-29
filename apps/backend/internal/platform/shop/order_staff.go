package shop

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// Staff-facing order tools: the internal note and the repeat of a buyer
// letter. Neither changes the order's status, stock or money.

// MaxStaffNoteLen is the longest internal note, in characters (the column's
// CHECK counts the same).
const MaxStaffNoteLen = 2000

// StaffNoteAuthor says who writes a note. UserID is the signed-in user (the
// name shown with the note is read from their account); Label is what is
// shown when there is no user, such as "api_key".
type StaffNoteAuthor struct {
	UserID *uuid.UUID
	Label  string
}

// authorName is the name stored beside a note: the user's first and last
// name, else their e-mail, else the label.
func (s *Service) authorName(ctx context.Context, a StaffNoteAuthor) string {
	if a.UserID != nil {
		var email, first, last *string
		err := s.db.QueryRow(ctx, `SELECT email, first_name, last_name FROM users WHERE id = $1`, *a.UserID).Scan(&email, &first, &last)
		if err == nil {
			parts := []string{}
			for _, p := range []*string{first, last} {
				if p != nil && strings.TrimSpace(*p) != "" {
					parts = append(parts, strings.TrimSpace(*p))
				}
			}
			if len(parts) > 0 {
				return truncateRunes(strings.Join(parts, " "), 200)
			}
			if email != nil && strings.TrimSpace(*email) != "" {
				return truncateRunes(strings.TrimSpace(*email), 200)
			}
		}
	}
	if a.Label == "" {
		return "staff"
	}
	return truncateRunes(a.Label, 200)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// SetStaffNote replaces the internal note of an order. An empty note erases
// it with its author and time. The note is for staff: no public route reads it.
func (s *Service) SetStaffNote(ctx context.Context, orgID, id uuid.UUID, note string, author StaffNoteAuthor) (OrderView, error) {
	note = strings.TrimSpace(note)
	if !textOK(note, MaxStaffNoteLen) {
		return OrderView{}, invalid("shop.text_too_long", "the note is limited to 2000 characters")
	}
	if err := s.ready(); err != nil {
		return OrderView{}, err
	}
	by := ""
	if note != "" {
		by = s.authorName(ctx, author)
	}
	var out gen.ShopOrderRow
	err := s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		o, err := q.GetShopOrderForUpdate(ctx, id, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		out, err = q.SetShopOrderStaffNote(ctx, o.ID, note, by)
		return err
	})
	if err != nil {
		return OrderView{}, err
	}
	return s.view(ctx, s.q, out)
}

// ResendEmail queues the buyer's e-mail of an order once more. kind is
// EmailReceived or EmailShipped. The letter is the one the worker writes for
// the first time (shop.order_email), with Resend set so the "already sent"
// marker does not swallow it. It refuses what the worker would skip: no
// address, an order that was never paid or is closed unpaid, a "shipped"
// letter for an order that did not ship or has nothing to ship.
func (s *Service) ResendEmail(ctx context.Context, orgID, id uuid.UUID, kind string) error {
	if kind != EmailReceived && kind != EmailShipped {
		return newErr(http.StatusBadRequest, "shop.invalid_email_kind", "kind must be received or shipped")
	}
	if err := s.ready(); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx pgx.Tx, q *gen.Queries) error {
		o, err := q.GetShopOrderForUpdate(ctx, id, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(o.BuyerEmail) == "" {
			return newErr(http.StatusConflict, "shop.order_no_buyer_email", "this order has no buyer e-mail address to write to")
		}
		switch o.Status {
		case StatusPaid, StatusShipped, StatusPartiallyRefunded, StatusRefunded:
		default:
			return newErr(http.StatusConflict, "shop.order_email_unavailable",
				"the buyer is written to once the order is paid; this one is "+o.Status)
		}
		if kind == EmailShipped && (o.DeliveryKind == DeliveryNone || o.ShippedAt == nil) {
			return newErr(http.StatusConflict, "shop.order_email_unavailable", "the shipping letter exists only for an order that was shipped")
		}
		_, err = worker.EnqueueInTx(ctx, tx, JobTypeOrderEmail, OrderEmailPayload{
			OrderID: o.ID.String(), OrgID: o.OrgID.String(), Kind: kind, Resend: true,
		}, orderEmailAttempts)
		return err
	})
}
