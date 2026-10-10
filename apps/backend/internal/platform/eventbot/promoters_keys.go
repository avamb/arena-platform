package eventbot

// promoterKeys lists every bot.prom.* key of the promoter screens (spec 35
// EC-14: the list, the card, the one-question dialogs, the page address
// confirmation, the archive question and the event card's promoter line). The
// locale test checks them in every supported catalog together with
// MessageKeys.
var promoterKeys = []string{
	"bot.prom.btn", "bot.prom.list_title", "bot.prom.list_hint", "bot.prom.empty", "bot.prom.card",
	"bot.prom.line_address", "bot.prom.line_tax", "bot.prom.line_phone", "bot.prom.line_email",
	"bot.prom.line_website", "bot.prom.line_page", "bot.prom.none", "bot.prom.now",
	"bot.prom.btn_name", "bot.prom.btn_address", "bot.prom.btn_tax", "bot.prom.btn_phone",
	"bot.prom.btn_email", "bot.prom.btn_website", "bot.prom.btn_page", "bot.prom.btn_archive",
	"bot.prom.q_name", "bot.prom.q_address", "bot.prom.q_tax", "bot.prom.q_phone", "bot.prom.q_email",
	"bot.prom.email_note", "bot.prom.q_website", "bot.prom.q_page", "bot.prom.clear_btn",
	"bot.prom.cancel_btn", "bot.prom.saved", "bot.prom.saved_page", "bot.prom.cleared",
	"bot.prom.err_name_empty", "bot.prom.err_name_long", "bot.prom.err_name_taken",
	"bot.prom.err_address_long", "bot.prom.err_tax", "bot.prom.err_phone", "bot.prom.err_email",
	"bot.prom.err_website", "bot.prom.err_slug_invalid", "bot.prom.err_slug_taken",
	"bot.prom.slug_confirm", "bot.prom.slug_save_btn", "bot.prom.archive_ask",
	"bot.prom.archive_yes_btn", "bot.prom.archived_done", "bot.prom.gone", "bot.prom.event_line",
	"bot.prom.event_line_org", "bot.prom.event_btn", "bot.prom.event_back_btn", "bot.prom.event_org_note",
}

func init() {
	MessageKeys = append(MessageKeys, promoterKeys...)
}
