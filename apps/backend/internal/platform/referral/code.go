package referral

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
)

var (
	// ErrNoProgram: the order's event has no active programme in its period.
	ErrNoProgram = errors.New("referral: no active programme for the event")
	// ErrNoSessions: the order's event has no live date, so a code would have
	// nothing to apply to (an empty session list means "every date" to the
	// checkout, which a personal code must never mean).
	ErrNoSessions = errors.New("referral: the event has no live session")
	// ErrNotEligible: the order cannot carry a personal code (not paid with
	// money, an invitation, an order a selling site took the money for).
	ErrNotEligible = errors.New("referral: the order is not eligible for a personal code")
	// ErrOrderNotFound: no such order.
	ErrOrderNotFound = errors.New("referral: order not found")
)

// codeAlphabet is the alphabet of spec 43 (no 0/O, 1/I/L): 31 characters.
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

const (
	maxCodeAttempts = 8
	prefixLen       = 6
	suffixLen       = 6
	plainCodeLen    = 10
)

// Code is a personal code of a referrer's order.
type Code struct {
	PromoCodeID       uuid.UUID
	ProgramID         uuid.UUID
	Code              string
	FriendDiscountPct int
	CashbackPct       int
	// Status is the promo code's status ("active", "paused", "inactive").
	Status string
}

// IssueCode returns the personal code of a paid order, creating it on the
// first call. created tells whether this call made it. It is idempotent: a
// repeat (or a race) returns the code the order already has. The order must be
// paid (or partly refunded) with money (total above zero) and not come from a
// selling site; its event needs an active programme in its period, else
// ErrNoProgram, or ErrNoSessions when the event has no live date. A code issued earlier is returned whatever the programme's
// status is now.
//
// db is a pool or a transaction. Issuing is not part of the payment's
// transaction: callers ask for the code after the order is paid.
func IssueCode(ctx context.Context, db gen.DBTX, orderID uuid.UUID) (Code, bool, error) {
	return issueCode(ctx, db, orderID, time.Now(), rand.Reader)
}

func issueCode(ctx context.Context, db gen.DBTX, orderID uuid.UUID, now time.Time, rnd io.Reader) (Code, bool, error) {
	q := gen.New(db)
	if existing, err := q.GetReferralCodeByReferrerOrder(ctx, orderID); err == nil {
		return codeFromRow(existing), false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Code{}, false, err
	}
	ord, err := q.GetReferralOrderFacts(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Code{}, false, ErrOrderNotFound
	}
	if err != nil {
		return Code{}, false, err
	}
	if !orderEligible(ord) {
		return Code{}, false, ErrNotEligible
	}
	program, err := q.GetActiveReferralProgramForEvent(ctx, ord.EventID, now)
	if errors.Is(err, pgx.ErrNoRows) {
		return Code{}, false, ErrNoProgram
	}
	if err != nil {
		return Code{}, false, err
	}
	sessions, err := q.ListLiveSessionIDsForEvent(ctx, ord.EventID)
	if err != nil {
		return Code{}, false, err
	}
	if len(sessions) == 0 {
		return Code{}, false, ErrNoSessions
	}
	var maxUses *int32
	if program.MaxFriends != nil {
		v := int32(*program.MaxFriends)
		maxUses = &v
	}
	for range maxCodeAttempts {
		text, err := generateCode(ord.BuyerName, rnd)
		if err != nil {
			return Code{}, false, err
		}
		id, inserted, err := q.InsertReferralPromoCode(ctx, gen.InsertReferralPromoCodeParams{
			OrgID: ord.OrgID, Code: text, DiscountPct: int64(program.FriendDiscountPct), SessionIDs: sessions,
			MaxUses: maxUses, ValidUntil: program.EndsAt, ProgramID: program.ID, ReferrerOrderID: orderID,
			CashbackPct: program.CashbackPct, CapPct: program.CashbackCapPct,
		})
		if err != nil {
			return Code{}, false, err
		}
		if inserted {
			return Code{
				PromoCodeID: id, ProgramID: program.ID, Code: text, Status: "active",
				FriendDiscountPct: int(program.FriendDiscountPct), CashbackPct: int(program.CashbackPct),
			}, true, nil
		}
		// Not written: the code text collided, or a parallel call issued the
		// order's code first. The order's own code tells which.
		if existing, err := q.GetReferralCodeByReferrerOrder(ctx, orderID); err == nil {
			return codeFromRow(existing), false, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return Code{}, false, err
		}
	}
	return Code{}, false, fmt.Errorf("referral: no free code after %d attempts", maxCodeAttempts)
}

func codeFromRow(r gen.ReferralCodeRow) Code {
	return Code{
		PromoCodeID: r.PromoCodeID, ProgramID: r.ProgramID, Code: r.Code, Status: r.Status,
		FriendDiscountPct: int(r.FriendDiscountPct), CashbackPct: int(r.CashbackPct),
	}
}

// orderEligible: paid with money and not a selling site's or an invitation.
func orderEligible(o gen.ReferralOrderFacts) bool {
	switch o.Status {
	case "paid", "partially_refunded":
	default:
		return false
	}
	switch o.Source {
	case "complimentary", "bil24_gateway":
		return false
	}
	return o.Total > 0
}

// generateCode makes "ANNA-7K2QX9": a Latin prefix of up to six letters from
// the buyer's name plus six characters of the code alphabet (31^6, about 887
// million); without a usable name, ten characters. The random bytes come from
// rnd.
func generateCode(buyerName string, rnd io.Reader) (string, error) {
	prefix := namePrefix(buyerName)
	if prefix == "" {
		return randomChars(plainCodeLen, rnd)
	}
	tail, err := randomChars(suffixLen, rnd)
	if err != nil {
		return "", err
	}
	return prefix + "-" + tail, nil
}

// namePrefix is the first word of the name, transliterated to Latin letters,
// upper-cased and cut to six letters; "" when no Latin letter survives.
func namePrefix(name string) string {
	slug := geoslug.Slugify(name)
	for word := range strings.SplitSeq(slug, "-") {
		var b strings.Builder
		for _, r := range word {
			if r >= 'a' && r <= 'z' {
				b.WriteRune(r - 'a' + 'A')
			}
		}
		if b.Len() > 0 {
			s := b.String()
			if len(s) > prefixLen {
				s = s[:prefixLen]
			}
			return s
		}
	}
	return ""
}

// randomChars draws n characters of codeAlphabet without modulo bias.
func randomChars(n int, rnd io.Reader) (string, error) {
	const limit = 256 - 256%len(codeAlphabet) // reject the tail so every letter is equally likely
	out := make([]byte, 0, n)
	buf := make([]byte, 16)
	for len(out) < n {
		if _, err := io.ReadFull(rnd, buf); err != nil {
			return "", fmt.Errorf("referral: random source: %w", err)
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, codeAlphabet[int(b)%len(codeAlphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}
