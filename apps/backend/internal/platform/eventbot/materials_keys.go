package eventbot

// materialsKeys lists every bot.mt.* key of the Materials screens (task 7-3:
// the menu, the five parts' lists, the entry cards, the questions, the delete
// question and the refusals). The locale test checks them in every supported
// catalog together with MessageKeys.
var materialsKeys = []string{
	"bot.mt.btn", "bot.mt.menu", "bot.mt.readonly", "bot.mt.gone_dialog", "bot.mt.menu_btn",
	"bot.mt.sec_g", "bot.mt.sec_v", "bot.mt.sec_p", "bot.mt.sec_m", "bot.mt.sec_r",
	"bot.mt.state_set", "bot.mt.state_unset", "bot.mt.list_g", "bot.mt.list_v", "bot.mt.list_p",
	"bot.mt.list_r", "bot.mt.empty_g", "bot.mt.empty_v", "bot.mt.empty_p", "bot.mt.empty_r",
	"bot.mt.hint_g", "bot.mt.hint_v", "bot.mt.hint_p", "bot.mt.hint_m", "bot.mt.hint_r",
	"bot.mt.pick_hint", "bot.mt.no_caption", "bot.mt.add_btn", "bot.mt.ask_g", "bot.mt.ask_r",
	"bot.mt.ask_more_g", "bot.mt.ask_more_r", "bot.mt.ask_v", "bot.mt.ask_p", "bot.mt.ask_m",
	"bot.mt.ask_caption", "bot.mt.ask_seller", "bot.mt.card_photo_g", "bot.mt.card_photo_r",
	"bot.mt.card_video", "bot.mt.card_partner", "bot.mt.up_btn", "bot.mt.down_btn",
	"bot.mt.caption_btn", "bot.mt.caption_clear_btn", "bot.mt.seller_btn", "bot.mt.delete_btn",
	"bot.mt.delete_yes_btn", "bot.mt.cancel_btn", "bot.mt.done_btn", "bot.mt.del_ask_g",
	"bot.mt.del_ask_v", "bot.mt.del_ask_p", "bot.mt.del_ask_m", "bot.mt.del_ask_r", "bot.mt.map_none",
	"bot.mt.map_card", "bot.mt.map_set_btn", "bot.mt.map_change_btn", "bot.mt.added",
	"bot.mt.added_more", "bot.mt.saved", "bot.mt.moved", "bot.mt.deleted", "bot.mt.err_full",
	"bot.mt.err_photo_failed", "bot.mt.err_photo_big", "bot.mt.err_photo_type",
	"bot.mt.err_need_photo", "bot.mt.err_need_text", "bot.mt.err_caption", "bot.mt.err_video_url",
	"bot.mt.err_partner_url", "bot.mt.err_seller", "bot.mt.err_map", "bot.mt.err_duplicate",
	"bot.mt.err_gone", "bot.mt.err_changed",
}

func init() {
	MessageKeys = append(MessageKeys, materialsKeys...)
}
