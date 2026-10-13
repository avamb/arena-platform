package eventbot

// orgDataKeys lists every bot.og.* key of the owner's "Organization data"
// screens (HP-4a). The locale test checks them in every supported catalog
// together with MessageKeys.
var orgDataKeys = []string{
	"bot.og.btn", "bot.og.owner_only", "bot.og.card", "bot.og.card_where",
	"bot.og.line_legal_name", "bot.og.line_legal_form", "bot.og.line_reg", "bot.og.line_tax",
	"bot.og.line_address", "bot.og.pages_title", "bot.og.pages_none", "bot.og.state_shown",
	"bot.og.state_hidden", "bot.og.none", "bot.og.now", "bot.og.missing_title",
	"bot.og.m_legal_name", "bot.og.m_tax_id", "bot.og.m_address_line1", "bot.og.m_address_city",
	"bot.og.m_address_postal_code", "bot.og.m_public_email", "bot.og.btn_legal_name", "bot.og.btn_legal_form",
	"bot.og.btn_reg", "bot.og.btn_tax", "bot.og.btn_address", "bot.og.btn_city",
	"bot.og.btn_postcode", "bot.og.btn_country", "bot.og.btn_page", "bot.og.q_legal_name",
	"bot.og.q_legal_form", "bot.og.q_reg", "bot.og.q_address", "bot.og.q_city",
	"bot.og.q_postcode", "bot.og.q_country", "bot.og.q_scheme", "bot.og.scheme_eu_vat",
	"bot.og.scheme_gb_vat", "bot.og.scheme_il_vat", "bot.og.scheme_us_ein", "bot.og.scheme_other",
	"bot.og.q_tax_eu_vat", "bot.og.q_tax_gb_vat", "bot.og.q_tax_il_vat", "bot.og.q_tax_us_ein",
	"bot.og.q_tax_other", "bot.og.other_scheme_btn", "bot.og.page_card", "bot.og.page_where",
	"bot.og.line_email", "bot.og.line_phone", "bot.og.line_state", "bot.og.btn_email",
	"bot.og.btn_phone", "bot.og.btn_show", "bot.og.btn_hide", "bot.og.q_email",
	"bot.og.q_phone", "bot.og.personal_email", "bot.og.clear_btn", "bot.og.cancel_btn",
	"bot.og.saved", "bot.og.cleared", "bot.og.shown_done", "bot.og.hidden_done",
	"bot.og.channel_gone", "bot.og.err_legal_name", "bot.og.err_legal_form", "bot.og.err_reg",
	"bot.og.err_address", "bot.og.err_city", "bot.og.err_postcode", "bot.og.err_country",
	"bot.og.err_tax_eu_vat", "bot.og.err_tax_gb_vat", "bot.og.err_tax_il_vat", "bot.og.err_tax_us_ein",
	"bot.og.err_tax_other", "bot.og.err_tax_api", "bot.og.err_email", "bot.og.err_phone",
}

func init() {
	MessageKeys = append(MessageKeys, orgDataKeys...)
}
