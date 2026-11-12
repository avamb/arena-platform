package eventbot

// menuSectionKeys lists every bot.mn.* key of the sectioned main menu
// (menu_sections.go). The locale test checks them in every supported catalog
// together with MessageKeys.
var menuSectionKeys = []string{
	"bot.mn.orders_btn", "bot.mn.promo_btn", "bot.mn.door_btn", "bot.mn.settings_btn",
	"bot.mn.disputes_count_btn", "bot.mn.mail_btn", "bot.mn.guests_btn", "bot.mn.pay_btn", "bot.mn.venues_btn",
	"bot.mn.orders_title", "bot.mn.promo_title", "bot.mn.door_title", "bot.mn.settings_title",
	"bot.mn.mail_text", "bot.mn.venues_text", "bot.mn.guests_text",
	"bot.mn.pay_ok", "bot.mn.pay_off", "bot.mn.pay_unknown",
	"bot.mn.steps_title",
	"bot.mn.step_org_data", "bot.mn.step_payments", "bot.mn.step_event", "bot.mn.step_sales_link",
	"bot.mn.step_org_data_btn", "bot.mn.step_payments_btn", "bot.mn.step_event_btn", "bot.mn.step_sales_link_btn",
}

func init() { MessageKeys = append(MessageKeys, menuSectionKeys...) }
