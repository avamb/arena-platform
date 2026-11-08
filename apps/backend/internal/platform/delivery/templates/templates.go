// Package templates renders localized email bodies and subjects for the
// ticket-deliver worker (feature #289, T-2).
//
// Templates live next to this file as <name>.<locale>.tmpl, where:
//
//   - name   ∈ {"ticket", "invitation", "change", "cancel", "refund"}  (the email kind)
//   - locale ∈ {"en", "de", "es", "fr", "he", "cs", "ru"}  (the AllPay
//     markets baseline plus cs/ru, added for the Czech-market launch —
//     feature #565 — where many buyers are Russian-speaking, plus fr for
//     the first French-market organizer, 2026-10-01)
//
// Each .tmpl file defines three blocks via Go's html/template "define" syntax:
//
//	{{define "subject"}} ... {{end}}    plain-text subject line
//	{{define "html"}}    ... {{end}}    HTML body
//	{{define "text"}}    ... {{end}}    plain-text fallback body
//
// Locale fallback: an unknown locale falls back to "en". Adding a new
// locale (for example "pt") is a two-file change: drop ticket.pt.tmpl and
// invitation.pt.tmpl into this directory; they are picked up automatically
// at process start because templates are embedded via embed.FS.
//
// The renderer is intentionally a thin layer: it owns only the embedded
// templates and the locale-fallback rules. The worker handler keeps
// responsibility for resolving data, attaching the PDF, and calling the
// SMTP adapter.
package templates

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	htmltemplate "html/template"
	"sort"
	"strings"
	texttemplate "text/template"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/directions"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventnote"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sellerinfo"
)

//go:embed *.tmpl
var templateFS embed.FS

// DefaultLocale is used when the requested locale is empty or not in
// SupportedLocales.
const DefaultLocale = "en"

// SupportedLocales lists the locales that this package ships templates for.
// Order is alphabetical; tests assert this list matches the embedded files.
var SupportedLocales = []string{"cs", "de", "en", "es", "fr", "he", "ru"}

// TemplateKindTicket is the standard paid/free-checkout ticket delivery email.
const TemplateKindTicket = "ticket"

// TemplateKindInvitation is the complimentary invitation email (feature #149).
const TemplateKindInvitation = "invitation"

// TemplateKindChange is sent to a buyer when the organizer moved an event
// session (date/time and/or venue changed). A new ticket PDF is attached; its
// barcode is the same as on the old PDF, so the old one still scans.
const TemplateKindChange = "change"

// TemplateKindCancel is sent to a buyer when the organizer cancelled the
// session. It carries no attachment and makes no promise about money.
const TemplateKindCancel = "cancel"

// TemplateKindRefundBlocked is sent to an organization's owners when a refund
// of an already scanned ticket was refused (owner decision 2026-10-12).
const TemplateKindRefundBlocked = "refund_blocked"

// TemplateKindScanException is sent to an organization's owners when a
// platform superadmin allowed one refund of a ticket that was already
// scanned (owner decision 2026-10-13).
const TemplateKindScanException = "refund_scan_exception"

// TemplateKindSalesAlert is sent to an organization's owners and managers
// when a session sold out (by hand or with its last place), and to its
// owners when the sites did not take a sales state change (package
// salesalert, owner decisions 2026-10-31). Data.SalesAlert says which.
const TemplateKindSalesAlert = "sales_alert"

// TemplateKindSaleSubscribeConfirm asks a buyer who left an address on a
// "coming soon" session to confirm it (double opt-in, package salesubscribe).
// TemplateKindSaleOpen tells a confirmed address that sales opened, with a
// link back to the page it subscribed on. Data.SaleNotice carries the link.
const (
	TemplateKindSaleSubscribeConfirm = "sale_subscribe_confirm"
	TemplateKindSaleOpen             = "sale_open"
)

// TemplateKindCheckoutReminder is the ONE reminder about an unpaid widget
// order while its hold is alive (package cartreminder, spec 51-1). A service
// letter: it names that order only and carries no other offer.
// Data.CartReminder carries the link and the hold's end.
const TemplateKindCheckoutReminder = "checkout_reminder"

// TemplateKindMyTicketsLink carries the one-time "my tickets" link a buyer
// asked for on the organizer's site (package mytickets, spec 62 §7).
// Data.MyTickets carries the link.
const TemplateKindMyTicketsLink = "my_tickets_link"

// TemplateKindLatePayment tells a buyer what happened to a payment that came
// after its order had closed (spec 62 П-15): the money goes back, or the
// organizer returns it. Data.LatePayment carries it. A payment whose places
// were taken again gets the ordinary ticket letter instead.
const TemplateKindLatePayment = "late_payment"

