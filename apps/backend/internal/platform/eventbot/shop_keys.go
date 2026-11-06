package eventbot

// shopKeys lists every bot.sh.* key of the "Shop" section (spec 61 section 7:
// the screen, the orders and their actions, the refund and the settling of an
// order in review, the storefront switch and the settings). The locale test
// checks them in every supported catalog together with MessageKeys.
var shopKeys = []string{
	"bot.sh.btn", "bot.sh.gone", "bot.sh.home_head", "bot.sh.storefront_open", "bot.sh.storefront_closed",
	"bot.sh.orders_only", "bot.sh.orders_only_only", "bot.sh.home_counts", "bot.sh.orders_btn",
	"bot.sh.open_btn", "bot.sh.close_btn", "bot.sh.settings_btn", "bot.sh.back_shop_btn", "bot.sh.edit_web_btn",
	"bot.sh.open_ask", "bot.sh.open_yes_btn", "bot.sh.close_ask", "bot.sh.close_yes_btn", "bot.sh.opened_done",
	"bot.sh.closed_done", "bot.sh.cannot_open", "bot.sh.blocker_settings_not_saved", "bot.sh.blocker_no_active_product",
	"bot.sh.blocker_payment_not_configured", "bot.sh.blocker_other", "bot.sh.cannot_open_hint",
	"bot.sh.settings_text", "bot.sh.none", "bot.sh.channel_default", "bot.sh.tab_to_ship", "bot.sh.tab_review",
	"bot.sh.tab_unpaid", "bot.sh.tab_all", "bot.sh.list_title", "bot.sh.list_total", "bot.sh.search_line",
	"bot.sh.search_none", "bot.sh.empty_to_ship", "bot.sh.empty_review", "bot.sh.empty_unpaid",
	"bot.sh.empty_all", "bot.sh.list_hint", "bot.sh.card_head", "bot.sh.st_pending_payment", "bot.sh.st_paid",
	"bot.sh.st_shipped", "bot.sh.st_cancelled", "bot.sh.st_expired", "bot.sh.st_refunded", "bot.sh.st_partially_refunded",
	"bot.sh.st_manual_review", "bot.sh.card_review_late", "bot.sh.card_review_unconfirmed", "bot.sh.card_review_other",
	"bot.sh.card_buyer", "bot.sh.card_delivery", "bot.sh.delivery_none", "bot.sh.delivery_pickup",
	"bot.sh.delivery_hidden", "bot.sh.card_comment", "bot.sh.card_items", "bot.sh.line_discount",
	"bot.sh.line_shipping", "bot.sh.line_total", "bot.sh.line_refunded", "bot.sh.card_payment",
	"bot.sh.card_shipped", "bot.sh.card_tracking", "bot.sh.card_note", "bot.sh.ship_btn", "bot.sh.track_btn",
	"bot.sh.courier_btn", "bot.sh.note_btn", "bot.sh.resend_btn", "bot.sh.refund_btn", "bot.sh.fulfil_btn",
	"bot.sh.refund_cancel_btn", "bot.sh.cancel_btn", "bot.sh.to_list_btn", "bot.sh.ship_ask", "bot.sh.no_number_btn",
	"bot.sh.ship_done", "bot.sh.note_ask", "bot.sh.note_current", "bot.sh.note_erase_btn", "bot.sh.note_saved",
	"bot.sh.note_erased", "bot.sh.resend_ask", "bot.sh.letter_received", "bot.sh.letter_shipped",
	"bot.sh.letter_refunded", "bot.sh.resend_done", "bot.sh.courier_head", "bot.sh.courier_empty",
	"bot.sh.courier_private", "bot.sh.cancel_word", "bot.sh.cancel_ask", "bot.sh.cancel_wrong",
	"bot.sh.cancel_done", "bot.sh.cancel_changed", "bot.sh.refund_word", "bot.sh.fulfil_word",
	"bot.sh.refund_amount", "bot.sh.refund_all_btn", "bot.sh.refund_part_btn", "bot.sh.refund_cancel_dialog_btn",
	"bot.sh.refund_amount_text", "bot.sh.refund_amount_bad", "bot.sh.refund_amount_changed", "bot.sh.refund_reason",
	"bot.sh.refund_no_reason_btn", "bot.sh.refund_reason_line", "bot.sh.refund_confirm", "bot.sh.fulfil_confirm",
	"bot.sh.refund_cancel_confirm", "bot.sh.word_wrong", "bot.sh.refund_retry", "bot.sh.refund_again_btn",
	"bot.sh.refund_done", "bot.sh.refund_pending", "bot.sh.refund_review", "bot.sh.refund_failed",
	"bot.sh.fulfil_done", "bot.sh.refund_cancel_done", "bot.sh.refund_cancel_pending", "bot.sh.refund_cancel_review",
	"bot.sh.refund_cancel_failed", "bot.sh.refund_nothing", "bot.sh.other_attempt", "bot.sh.menu_back_btn",
	"bot.sh.menu_leave_refund", "bot.sh.menu_leave_maybe", "bot.sh.err_not_refundable", "bot.sh.err_out_of_stock",
	"bot.sh.err_payment_unconfirmed", "bot.sh.err_not_resolvable", "bot.sh.err_not_in_review",
	"bot.sh.err_money_failed", "bot.sh.err_changed", "bot.sh.err_postal_required", "bot.sh.err_invalid_postal",
	"bot.sh.err_too_long", "bot.sh.err_rate_limited", "bot.sh.err_no_email", "bot.sh.err_letter_unavailable",
	"bot.sh.err_unavailable", "bot.sh.tab_failed", "bot.sh.empty_failed", "bot.sh.letter_delivered",
	"bot.sh.card_step", "bot.sh.step_line", "bot.sh.step_track", "bot.sh.step_reason", "bot.sh.step_waiting",
	"bot.sh.step_packed", "bot.sh.step_handed_over", "bot.sh.step_delivered", "bot.sh.step_failed",
	"bot.sh.reason_no_answer", "bot.sh.reason_not_home", "bot.sh.reason_wrong_address", "bot.sh.reason_refused",
	"bot.sh.reason_other", "bot.sh.packed_btn", "bot.sh.delivered_btn", "bot.sh.failed_btn", "bot.sh.failed_ask",
	"bot.sh.reason_text_ask", "bot.sh.step_done", "bot.sh.err_closed", "bot.sh.err_unpaid", "bot.sh.err_in_review",
	"bot.sh.err_reason_long",
}

func init() {
	MessageKeys = append(MessageKeys, shopKeys...)
}
