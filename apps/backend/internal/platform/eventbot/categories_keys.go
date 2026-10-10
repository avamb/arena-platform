package eventbot

// categoryKeys lists every bot.cat.* key of the category screens (spec 35
// EC-15: the list, the card, close/open, the price question and its
// confirmation, the quantity question, the sale-window screen and calendars).
// The locale test checks them in every supported catalog together with
// MessageKeys.
var categoryKeys = []string{
	"bot.cat.btn", "bot.cat.list_title", "bot.cat.list_empty", "bot.cat.list_hint", "bot.cat.line",
	"bot.cat.places", "bot.cat.places_none", "bot.cat.price_free", "bot.cat.price_pwyw", "bot.cat.gone",
	"bot.cat.gone_one", "bot.cat.state_open", "bot.cat.state_closed", "bot.cat.card_head",
	"bot.cat.card_price", "bot.cat.card_next_price", "bot.cat.card_places", "bot.cat.card_window",
	"bot.cat.card_session_end", "bot.cat.window_both", "bot.cat.window_from", "bot.cat.window_until",
	"bot.cat.window_none", "bot.cat.note_free", "bot.cat.note_pwyw", "bot.cat.note_chain",
	"bot.cat.note_chain_target", "bot.cat.note_seated", "bot.cat.close_btn", "bot.cat.open_btn",
	"bot.cat.price_btn", "bot.cat.window_btn", "bot.cat.qty_btn", "bot.cat.cancel_btn",
	"bot.cat.closed_done", "bot.cat.opened_done", "bot.cat.q_price", "bot.cat.err_price",
	"bot.cat.price_confirm", "bot.cat.price_save_btn", "bot.cat.price_saved", "bot.cat.price_saved_window",
	"bot.cat.q_qty", "bot.cat.err_qty", "bot.cat.err_qty_below", "bot.cat.err_qty_seated",
	"bot.cat.qty_saved", "bot.cat.window_screen", "bot.cat.window_from_btn", "bot.cat.window_until_btn",
	"bot.cat.window_remove_btn", "bot.cat.q_wstart", "bot.cat.q_wend", "bot.cat.cap_note",
	"bot.cat.err_window", "bot.cat.window_saved", "bot.cat.window_removed",
}

func init() {
	MessageKeys = append(MessageKeys, categoryKeys...)
}