// TemplateKindMarketingConfirm asks a buyer who ticked the marketing consent
// to confirm the address (double opt-in, package marketing). Data.Marketing
// carries the confirmation and the withdraw links.
const TemplateKindMarketingConfirm = "marketing_confirm"

// TemplateKindRefund is sent to a buyer when money for their tickets was
// returned (PAY-07). It states the amount and the usual bank delay and
// promises nothing beyond that.
const TemplateKindRefund = "refund"

// TemplateKindShopOrderReceived is sent to the buyer of a merchandise shop
// order once it is paid (spec 47 section 7): the goods, the totals, where it
// goes, the digital files' pointer and the link to the order status page.
const TemplateKindShopOrderReceived = "shop_order_received"

// TemplateKindShopOrderShipped is sent when the organizer marks a shop order
// shipped (or ready, for a pickup order), with the tracking note.
const TemplateKindShopOrderShipped = "shop_order_shipped"

// TemplateKindShopOrderDelivered is sent when a shop order reaches the
// delivery step "delivered" (spec 61 section 7b.4, V-17): always, at most once
// per order. Same design as shop_order_shipped.
const TemplateKindShopOrderDelivered = "shop_order_delivered"

// TemplateKindShopOrderRefunded is sent when money of a paid shop order went
// back to the buyer (a succeeded refund, partial or full): the amount, how
// much of the order has been returned so far, the items and the shop's
// contact. It promises no date: the bank sets the delay.
const TemplateKindShopOrderRefunded = "shop_order_refunded"

// Shop line kinds (shop_products.kind): ShopLine.Kind carries one of them.
const (
	ShopKindPhysical = "physical"
	ShopKindDigital  = "digital"
	ShopKindService  = "service"
)

// ShopLine is one line of a shop order e-mail, every amount pre-formatted
// ("25.00 ILS").
type ShopLine struct {
	Title     string
	Qty       int
	UnitPrice string
	LineTotal string
	// Kind is ShopKindPhysical, ShopKindDigital or ShopKindService.
	Kind string
}

// ShopOrderData is what the shop order e-mails say; nil for every other kind.
type ShopOrderData struct {
	// OrderNumber is the buyer-facing number (shop_orders.number).
	OrderNumber string
	Items       []ShopLine
	// Digital are the digital lines of Items, printed as the "files" block:
	// each title links to StatusURL, where the download buttons are (the
	// signed links themselves expire, so they never go into a letter).
	Digital []ShopLine
	// Subtotal, Discount, Shipping and Total are what the buyer paid for; an
	// empty one is not printed. Shipping stays empty for an order that needs
	// no shipping (only digital or service lines).
	Subtotal string
	Discount string
	Shipping string
	Total    string
	// Pickup says the buyer collects the order. Address is the delivery
	// address, one line each in the courier form's order; empty for a pickup
	// or an order with nothing to ship.
	Pickup  bool
	Address []string
	// Note is the buyer's own note; TrackingNote the organizer's shipping
	// details (shipped letter only).
	Note         string
	TrackingNote string
	// StatusURL is the storefront's order status page.
	StatusURL string
	// RefundAmount is what THIS refund returned, RefundedTotal what the
	// order has got back in all (both pre-formatted, refunded letter only).
	// RefundFull is true when the whole order has been refunded.
	RefundAmount  string
	RefundedTotal string
	RefundFull    bool
}

// Data is the value passed to every template. Fields are intentionally
// strings (not domain structs) so the template author can ignore nil checks
// and {{with}} blocks handle the "empty means omit" presentation rule.
// ReissueData is the explanation of a letter with replacement tickets.
type ReissueData struct {
	// Typo: the first letter went to a mistyped address (reason
	// sent_to_wrong_address); otherwise the tickets were replaced at the
	// buyer's request.
	Typo bool
	// OrderNumber is the buyer-facing order number, "" when unknown.
	OrderNumber string
}

