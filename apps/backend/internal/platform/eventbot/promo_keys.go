package eventbot

// promoKeys lists every bot.promo.* key of the promo-code screens (spec 35
// EC-11: the list, the card, the new-code dialog, the sessions edit, the usage
// list and the delete question). The locale test checks them in every
// supported catalog together with MessageKeys.
var promoKeys = []string{
	"bot.promo.btn", "bot.promo.list_title", "bot.promo.legend", "bot.promo.scope_line",
	"bot.promo.list_all_btn", "bot.promo.empty", "bot.promo.empty_event", "bot.promo.new_btn",
	"bot.promo.st_active", "bot.promo.st_paused", "bot.promo.st_expired", "bot.promo.st_exhausted",
	"bot.promo.st_scheduled", "bot.promo.discount_pct", "bot.promo.discount_fixed",
	"bot.promo.sess_all", "bot.promo.sess_n", "bot.promo.lim_total", "bot.promo.lim_per",
	"bot.promo.lim_none", "bot.promo.valid_from", "bot.promo.valid_until", "bot.promo.valid_none",
	"bot.promo.usage_none", "bot.promo.usage", "bot.promo.usage_mixed", "bot.promo.card_discount",
	"bot.promo.card_rule", "bot.promo.card_sessions", "bot.promo.card_sess_line",
	"bot.promo.card_sess_more", "bot.promo.card_limits", "bot.promo.card_valid",
	"bot.promo.card_usage", "bot.promo.card_state", "bot.promo.only_currency", "bot.promo.pause_btn",
	"bot.promo.activate_btn", "bot.promo.sessions_btn", "bot.promo.usage_btn", "bot.promo.csv_btn",
	"bot.promo.delete_btn", "bot.promo.paused_done", "bot.promo.activated_done",
	"bot.promo.sess_head", "bot.promo.sess_all_btn", "bot.promo.sess_only_btn",
	"bot.promo.sess_all_done", "bot.promo.sess_saved", "bot.promo.e_head", "bot.promo.q_event_edit",
	"bot.promo.pick_edit_note", "bot.promo.usage_title", "bot.promo.usage_empty",
	"bot.promo.usage_row", "bot.promo.buyer_unknown", "bot.promo.csv_empty", "bot.promo.csv_scope",
	"bot.promo.csv_what", "bot.promo.csv_too_big", "bot.promo.delete_ask", "bot.promo.delete_wrong",
	"bot.promo.delete_word", "bot.promo.deleted_done", "bot.promo.gone", "bot.promo.cancelled",
	"bot.promo.created", "bot.promo.use_buttons", "bot.promo.c_head", "bot.promo.q_code",
	"bot.promo.q_type", "bot.promo.type_pct_btn", "bot.promo.type_fix_btn", "bot.promo.q_percent",
	"bot.promo.q_amount", "bot.promo.q_currency", "bot.promo.q_scope", "bot.promo.q_scope_event",
	"bot.promo.scope_all_btn", "bot.promo.scope_pick_btn", "bot.promo.q_event", "bot.promo.q_pick",
	"bot.promo.pick_done_btn", "bot.promo.pick_event_btn", "bot.promo.q_total", "bot.promo.q_per",
	"bot.promo.skip_btn", "bot.promo.skip_exp_btn", "bot.promo.q_expiry", "bot.promo.q_status",
	"bot.promo.status_active_btn", "bot.promo.status_paused_btn", "bot.promo.summary",
	"bot.promo.create_btn", "bot.promo.cancel_btn", "bot.promo.err_code_empty",
	"bot.promo.err_code_long", "bot.promo.err_code_chars", "bot.promo.err_code_dup",
	"bot.promo.err_percent", "bot.promo.err_amount", "bot.promo.err_currency", "bot.promo.err_limit",
	"bot.promo.err_per_gt_total", "bot.promo.err_pick_none", "bot.promo.err_session_currency",
	"bot.promo.err_session_foreign", "bot.promo.err_duplicate", "bot.promo.err_no_events",
	"bot.promo.err_no_sessions", "bot.promo.err_incomplete",
}

func init() {
	MessageKeys = append(MessageKeys, promoKeys...)
}
