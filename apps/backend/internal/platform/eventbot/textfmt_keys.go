package eventbot

// textfmtKeys lists every bot.tf.* key of the "format my text" loop (spec 44,
// RT-05 and RT-06). The locale test checks them in every supported catalog
// together with MessageKeys.
var textfmtKeys = []string{
	"bot.tf.formatting", "bot.tf.preview_ask", "bot.tf.not_applied_note",
	"bot.tf.save_btn", "bot.tf.plain_btn", "bot.tf.again_btn", "bot.tf.cancel_btn",
	"bot.tf.expired", "bot.tf.cancelled",
	"bot.tf.err_too_long", "bot.tf.err_links", "bot.tf.err_scheme", "bot.tf.err_chars", "bot.tf.err_generic",
}

func init() {
	MessageKeys = append(MessageKeys, textfmtKeys...)
}
