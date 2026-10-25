package eventbot

// textfmtKeys lists every bot.tf.* key of the "format my text" loop (spec 44,
// RT-05 and RT-06). The locale test checks them in every supported catalog
// together with MessageKeys.
var textfmtKeys = []string{
	"bot.tf.formatting", "bot.tf.preview_ask", "bot.tf.not_applied_note",
	"bot.tf.save_btn", "bot.tf.plain_btn", "bot.tf.again_btn", "bot.tf.cancel_btn",
	"bot.tf.expired", "bot.tf.cancelled",
	"bot.tf.err_too_long", "bot.tf.err_links", "bot.tf.err_scheme", "bot.tf.err_chars", "bot.tf.err_generic",
	// "Text in the letter" on the event card (RT-06)
	"bot.tf.event_btn", "bot.tf.note_title", "bot.tf.note_about", "bot.tf.note_none", "bot.tf.note_now",
	"bot.tf.note_add_btn", "bot.tf.note_edit_btn", "bot.tf.note_delete_btn", "bot.tf.note_test_btn",
	"bot.tf.note_ask", "bot.tf.note_saved", "bot.tf.note_save_failed",
	"bot.tf.note_delete_ask", "bot.tf.note_delete_yes_btn", "bot.tf.note_delete_no_btn", "bot.tf.note_deleted",
	"bot.tf.note_test_sent", "bot.tf.note_test_empty", "bot.tf.note_test_limit",
	// The button of the Telegram mini app editor, its note and its refusal (RT-12a)
	"bot.tf.edit_btn", "bot.tf.edit_note", "bot.tf.edit_failed", "bot.tf.edit_superadmin",
	// The confirmation after the mini app editor saved a text (RT-12b)
	"bot.tf.editor_saved",
}

func init() {
	MessageKeys = append(MessageKeys, textfmtKeys...)
}