type Data struct {
	// TicketID is the printable ticket UUID.
	TicketID string
	// RecipientEmail is the address the email is being sent to.
	RecipientEmail string
	// HolderName is the printed ticket holder name; may be empty.
	HolderName string
	// EventName is the human-readable event name (always required).
	EventName string
	// SessionStart is a pre-formatted "YYYY-MM-DD HH:MM (zone)" string in
	// the venue's local timezone, or empty if unknown.
	SessionStart string
	// DoorsOpen is the pre-formatted doors-open time ("19:30") on the venue's
	// wall clock (sessions.doors_open_at, migration 0128); empty prints no line.
	DoorsOpen string
	// VenueName may be empty if the venue is not yet resolved.
	VenueName string
	// Directions are the Google Maps / Waze links of the venue (built by
	// internal/platform/directions). Zero prints no line. The ticket, change
	// and invitation letters read it; the cancel letter does not.
	Directions directions.Links
	// TierName may be empty for GA / untiered tickets.
	TierName string

	// SeatSector / SeatRow / SeatNumber are the denormalized seat
	// coordinates copied from tickets.seat_sector / seat_row / seat_number
	// (SEAT-C3, feature #311). All three are empty for general-admission
	// tickets, in which case the templates omit the Sector / Row / Seat
	// rows entirely via {{with}} guards. All three are populated together
	// for tickets issued from an assigned-seats reservation.
	SeatSector string
	SeatRow    string
	SeatNumber string

	// HumanCode is the SEAT-C4 human-readable credential code in grouped
	// display form ("XXXX-XXXX") — the manual-entry fallback at the gate.
	// Shown in the email body text so the code survives attachment
	// stripping. Empty for legacy credentials without a code, in which
	// case the templates omit the row via {{with}} guards.
	HumanCode string

	// Branding is the organisation branding block applied to the header
	// (logo + display name + website) and footer (legal identification).
	// Required for EU "commercial communications" minimum identification.
	// Callers must populate this with resolved values; the worker handler
	// is responsible for the org-logo / platform-logo fallback.
	Branding Branding

	// Change carries what a session-change / session-cancel e-mail says. It
	// is zero for the ticket and invitation kinds.
	Change ChangeData

	// Accent is the colour of the thin band under the header, a CSS hex
	// value. Render fills Branding.Accent (the organization's
	// branding.primary) and then DefaultAccentColor when it is empty, so a
	// caller never has to.
	Accent string

	// Tickets lists every ticket the one e-mail carries (one letter per
	// order). The ticket e-mail prints this list; when it is empty the
	// template falls back to the single ticket described by TicketID,
	// TierName and the Seat* fields above, which is what the change and
	// cancel e-mails (one ticket each) and older callers use.
	Tickets []TicketLine

	// Alert carries what a refund-blocked notice says (kind
	// "refund_blocked"); zero for every other kind.
	Alert ScanAlertData

	// SalesAlert is what the sales alert says (kind "sales_alert"); zero for
	// every other kind.
	SalesAlert SalesAlertData

	// SaleNotice is what the two subscriber letters say (kinds
	// sale_subscribe_confirm and sale_open); zero for every other kind.
	SaleNotice SaleNoticeData
	// MyTickets is the link of the kind my_tickets_link; zero otherwise.
	MyTickets MyTicketsLinkData

	// CartReminder is what the unpaid-order reminder says (kind
	// checkout_reminder); zero for every other kind.
	CartReminder CartReminderData

	// LatePayment is what the late-payment letter says (kind late_payment);
	// zero for every other kind.
	LatePayment LatePaymentData

	// Marketing is what the marketing consent confirmation says (kind
	// marketing_confirm); zero for every other kind.
	Marketing MarketingConfirmData

	// Refund is what the refund e-mail says; nil for every other kind. The
	// refunded tickets themselves are listed in Tickets, each with the
	// amount returned for it in Price.
	Refund *RefundData

	// Payment is the purchase summary printed as the "Payment" block of the
	// ticket e-mail. nil omits the block: an invitation, a free ticket or an
	// order whose money is not known pays nothing to show.
	Payment *PaymentData

	// EventNote is the organizer's own text of the event (EC-31, migration
	// 0161), printed as the "About the event" block of the ticket and change
	// letters. Build it with eventnote.Build; the zero value prints nothing.
	// The other kinds ignore it.
	EventNote eventnote.Block

	// Reissue explains a letter with replacement tickets (spec 63 section
	// 4.6). Only the "ticket" kind prints it; nil is an ordinary letter.
	Reissue *ReissueData

	// WithdrawalNotice prints the sentence that tickets for an event on a
	// specific date carry no right of withdrawal (EU/EEA sellers, ticket
	// e-mail only). The delivery worker sets it from the seller's country;
	// it stays false for invitations and for any other seller.
	WithdrawalNotice bool

	// WhiteLabel hides the "Arena Platform" line of a buyer letter: the
	// brand of the event (its promoter, else the organization) or of the shop
	// has white label today (spec 08_architecture/53). The letters to the
	// organizer (alerts) never read it.
	WhiteLabel bool

	// Shop is the order the shop e-mails describe (kinds shop_order_received,
	// shop_order_shipped, shop_order_delivered and shop_order_refunded); Render fills an empty one when it is nil.
	Shop *ShopOrderData

	// Referral is what the three "Refer a friend" letters say (kinds
	// referral_cashback_sent, referral_cashback_unavailable and
	// referral_reversal_organizer); Render fills an empty one when it is nil.
	Referral *ReferralData
}

