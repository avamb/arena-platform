package eventbot

// inviteKeys lists every bot.inv.* key of the "Invitations" screens (spec 35
// EC-12). The locale tests check them in every supported catalog together
// with MessageKeys.
var inviteKeys = []string{
	"bot.inv.btn", "bot.inv.hub", "bot.inv.issue_btn", "bot.inv.list_btn",
	"bot.inv.ev_title", "bot.inv.ev_empty", "bot.inv.se_title", "bot.inv.se_empty",
	"bot.inv.ti_title", "bot.inv.ti_empty", "bot.inv.ti_seated", "bot.inv.ti_full",
	"bot.inv.free_label", "bot.inv.qty_ask", "bot.inv.qty_bad", "bot.inv.qty_over_max",
	"bot.inv.qty_over_free", "bot.inv.rcpt_ask", "bot.inv.rcpt_got", "bot.inv.rcpt_bad",
	"bot.inv.bad_line", "bot.inv.reason_email", "bot.inv.reason_name", "bot.inv.reason_repeat",
	"bot.inv.reason_several", "bot.inv.rcpt_too_many", "bot.inv.confirm", "bot.inv.send_btn",
	"bot.inv.done", "bot.inv.partial", "bot.inv.partial_done", "bot.inv.partial_left",
	"bot.inv.retry_btn", "bot.inv.why_full", "bot.inv.why_rejected", "bot.inv.why_gone",
	"bot.inv.why_error", "bot.inv.list_title", "bot.inv.legend", "bot.inv.list_empty",
	"bot.inv.state_valid", "bot.inv.state_revoked", "bot.inv.state_used", "bot.inv.card",
	"bot.inv.revoke_btn", "bot.inv.revoke_ask", "bot.inv.revoke_wrong", "bot.inv.revoke_word",
	"bot.inv.revoke_done", "bot.inv.revoke_already", "bot.inv.revoke_used", "bot.inv.private_only",
}

func init() {
	MessageKeys = append(MessageKeys, inviteKeys...)
}
