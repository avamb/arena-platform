// Package invoicing issues the platform's invoices to organizers and records
// their payment (billing tasks B3-3, B4-1, B4-2; 41 sections 6.4, 6.6, 8.1
// B-6, B-7, B-14): usage invoices from charges minus allocations, credit-pack
// invoices whose payment creates the credit lot in the same transaction, a
// gapless number per issuer and year, a payment reference that a bank
// transfer carries, a card fee line for a Stripe card payment, incoming bank
// payments matched by reference and amount, void that never touches a paid
// invoice.
//
// Every function takes a transaction-bound *gen.Queries: the caller owns the
// transaction, and network calls (Stripe) happen outside it.
package invoicing

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/bits"
	"strings"
)

// Errors a caller maps to API answers.
var (
	ErrCardFeeNotConfigured = errors.New("invoicing: no card fee is configured for this currency")
	ErrNotPayable           = errors.New("invoicing: the invoice is not open for payment")
	ErrAlreadyPaid          = errors.New("invoicing: the invoice is already paid by another payment")
	ErrAmountMismatch       = errors.New("invoicing: the amount does not match the invoice")
	ErrPaidNeedsCreditNote  = errors.New("invoicing: a paid invoice is corrected by a credit note, never voided")
	ErrNotDraft             = errors.New("invoicing: the invoice is not a draft")
	ErrPeriodNotClosed      = errors.New("invoicing: the period has not ended yet")
	ErrBadInput             = errors.New("invoicing: bad input")
	ErrPackUnavailable      = errors.New("invoicing: the pack is not on sale for this account")
)

// Payment methods and the ways an invoice gets paid.
const (
	MethodBankTransfer = "bank_transfer"
	MethodStripeCard   = "stripe_card"

	PaidViaBankTransfer  = "bank_transfer"
	PaidViaStripeCard    = "stripe_card"
	PaidViaStripeInvoice = "stripe_invoice"
	PaidViaManual        = "manual"
)

// maxMinor bounds amounts the fee arithmetic multiplies.
const maxMinor = int64(1) << 42

// CardFee is the "payment system fee" line of an invoice of totalMinor paid by
// card (owner decision B-14: the payer bears the Stripe fee). It grosses the
// amount up so that after the provider keeps pctBp of the charged amount plus
// fixedMinor, the platform nets exactly totalMinor:
//
//	charged = ceil((total + fixed) × 10000 / (10000 − pctBp)),  fee = charged − total
//
// rounded UP to a whole minor unit, so the platform never nets less than the
// invoice. A bank transfer is a net payment and has no such line.
func CardFee(totalMinor int64, pctBp int32, fixedMinor int64) (int64, error) {
	if totalMinor < 0 || totalMinor > maxMinor || fixedMinor < 0 || fixedMinor > maxMinor || pctBp < 0 || pctBp >= 10000 {
		return 0, fmt.Errorf("%w: card fee of %d at %d bp + %d", ErrBadInput, totalMinor, pctBp, fixedMinor)
	}
	if totalMinor == 0 {
		return 0, nil
	}
	base := uint64(totalMinor + fixedMinor)
	den := uint64(10000 - int64(pctBp))
	hi, lo := bits.Mul64(base, 10000)
	q, rem := bits.Div64(hi, lo, den)
	if rem != 0 {
		q++
	}
	if q > uint64(maxMinor) {
		return 0, fmt.Errorf("%w: card fee out of bounds", ErrBadInput)
	}
	return int64(q) - totalMinor, nil
}

// referenceAlphabet has no 0/O, 1/I/L or U, so a reference read aloud or
// typed into a bank form survives.
const referenceAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// referencePrefix starts every payment reference.
const referencePrefix = "ARN"

// NewPaymentReference returns a fresh reference like ARN-7KQM-X2TB.
func NewPaymentReference() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(referencePrefix)
	for i, c := range raw {
		if i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(referenceAlphabet[int(c)%len(referenceAlphabet)])
	}
	return b.String(), nil
}

// FindReferences returns every payment reference written in a bank
// payment's remittance text, in the canonical form ARN-XXXX-XXXX. Spaces,
// dashes, dots and case do not matter ("arn 7kqm x2tb" is found); every
// candidate is checked against the invoices afterwards, so a false candidate
// only fails to match.
func FindReferences(text string) []string {
	var norm strings.Builder
	for _, r := range strings.ToUpper(text) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			norm.WriteRune(r)
		}
	}
	s := norm.String()
	var out []string
	seen := map[string]bool{}
	for i := 0; i+len(referencePrefix)+8 <= len(s); i++ {
		if s[i:i+len(referencePrefix)] != referencePrefix {
			continue
		}
		body := s[i+len(referencePrefix) : i+len(referencePrefix)+8]
		if strings.IndexFunc(body, func(r rune) bool { return !strings.ContainsRune(referenceAlphabet, r) }) >= 0 {
			continue
		}
		ref := referencePrefix + "-" + body[:4] + "-" + body[4:]
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}
