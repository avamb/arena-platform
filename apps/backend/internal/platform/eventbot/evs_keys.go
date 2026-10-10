package eventbot

// evsKeys lists every bot.evs.* key of the event card's status buttons (spec
// 35 EC-10: publish, take off sale, archive, delete). The locale test checks
// them in every supported catalog together with MessageKeys.
var evsKeys = []string{
	"bot.evs.publish_btn", "bot.evs.off_btn", "bot.evs.archive_btn", "bot.evs.delete_btn",
	"bot.evs.off_ask", "bot.evs.off_yes_btn", "bot.evs.archive_ask", "bot.evs.archive_yes_btn",
	"bot.evs.published_done", "bot.evs.off_done", "bot.evs.archived_done", "bot.evs.deleted_done",
	"bot.evs.delete_ask", "bot.evs.delete_wrong", "bot.evs.delete_blocked", "bot.evs.delete_blocked_arch",
	"bot.evs.delete_word", "bot.evs.need_session", "bot.evs.need_tier", "bot.evs.not_allowed",
	"bot.evs.archive_instead_btn",
}

func init() {
	MessageKeys = append(MessageKeys, evsKeys...)
}