// Kinds of the "Refer a friend" letters (spec 52). The first two go to the
// referrer, who is a buyer (they end with the buyer unsubscribe note); the
// third goes to the organization's owners and carries no note.
const (
	// TemplateKindReferralCashbackSent tells the referrer that the cashback
	// went back to the card they paid with. It promises only the bank's delay.
	TemplateKindReferralCashbackSent = "referral_cashback_sent"
	// TemplateKindReferralCashbackUnavailable tells the referrer that the
	// cashback could not be returned to their card and sends them to the
	// organizer (the same contact rule as the ticket letters).
	TemplateKindReferralCashbackUnavailable = "referral_cashback_unavailable"
	// TemplateKindReferralReversalOrganizer tells an organization's owners
	// that a friend got money back or opened a dispute after the cashback was
	// paid.
	TemplateKindReferralReversalOrganizer = "referral_reversal_organizer"
	// TemplateKindReferralPayoutOrganizer tells an organization's owners about
	// cashback payouts (owner decision 03.11.2026): they wait for the Stripe
	// balance, Stripe holds them pending, or one was not paid
	// (ReferralData.Notice).
	TemplateKindReferralPayoutOrganizer = "referral_payout_organizer"
)

// Payout notice kinds (ReferralData.Notice).
const (
	ReferralNoticeBalance = "balance"
	ReferralNoticePending = "pending"
	ReferralNoticeUnpaid  = "unpaid"
)

// Referral reversal kinds (ReferralData.Reversal).
const (
	ReferralReversalRefunded = "refunded"
	ReferralReversalDisputed = "disputed"
)

// ReferralData is the content of a "Refer a friend" letter; every amount is
// pre-formatted ("5.00 EUR").
type ReferralData struct {
	// Code is the personal code the friend used.
	Code string
	// Amount is the cashback (buyer letters) or the cashback already paid
	// (organizer letter).
	Amount string
	// Order is the referrer's order number in the buyer letters and the
	// friend's order number in the organizer letter (orders.system_id).
	Order string
	// Reversal is ReferralReversalRefunded or ReferralReversalDisputed
	// (organizer letter only).
	Reversal string
	// CodePaused says the code was put on pause (organizer letter only).
	CodePaused bool
	// Notice is ReferralNoticeBalance, ReferralNoticePending or
	// ReferralNoticeUnpaid (payout letter only); Count is how many cashbacks
	// it speaks of and Amount their sum; BalanceTimeout says an unpaid one
	// was never covered by the balance (else the provider refused it).
	Notice         string
	Count          int
	BalanceTimeout bool
}

// ScanAlertData is the content of a refund-blocked notice.
type ScanAlertData struct {
	// TicketNumber is the buyer-facing ticket number; OrderNumber the order's
	// (empty for an invitation).
	TicketNumber string
	OrderNumber  string
	// Actor names who tried (a member's name or e-mail, an API key's name);
	// empty when it cannot be told.
	Actor string
	// Reason and Until belong to a superadmin's exception: why it was
	// granted and until when it is valid, ready to print.
	Reason string
	Until  string
}

// MyTicketsLinkData is the content of the "my tickets" link letter.
type MyTicketsLinkData struct {
	// URL is the page that exchanges the one-time token for a session.
	URL string
}

// SaleNoticeData is the content of a subscriber letter.
type SaleNoticeData struct {
	// URL is the confirmation link, or the page to buy on.
	URL string
	// When is the session start on the venue's clock; Venue its venue.
	When  string
	Venue string
}

// LatePaymentData is the content of the late-payment letter.
type LatePaymentData struct {
	// BuyerCancelled: the buyer cancelled the order before the payment
	// came; otherwise it came after the order had closed.
	BuyerCancelled bool
	// Refunding: arena returns the money itself; otherwise the organizer
	// does and contacts the buyer.
	Refunding bool
	// Total is the amount ready to print, OrderNumber the buyer-facing
	// number, When the session start and Venue the venue.
	Total       string
	OrderNumber string
	When        string
	Venue       string
}

// CartReminderData is the content of the unpaid-order reminder.
type CartReminderData struct {
	// URL brings the buyer back to the payment (arena's resume route).
	URL string
	// When is the session start and HoldUntil the end of the hold, both on
	// the venue's clock; Venue is the venue.
	When      string
	HoldUntil string
	Venue     string
	// Tickets is how many tickets the order holds; Total its amount, ready
	// to print ("37.80 EUR"); OrderNumber the buyer-facing number.
	Tickets     int
	Total       string
	OrderNumber string
}

