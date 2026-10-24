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
	// why the code does not lead to its target (QL-8b, the server's destination_reason)
	"bot.ql.card_why_event_unpublished", "bot.ql.card_why_event_no_page", "bot.ql.card_why_session",
	"bot.ql.card_why_promoter", "bot.ql.card_why_no_address", "bot.ql.rt_same", "bot.ql.rt_done_same_dest",
	"bot.ql.rt_done_no_dest",
	"bot.ql.stats_btn", "bot.ql.retarget_btn", "bot.ql.archive_btn", "bot.ql.unarchive_btn",
	"bot.ql.qr_again_btn", "bot.ql.stats_head", "bot.ql.stats_scans", "bot.ql.stats_utc", "bot.ql.stats_bots",
	"bot.ql.stats_orders", "bot.ql.stats_revenue", "bot.ql.stats_no_revenue", "bot.ql.rt_head",
	"bot.ql.rt_archived", "bot.ql.rt_confirm", "bot.ql.rt_yes_btn", "bot.ql.rt_done", "bot.ql.archived_done",
	"bot.ql.unarchived_done", "bot.ql.gone",
	// the list of links (QL-9)
	"bot.ql.list_head", "bot.ql.list_event", "bot.ql.list_hint", "bot.ql.list_row", "bot.ql.list_mark_archived",
	"bot.ql.list_empty", "bot.ql.list_empty_archived", "bot.ql.list_empty_event", "bot.ql.w_event",
	"bot.ql.w_session", "bot.ql.w_promoter", "bot.ql.w_org", "bot.ql.w_url", "bot.ql.f_active",
	"bot.ql.f_archived", "bot.ql.f_all", "bot.ql.list_new_btn", "bot.ql.list_pick_btn",
	"bot.ql.list_all_events_btn", "bot.ql.list_btn", "bot.ql.list_event_btn", "bot.ql.list_pick_event",
}

func init() {
	MessageKeys = append(MessageKeys, qlKeys...)
}
