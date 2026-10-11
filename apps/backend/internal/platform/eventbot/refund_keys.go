package eventbot

// refundKeys lists every bot.ref.* key of the "Refund" dialog (spec 35 EC-21
// and EC-22). The locale test checks them in every supported catalog together
// with MessageKeys.
var refundKeys = []string{
	"bot.ref.btn", "bot.ref.private_only", "bot.ref.site", "bot.ref.unsupported", "bot.ref.nothing",
	"bot.ref.pick", "bot.ref.select_all", "bot.ref.continue", "bot.ref.none_selected",
	"bot.ref.partial_btn", "bot.ref.partial_ask", "bot.ref.partial_bad",
	"bot.ref.reason_ask", "bot.ref.reason1", "bot.ref.reason2", "bot.ref.reason3",
	"bot.ref.reason_other_btn", "bot.ref.reason_text_ask", "bot.ref.reason_bad",
	"bot.ref.confirm", "bot.ref.letter_on_btn", "bot.ref.letter_off_btn", "bot.ref.word", "bot.ref.word_wrong",
	"bot.ref.done_head", "bot.ref.line", "bot.ref.st_succeeded", "bot.ref.st_pending", "bot.ref.st_failed",
	"bot.ref.st_review", "bot.ref.st_other", "bot.ref.err_progress", "bot.ref.err_changed",
}

func init() {
	MessageKeys = append(MessageKeys, refundKeys...)
}