// MarketingConfirmData is the content of the marketing consent confirmation.
type MarketingConfirmData struct {
	ConfirmURL  string
	WithdrawURL string
	// FromSite is a subscription left on the organization's website form
	// (marketing.SourceSite); false is the consent ticked at checkout, which
	// the letter's first sentence names.
	FromSite bool
}

// SalesAlertData is the content of a sales alert.
type SalesAlertData struct {
	// Kind is "sold_out", "site_stale" or "reissue_old_code"
	// (salesalert.KindSoldOut / KindSiteStale / KindReissueOldCode).
	Kind string
	// Manual: the sell-out was set by hand; Actor names who did it.
	Manual bool
	Actor  string
	// When is the session start on the venue's clock, ready to print; Venue its venue.
	When  string
	Venue string
	// Total and Free count the places of the session.
	Total int64
	Free  int64
	// State is the new state of the change the sites did not take
	// ("open", "soon" or "sold_out"), named by the template.
	State string
	// Kind "reissue_old_code" (spec 63 section 4.8): the old code of a
	// replaced ticket was used before the replacement, or tried after it
	// (Attempt). OrderNumber is 0 for an invitation without an order. At is
	// the moment on the venue's clock; Source is "arena_scanner" or "macs",
	// Gate the entrance, Admitted whether the scanner let the person in.
	Attempt     bool
	OrderNumber int64
	OldNumber   int64
	NewNumber   int64
	At          string
	Source      string
	Gate        string
	Admitted    bool
}

// RefundData is the refund e-mail's own block.
type RefundData struct {
	// Total is everything returned in this letter, pre-formatted ("25.00 EUR").
	Total string
	// TicketsCancelled says EVERY refunded ticket of the letter no longer
	// admits; false for a money-only partial refund.
	TicketsCancelled bool
	// SomeTicketsCancelled says some, but not all, of the letter's tickets no
	// longer admit: each of them carries TicketLine.Void, the others stay
	// valid.
	SomeTicketsCancelled bool
}

// TicketLine is one ticket of a multi-ticket e-mail. Every field is a
// ready-to-print string; empty ones are omitted by the templates.
type TicketLine struct {
	// Number is the buyer-facing ticket number (never a UUID).
	Number string
	// Tier is the category name; may be empty.
	Tier string
	// Seat is the "Sector A · Row 3 · Seat 5" style position, already put
	// together in the buyer's language; empty for general admission.
	Seat string
	// Price is what the buyer paid for this ticket ("25.00 EUR"); empty for
	// a free ticket or when it is not known.
	Price string
	// Void marks a ticket the refund letter reports as no longer valid for
	// entry (its real state is cancelled).
	Void bool
	// DownloadURL is the ticket's own PDF on arena's public route, marked
	// ?src=email so the download is logged as coming from the letter
	// (migration 0184); empty prints no link. The PDF stays attached either
	// way.
	DownloadURL string
}

// PaymentData is the "Payment" block of the ticket e-mail: the numbers of the
// purchase as the buyer was charged, pre-formatted ("25.00 EUR").
type PaymentData struct {
	// OrderNumber is the buyer-facing order reference (orders.system_id).
	OrderNumber string
	// PaidAt is the payment time, formatted in the venue's zone; may be empty.
	PaidAt string
	// Subtotal is the sum of the ticket prices before any discount.
	Subtotal string
	// Discount is the promo-code discount as a positive amount; empty when
	// there was none.
	Discount string
	// ServiceFee is the platform service charge; empty when there was none.
	ServiceFee string
	// Total is what the buyer paid.
	Total string
}

// ChangeData carries what a session-change / session-cancel e-mail says. It is
// zero for the ticket and invitation kinds.
type ChangeData struct {
	// Invitation marks a cancel letter that speaks about ONE staff invitation
	// the organizer withdrew (the session itself goes on): the cancel
	// template then says "your invitation was cancelled" instead of "the
	// session was cancelled".
	Invitation bool
	// Date / Venue say which parts of the session changed (kind "change").
	Date  bool
	Venue bool
	// OldStart / NewStart are pre-formatted "YYYY-MM-DD HH:MM (zone)" strings (venue-local zone).
	OldStart string
	NewStart string
	// OldVenue / NewVenue are venue display names (may be empty).
	OldVenue string
	NewVenue string
	// Message is the organizer's own text as plain text (the text part of the
	// letter prints it; the html engine escapes it where it is used); may be empty.
	Message string
	// MessageHTML is the same message as safe markup for the HTML part
	// (eventnote.BuildMessage: richtext.Render with the session_message
	// profile, inline styles for mail clients). Empty means "print Message as
	// plain text": Render then fills it with the escaped Message.
	MessageHTML htmltemplate.HTML
	// ContactName is the promoter / organization the buyer should write to;
	// ContactPhone is empty when the organizer chose to hide it. ContactEmail
	// is the address the letter prints; when it is empty the letter links
	// ContactFormURL, the hosted page's "Write to the organizer" form
	// (delivery.ContactRoute: a hosted page never publishes the organization's
	// own address).
	ContactName    string
	ContactEmail   string
	ContactFormURL string
	ContactPhone   string
}

