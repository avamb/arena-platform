package htickets

// complimentary_validate.go — the checks POST /v1/organizations/{org_id}/
// complimentary makes on the shape of a request before it touches the
// database (EC-12, spec 08_architecture/35 §6.3): the size of one operation,
// the recipients and their names. Until then a request could name any text as
// a recipient (the guest simply never got a letter) and any quantity.

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxComplimentaryQty is the most tickets one issuance may hold. More goes
	// in several operations; the Telegram bot says so in the same words.
	MaxComplimentaryQty = 50
	// maxComplimentaryNameRunes is the longest guest name stored on a ticket
	// (the tickets_holder_name_len_check of migration 0136).
	maxComplimentaryNameRunes = 200
)

// complimentaryValidationError is a refusal with the API error code, a
// sentence and the field it is about.
type complimentaryValidationError struct {
	code, message string
	details       map[string]any
}

// validateComplimentaryRequest checks qty, recipients and recipient_names. It
// normalises them in place: addresses and names are trimmed. recipient_names[i]
// belongs to ticket i; an empty slot is an anonymous ticket or an unnamed guest.
func validateComplimentaryRequest(req *createComplimentaryIssuanceRequest) *complimentaryValidationError {
	if req.Qty > MaxComplimentaryQty {
		return &complimentaryValidationError{
			code:    "complimentary.qty_too_large",
			message: "at most 50 tickets can be issued in one operation, use several operations for more",
			details: map[string]any{"field": "qty", "max": MaxComplimentaryQty, "requested": req.Qty},
		}
	}
	if len(req.Recipients) > int(req.Qty) {
		return &complimentaryValidationError{
			code:    "complimentary.too_many_recipients",
			message: "there are more recipients than tickets",
			details: map[string]any{"field": "recipients", "qty": req.Qty, "recipients": len(req.Recipients)},
		}
	}
	for i, r := range req.Recipients {
		r = strings.TrimSpace(r)
		req.Recipients[i] = r
		if r != "" && !ValidResendEmail(r) {
			return &complimentaryValidationError{
				code:    "complimentary.invalid_recipient",
				message: "a recipient is not a valid e-mail address",
				details: map[string]any{"field": "recipients", "index": i},
			}
		}
	}
	if len(req.RecipientNames) > int(req.Qty) {
		return &complimentaryValidationError{
			code:    "complimentary.invalid_recipient_name",
			message: "recipient_names has more entries than tickets",
			details: map[string]any{"field": "recipient_names"},
		}
	}
	for i, n := range req.RecipientNames {
		n = strings.TrimSpace(n)
		req.RecipientNames[i] = n
		if n == "" {
			continue
		}
		if utf8.RuneCountInString(n) > maxComplimentaryNameRunes || strings.IndexFunc(n, unicode.IsControl) >= 0 {
			return &complimentaryValidationError{
				code:    "complimentary.invalid_recipient_name",
				message: "a recipient name is too long or has control characters",
				details: map[string]any{"field": "recipient_names", "index": i, "max": maxComplimentaryNameRunes},
			}
		}
	}
	return nil
}
