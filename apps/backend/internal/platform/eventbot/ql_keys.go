package eventbot

// qlKeys lists every bot.ql.* key of the "Links and QR" dialog (spec 43, QL-8:
// the target choice, the placement, the card, the statistics and the
// retarget). The locale test checks them in every supported catalog together
// with MessageKeys.
var qlKeys = []string{
	"bot.ql.menu_btn", "bot.ql.btn", "bot.ql.cancel_btn", "bot.ql.cancelled", "bot.ql.target_q",
	"bot.ql.target_q_event", "bot.ql.t_event", "bot.ql.t_session", "bot.ql.t_pages", "bot.ql.t_other_event",
	"bot.ql.t_pick_event", "bot.ql.t_url", "bot.ql.events_q", "bot.ql.events_none", "bot.ql.sessions_q",
	"bot.ql.sessions_none", "bot.ql.url_q", "bot.ql.err_url", "bot.ql.err_url_long", "bot.ql.err_url_refused", "bot.ql.err_target",
	"bot.ql.tgt_event", "bot.ql.tgt_session", "bot.ql.tgt_page", "bot.ql.tgt_url", "bot.ql.placement_q",
	"bot.ql.pl_poster", "bot.ql.pl_flyer", "bot.ql.pl_sign", "bot.ql.pl_social", "bot.ql.pl_email",
	"bot.ql.pl_messenger", "bot.ql.pl_other", "bot.ql.change_target_btn", "bot.ql.label_q",
	"bot.ql.err_label_empty", "bot.ql.err_label_long", "bot.ql.created", "bot.ql.qr_caption",
	"bot.ql.svg_caption", "bot.ql.qr_failed", "bot.ql.card_head", "bot.ql.card_short", "bot.ql.card_dest",
	"bot.ql.card_no_dest", "bot.ql.card_degraded", "bot.ql.card_archived", "bot.ql.card_counts",
	"bot.ql.stats_btn", "bot.ql.retarget_btn", "bot.ql.archive_btn", "bot.ql.unarchive_btn",
	"bot.ql.qr_again_btn", "bot.ql.stats_head", "bot.ql.stats_scans", "bot.ql.stats_utc", "bot.ql.stats_bots",
	"bot.ql.stats_orders", "bot.ql.stats_revenue", "bot.ql.stats_no_revenue", "bot.ql.rt_head",
	"bot.ql.rt_archived", "bot.ql.rt_confirm", "bot.ql.rt_yes_btn", "bot.ql.rt_done", "bot.ql.archived_done",
	"bot.ql.unarchived_done", "bot.ql.gone",
}

func init() {
	MessageKeys = append(MessageKeys, qlKeys...)
}
