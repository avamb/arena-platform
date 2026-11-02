package eventbot

// referralKeys lists every bot.rfp.* key of the "Refer a friend" screens (spec
// 52: the programme card, the turn-on dialog, the end question and the cashback
// list). The locale test checks them in every supported catalog together with
// MessageKeys.
var referralKeys = []string{
	"bot.rfp.btn", "bot.rfp.events_btn", "bot.rfp.gone", "bot.rfp.card_head", "bot.rfp.none",
	"bot.rfp.st_active", "bot.rfp.st_paused", "bot.rfp.st_ended", "bot.rfp.settings",
	"bot.rfp.max_none", "bot.rfp.sum", "bot.rfp.sum_none", "bot.rfp.m_discount", "bot.rfp.m_paid",
	"bot.rfp.m_way", "bot.rfp.m_kept", "bot.rfp.sum_reversals", "bot.rfp.cap_no",
	"bot.rfp.why_seller_site", "bot.rfp.why_provider_unsupported", "bot.rfp.why_no_partial_refund",
	"bot.rfp.warn_no_channel", "bot.rfp.warn_some_channels_unsupported", "bot.rfp.on_btn",
	"bot.rfp.change_btn", "bot.rfp.pause_btn", "bot.rfp.resume_btn", "bot.rfp.rewards_btn",
	"bot.rfp.end_btn", "bot.rfp.paused_done", "bot.rfp.resumed_done", "bot.rfp.ended_done",
	"bot.rfp.on_done", "bot.rfp.saved_done", "bot.rfp.cancelled", "bot.rfp.end_ask",
	"bot.rfp.end_yes_btn", "bot.rfp.end_no_btn", "bot.rfp.d_head_new", "bot.rfp.d_head_edit",
	"bot.rfp.q_friend", "bot.rfp.q_cashback", "bot.rfp.q_cap", "bot.rfp.q_max", "bot.rfp.keep_btn",
	"bot.rfp.cap100_btn", "bot.rfp.nomax_btn", "bot.rfp.cancel_btn", "bot.rfp.rules",
	"bot.rfp.edit_confirm", "bot.rfp.go_btn", "bot.rfp.save_btn", "bot.rfp.err_percent",
	"bot.rfp.err_friends", "bot.rfp.use_buttons", "bot.rfp.err_provider", "bot.rfp.err_terms",
	"bot.rfp.err_value", "bot.rfp.err_ended", "bot.rfp.f_all", "bot.rfp.rs_due", "bot.rfp.rs_sent",
	"bot.rfp.rs_paid", "bot.rfp.rs_capped", "bot.rfp.rs_not_possible", "bot.rfp.rs_failed",
	"bot.rfp.rs_cancelled", "bot.rfp.rw_head", "bot.rfp.rw_empty", "bot.rfp.rw_row",
	"bot.rfp.rw_reversal", "bot.rfp.why_r_cap_reached", "bot.rfp.why_r_self_referral",
	"bot.rfp.why_r_currency_mismatch", "bot.rfp.why_r_card_unavailable",
	"bot.rfp.why_r_friend_refunded", "bot.rfp.why_r_friend_disputed", "bot.rfp.why_r_friend_not_paid",
	"bot.rfp.why_r_no_refundable_ticket",
}

func init() {
	MessageKeys = append(MessageKeys, referralKeys...)
}
