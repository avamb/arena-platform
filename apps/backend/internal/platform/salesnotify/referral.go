package salesnotify

// referral.go — the organizer's Telegram notice of "Refer a friend" (spec 52
// section 5.2): a friend who used a personal code got the money back or opened
// a dispute after the referrer's cashback had been paid. The text speaks the
// organization's language (organizations.default_locale) through the
// salesnotify.referral.* keys of the shared i18n bundle, like the shop
// messages. It carries no buyer e-mail, name or phone: only the code, the
// friend's order number and the amount.

import (
	"strings"
)

// ReferralReversalInfo is what the organizer notice says.
type ReferralReversalInfo struct {
	// Locale is the organization's default locale.
	Locale string
	// OrgName is the organization's name.
	OrgName string
	// Code is the personal code the friend used.
	Code string
	// FriendOrderNumber is the friend's order number (orders.system_id).
	FriendOrderNumber int64
	// AmountMinor and Currency are the cashback already paid to the referrer.
	AmountMinor int64
	Currency    string
	// Disputed: the friend opened a dispute (otherwise the friend got money back).
	Disputed bool
	// CodePaused: the code is on pause because of repeated reversals.
	CodePaused bool
}

// referralKeys lists every salesnotify.referral.* key, for the completeness test.
var referralKeys = []string{
	"salesnotify.referral.title_refunded",
	"salesnotify.referral.title_disputed",
	"salesnotify.referral.body_refunded",
	"salesnotify.referral.body_disputed",
	"salesnotify.referral.code_paused",
}

// FormatReferralReversal renders the notice (HTML parse mode).
func FormatReferralReversal(in ReferralReversalInfo) string {
	kind := "refunded"
	if in.Disputed {
		kind = "disputed"
	}
	data := map[string]any{
		"Org":    esc(in.OrgName),
		"Code":   esc(in.Code),
		"Number": in.FriendOrderNumber,
		"Amount": esc(formatShopMoney(in.AmountMinor, in.Currency, in.Locale)),
	}
	var b strings.Builder
	b.WriteString(say(in.Locale, "salesnotify.referral.title_"+kind, data))
	b.WriteString("\n\n")
	b.WriteString(say(in.Locale, "salesnotify.referral.body_"+kind, data))
	if in.CodePaused {
		b.WriteString("\n\n")
		b.WriteString(say(in.Locale, "salesnotify.referral.code_paused", data))
	}
	return b.String()
}