// Branding carries the organisation branding fields rendered into the
// header and footer of every transactional email and PDF e-ticket
// (feature #290, T-3).
//
// All fields are pre-resolved strings: the templates and pdf renderers
// perform no media or organisation lookups. The worker handler resolves
// LogoURL via the media adapter (or substitutes the platform fallback
// when the organisation has no logo_media_id).
//
// Legal-identification fields back the EU minimum-identification rule
// for commercial communications and the organizer contact footer. No
// letter carries an unsubscribe: they are all transactional and a pressed
// unsubscribe blocks the buyer's address in Brevo for every later letter
// (templates_no_unsubscribe_test.go).
type Branding struct {
	// OrgName is the public display name printed in the header next to
	// the logo. Empty falls back to PlatformOrgName.
	OrgName string
	// Accent is the organization's brand colour (branding.primary,
	// migration 0203) as "#rrggbb"; empty = the platform accent. Render
	// copies it into Data.Accent when the caller left that empty.
	Accent string
	// WebsiteURL is the organisation website rendered as a footer link.
	// Empty means "omit the link"; the {{with}} guards collapse the row.
	WebsiteURL string
	// LogoURL is a fully-qualified URL (signed media URL or platform
	// fallback) used as the <img src> in the email header. Empty means
	// "render the wordmark only" — the {{with}} guard suppresses the
	// <img>. When LogoURL is empty the worker handler MUST set this to
	// PlatformLogoURL so the header always carries a logo.
	LogoURL string
	// LogoAlt is the accessible alt text for LogoURL. Defaults to OrgName
	// (or PlatformOrgName) when empty.
	LogoAlt string

	// Footer / EU minimum-identification fields.

	// LegalName is the registered juridical name of the organisation: the
	// SELLER, whose provider account took the money. Empty falls back to the
	// organisation's display name (resolveBranding), never to the platform:
	// the platform is not the seller (spec 51 section 2).
	LegalName              string
	LegalAddressLine1      string
	LegalAddressLine2      string
	LegalAddressPostalCode string
	LegalAddressCity       string
	// LegalAddressCountry is ISO-3166-1 alpha-2 (e.g. "DE", "IL").
	LegalAddressCountry string
	// ContactEmail is the contact address printed in the footer. Empty
	// prints no contact line. It is the public e-mail of the sales page the
	// ticket was sold through when it has one; for a page WITHOUT a hosted
	// page, the organization's contact_email (HP-4b). A hosted page without
	// a public e-mail never prints the organization's address: ContactFormURL
	// is printed instead (delivery.ContactRoute).
	ContactEmail string
	// ContactFormURL is the hosted page's "Write to the organizer" link,
	// printed in the footer only when ContactEmail is empty.
	ContactFormURL string
	// RegistrationNumber, TaxID and TaxIDScheme identify the seller (HP-4b).
	RegistrationNumber string
	TaxID              string
	TaxIDScheme        string
	// SellerIDs is the footer line built from the three fields above in the
	// letter's language ("Registration no. 123 · VAT ID CZ123"). Render
	// fills it; empty prints no line.
	SellerIDs string
	// PaymentProvider is the human name of the payment provider the buyer's
	// money went through ("Stripe"), empty when the letter belongs to no
	// payment (an invitation, a free order, an order paid on a selling site).
	PaymentProvider string
	// SellerLabel ("Seller:") and PaymentLine ("Payment processed by
	// Stripe") are the footer words in the letter's language (spec 51
	// section 2: the letter names the organization that received the money
	// as the seller, and the provider). Render fills them; a letter without
	// PaymentProvider prints no payment line.
	SellerLabel string
	PaymentLine string
}

// sellerFooterWords are the footer words of every buyer letter, by locale.
// The payment line takes the provider's name.
var sellerFooterWords = map[string]struct{ seller, payment string }{
	"en": {"Seller:", "Payment processed by %s"},
	"ru": {"Продавец:", "Оплата проведена через %s"},
	"cs": {"Prodávající:", "Platbu zpracovává %s"},
	"de": {"Verkäufer:", "Zahlung abgewickelt über %s"},
	"es": {"Vendedor:", "Pago procesado por %s"},
	"fr": {"Vendeur :", "Paiement traité par %s"},
	"he": {"המוכר:", "התשלום בוצע באמצעות %s"},
}

