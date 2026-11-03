package eventbot

// smartPriceKeys lists every bot.sp.* key of the smart price screen
// (smart_price.go). The locale test checks them in every supported catalog
// together with MessageKeys.
var smartPriceKeys = []string{
	"bot.sp.btn", "bot.sp.title", "bot.sp.intro", "bot.sp.kind_by_sales_btn",
	"bot.sp.kind_by_days_btn", "bot.sp.kind_last_days_btn", "bot.sp.rule_by_sales", "bot.sp.rule_by_days",
	"bot.sp.rule_last_days", "bot.sp.active", "bot.sp.now", "bot.sp.now_last",
	"bot.sp.now_left", "bot.sp.now_at", "bot.sp.stop_btn", "bot.sp.stop_confirm",
	"bot.sp.stop_yes_btn", "bot.sp.stopped", "bot.sp.in_chain", "bot.sp.ended_stopped",
	"bot.sp.ended_overridden", "bot.sp.preview_head", "bot.sp.row_now", "bot.sp.row_tickets",
	"bot.sp.row_rest", "bot.sp.row_from", "bot.sp.preview_note", "bot.sp.enable_btn",
	"bot.sp.nums_btn", "bot.sp.enabled", "bot.sp.q_nums_by_sales", "bot.sp.q_nums_by_days",
	"bot.sp.q_nums_last_days", "bot.sp.err_nums", "bot.sp.err_input", "bot.sp.err_on", "bot.sp.err_changed",
	"bot.sp.err_schedule", "bot.sp.err_places", "bot.sp.err_no_room", "bot.sp.err_too_late",
	"bot.sp.err_seated", "bot.sp.err_free", "bot.sp.err_off", "bot.sp.price_warn",
}

func init() {
	MessageKeys = append(MessageKeys, smartPriceKeys...)
}
