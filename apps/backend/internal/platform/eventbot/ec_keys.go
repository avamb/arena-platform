package eventbot

// ecKeys lists every bot.ec.* key of the event-center screens of spec 35 stage 1
// (events list, event card, summaries, CSV export, notifications). The locale
// test checks them in every supported catalog together with MessageKeys.
var ecKeys = []string{
	"bot.ec.filter_run", "bot.ec.filter_arc", "bot.ec.list_title", "bot.ec.legend", "bot.ec.search_line",
	"bot.ec.search_none", "bot.ec.empty_run", "bot.ec.empty_arc", "bot.ec.clear_search_btn",
	"bot.ec.tot_places", "bot.ec.refunds_line", "bot.ec.entered_line", "bot.ec.comp_line", "bot.ec.promos_head",
	"bot.ec.promo_line", "bot.ec.more", "bot.ec.tiers_head", "bot.ec.tier_line", "bot.ec.tier_closed",
	"bot.ec.all_dates", "bot.ec.next_line", "bot.ec.when_line", "bot.ec.summary_btn", "bot.ec.csv_btn",
	"bot.ec.summary_csv_btn", "bot.ec.dates_btn", "bot.ec.dates_hide_btn", "bot.ec.sum_title",
	"bot.ec.sum_tickets", "bot.ec.sum_orders", "bot.ec.sum_places", "bot.ec.sum_money", "bot.ec.scope_all",
	"bot.ec.csv_caption", "bot.ec.csv_what_sales", "bot.ec.csv_what_summary", "bot.ec.csv_empty",
	"bot.ec.csv_failed", "bot.ec.csv_too_big_event", "bot.ec.csv_too_big_session", "bot.ec.not_found",
	"bot.ec.no_rights", "bot.ec.notif_btn", "bot.ec.notif_open_btn", "bot.ec.notif_owner",
	"bot.ec.notif_manager",
}

func init() {
	MessageKeys = append(MessageKeys, ecKeys...)
}