// fillSellerFooter sets the seller label and the payment line in locale.
func fillSellerFooter(b *Branding, locale string) {
	words, ok := sellerFooterWords[locale]
	if !ok {
		words = sellerFooterWords["en"]
	}
	if b.SellerLabel == "" {
		b.SellerLabel = words.seller
	}
	if p := strings.TrimSpace(b.PaymentProvider); p != "" && b.PaymentLine == "" {
		b.PaymentLine = fmt.Sprintf(words.payment, p)
	}
}

// Platform defaults used by the worker handler when the organisation
// itself has no branding fields (logo_media_id IS NULL, legal_name IS
// NULL, etc.). The templates themselves never substitute these
// values — the renderer prints exactly what it is given.
const (
	PlatformOrgName = "Arena Platform"
	// PlatformLogoURL is empty on purpose: there is no hosted platform logo,
	// and a placeholder address (it used to be assets.arena.example.com)
	// made Gmail show a broken picture on every e-mail of an organization
	// without a logo. Empty makes the {{with .Branding.LogoURL}} guards drop
	// the <img>, and the header shows the coloured band instead.
	PlatformLogoURL   = ""
	PlatformLegalName = "Arena Platform"
	// PlatformContactEmail is a placeholder address for test fixtures only.
	// No code path substitutes it for an organization without a contact.
	PlatformContactEmail = "support@arena.example.com"
)

// DefaultAccentColor is the Arena Sold Out indigo — the same value as the PDF
// ticket's accent (pdf.DefaultAccentColor) so a letter and its attachment
// carry one colour.
const DefaultAccentColor = "#4f46e5"

// Rendered is the output of a single Render call: the three pieces of the
// email body that the SMTP adapter needs.
type Rendered struct {
	Subject  string
	HTMLBody string
	TextBody string
}

// Renderer holds parsed templates keyed by "<kind>.<locale>". It is safe
// for concurrent use after construction.
//
// Each (kind, locale) pair gets its own *html.Template and *text.Template
// pair so that the "subject"/"html"/"text" blocks defined inside one file
// do not overwrite the same-named blocks in another file (Go templates
// merge define-blocks into a shared namespace inside a single root).
type Renderer struct {
	html map[string]*htmltemplate.Template
	text map[string]*texttemplate.Template
	// known is the set of "<kind>.<locale>" pairs that were actually
	// parsed. Used by ResolveLocale to fall back when a locale is missing.
	known map[string]struct{}
}

// New parses every embedded template. Returns an error only if a file is
// malformed — in normal operation, the templates have been validated at
// build time so this never fails in production.
//
// HTML templates are parsed with html/template (auto-escaping). The
// text-body and subject use text/template (no escaping — they are
// already plain text).
func New() (*Renderer, error) {
	entries, err := templateFS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("templates: read embed.FS: %w", err)
	}

	htmlByKey := make(map[string]*htmltemplate.Template)
	textByKey := make(map[string]*texttemplate.Template)
	known := make(map[string]struct{})

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tmpl") {
			continue
		}
		body, err := templateFS.ReadFile(e.Name())
		if err != nil {
			return nil, fmt.Errorf("templates: read %s: %w", e.Name(), err)
		}
		key := strings.TrimSuffix(e.Name(), ".tmpl") // "ticket.en"

		// Fresh per-file template set so define-blocks do not collide
		// across files.
		htmlT := htmltemplate.New(key)
		if _, err := htmlT.Parse(string(body)); err != nil {
			return nil, fmt.Errorf("templates: parse html %s: %w", e.Name(), err)
		}
		textT := texttemplate.New(key)
		if _, err := textT.Parse(string(body)); err != nil {
			return nil, fmt.Errorf("templates: parse text %s: %w", e.Name(), err)
		}

		// Make sure every file declares all three blocks.
		for _, block := range []string{"subject", "html", "text"} {
			if htmlT.Lookup(block) == nil {
				return nil, fmt.Errorf(
					"templates: %s missing required block %q", e.Name(), block,
				)
			}
		}

		htmlByKey[key] = htmlT
		textByKey[key] = textT
		known[key] = struct{}{}
	}

	if len(known) == 0 {
		return nil, fmt.Errorf("templates: no .tmpl files found in embed.FS")
	}

	return &Renderer{html: htmlByKey, text: textByKey, known: known}, nil
}

