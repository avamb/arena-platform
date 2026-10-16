package eventbot

// ordKeys lists every bot.ord.* key of the Orders screens (spec 35 EC-04 and
// EC-05: the list with its tabs and search, the order card, cancelling an
// unpaid order). The locale test checks them in every supported catalog
// together with MessageKeys.
var ordKeys = []string{
	"bot.ord.btn", "bot.ord.list_title", "bot.ord.legend", "bot.ord.tab_recent", "bot.ord.tab_paid",
	"bot.ord.tab_unpaid", "bot.ord.scope_line", "bot.ord.scope_all_btn", "bot.ord.empty_recent",
	"bot.ord.empty_paid", "bot.ord.empty_unpaid", "bot.ord.search_none",
	"bot.ord.st_paid", "bot.ord.st_pending_payment", "bot.ord.st_expired", "bot.ord.st_cancelled",
	"bot.ord.st_partially_refunded", "bot.ord.st_refunded", "bot.ord.st_manual_review",
	"bot.ord.card_head", "bot.ord.card_event", "bot.ord.card_buyer", "bot.ord.buyer_unknown",
	"bot.ord.contacts_private", "bot.ord.phone_call", "bot.ord.phone_wa",
	"bot.ord.tickets_head", "bot.ord.tickets_more", "bot.ord.no_tickets", "bot.ord.ticket_line",
	"bot.ord.seat", "bot.ord.entered", "bot.ord.tk_active", "bot.ord.tk_cancelled", "bot.ord.tk_transferred",
	"bot.ord.channel_line", "bot.ord.channel_hosted_page", "bot.ord.channel_widget", "bot.ord.channel_site",
	"bot.ord.channel_other", "bot.ord.payment_line",
	"bot.ord.letter_tickets_none", "bot.ord.letter_tickets_queued", "bot.ord.letter_tickets_partial_queued",
	"bot.ord.letter_tickets_sent", "bot.ord.letter_tickets_delivered", "bot.ord.letter_tickets_opened",
	"bot.ord.letter_tickets_partial", "bot.ord.letter_tickets_failed",
	"bot.ord.letter_tickets_delayed", "bot.ord.letter_tickets_stuck", "bot.ord.letter_tickets_bounced",
	"bot.ord.letter_refund_delayed", "bot.ord.letter_refund_stuck", "bot.ord.letter_refund_bounced",
	"bot.ord.letter_refund_off", "bot.ord.letter_refund_queued", "bot.ord.letter_refund_partial_queued",
	"bot.ord.letter_refund_sent", "bot.ord.letter_refund_delivered", "bot.ord.letter_refund_opened",
	"bot.ord.letter_refund_failed", "bot.ord.letter_retry_btn",
	"bot.ord.reason_payment_failed", "bot.ord.reason_payment_abandoned", "bot.ord.reason_hold_expired",
	"bot.ord.reason_cancelled", "bot.ord.reason_awaiting_payment", "bot.ord.reason_manual_review",
	"bot.ord.reason_unknown", "bot.ord.reason_provider",
	"bot.ord.cancel_btn", "bot.ord.cancel_word", "bot.ord.cancel_ask", "bot.ord.cancel_wrong", "bot.ord.cancel_done",
	"bot.ord.cancel_not_possible",
	"bot.ord.vip_mark", "bot.ord.vip_on_btn", "bot.ord.vip_off_btn",
}

func init() {
	MessageKeys = append(MessageKeys, ordKeys...)
}
