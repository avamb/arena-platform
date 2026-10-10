package eventbot

// resendKeys lists every bot.resend.* key of the "Resend tickets" dialog (spec
// 35 EC-13). The locale test checks them in every supported catalog together
// with MessageKeys.
var resendKeys = []string{
	"bot.resend.btn", "bot.resend.choose", "bot.resend.choose_noaddr", "bot.resend.same_btn",
	"bot.resend.other_btn", "bot.resend.other_ask", "bot.resend.bad_email",
	"bot.resend.confirm_same", "bot.resend.confirm_other", "bot.resend.send_btn",
	"bot.resend.done_same", "bot.resend.done_other",
	"bot.resend.site", "bot.resend.not_paid", "bot.resend.no_tickets", "bot.resend.private_only",
}

func init() {
	MessageKeys = append(MessageKeys, resendKeys...)
}