// Version names the exact template file a (kind, locale) letter is rendered
// from: "<kind>.<resolved locale>@<first 12 hex of the file's SHA-256>". It
// is stored with every sent letter (email_letters.template_version) so the
// wording a buyer received can be proven after the templates change. Empty
// when the template does not exist.
func (r *Renderer) Version(kind, locale string) string {
	key := kind + "." + r.ResolveLocale(kind, locale)
	if _, ok := r.known[key]; !ok {
		return ""
	}
	body, err := templateFS.ReadFile(key + ".tmpl")
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return key + "@" + hex.EncodeToString(sum[:])[:12]
}

// NoteVersionSuffix is appended to a template version when the organizer's own
// event text (Data.EventNote) is part of the letter. The letter's body then
// differs from every other letter of the same template file, and the stored
// version must say so (plan 3a, rule 5).
const NoteVersionSuffix = "+note"

// VersionFor is Version plus NoteVersionSuffix when the letter carries an
// event note. Empty when the template does not exist.
func (r *Renderer) VersionFor(kind, locale string, hasNote bool) string {
	v := r.Version(kind, locale)
	if v != "" && hasNote {
		v += NoteVersionSuffix
	}
	return v
}

// ResolveLocale returns locale when a template for it exists, or
// DefaultLocale otherwise. kind is required because, in theory, a new
// kind could be added without yet shipping every locale for it.
func (r *Renderer) ResolveLocale(kind, locale string) string {
	locale = normalize(locale)
	if locale != "" {
		if _, ok := r.known[kind+"."+locale]; ok {
			return locale
		}
	}
	return DefaultLocale
}

// Render renders the three template blocks for (kind, locale).
// Falls back to DefaultLocale when locale is missing. Returns an error
// only if kind is unknown or the templates fail to execute.
func (r *Renderer) Render(kind, locale string, data Data) (Rendered, error) {
	if strings.TrimSpace(data.Accent) == "" {
		data.Accent = strings.TrimSpace(data.Branding.Accent)
	}
	if strings.TrimSpace(data.Accent) == "" {
		data.Accent = DefaultAccentColor
	}
	if data.Change.MessageHTML == "" && data.Change.Message != "" {
		data.Change.MessageHTML = htmltemplate.HTML(`<p style="margin:0;white-space:pre-line">` + htmltemplate.HTMLEscapeString(data.Change.Message) + `</p>`) //nolint:gosec // escaped
	}
	if data.Shop == nil {
		data.Shop = &ShopOrderData{}
	}
	if data.Referral == nil {
		data.Referral = &ReferralData{}
	}
	resolved := r.ResolveLocale(kind, locale)
	key := kind + "." + resolved
	if data.Branding.SellerIDs == "" {
		data.Branding.SellerIDs = sellerinfo.IDsLine(resolved, data.Branding.RegistrationNumber, data.Branding.TaxID, data.Branding.TaxIDScheme)
	}
	fillSellerFooter(&data.Branding, resolved)

	if _, ok := r.known[key]; !ok {
		return Rendered{}, fmt.Errorf(
			"templates: unknown template kind %q (locale %q)", kind, locale,
		)
	}

	// Subject and text body come from the text engine (no HTML escaping).
	subject, err := r.execText(key, "subject", data)
	if err != nil {
		return Rendered{}, fmt.Errorf("templates: render subject: %w", err)
	}
	textBody, err := r.execText(key, "text", data)
	if err != nil {
		return Rendered{}, fmt.Errorf("templates: render text: %w", err)
	}

	// HTML body comes from the html engine (auto-escaped).
	htmlBody, err := r.execHTML(key, "html", data)
	if err != nil {
		return Rendered{}, fmt.Errorf("templates: render html: %w", err)
	}

	return Rendered{
		Subject:  strings.TrimSpace(subject),
		HTMLBody: htmlBody,
		TextBody: textBody,
	}, nil
}

func (r *Renderer) execText(key, block string, data Data) (string, error) {
	t, ok := r.text[key]
	if !ok {
		return "", fmt.Errorf("missing text template %q", key)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (r *Renderer) execHTML(key, block string, data Data) (string, error) {
	t, ok := r.html[key]
	if !ok {
		return "", fmt.Errorf("missing html template %q", key)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// KnownTemplates returns the sorted list of "<kind>.<locale>" pairs that
// were parsed at construction. Useful for diagnostics and tests.
func (r *Renderer) KnownTemplates() []string {
	out := make([]string, 0, len(r.known))
	for k := range r.known {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normalize lowercases the locale and reduces "en-US" → "en".
func normalize(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if dash := strings.IndexByte(locale, '-'); dash > 0 {
		locale = locale[:dash]
	}
	return locale
}
