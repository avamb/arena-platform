package eventbot

// webKeys lists every bot.web.* key of "Open in browser" (web_login.go). The
// locale test checks them in every supported catalog together with
// MessageKeys.
var webKeys = []string{
	"bot.web.btn", "bot.web.link", "bot.web.open_btn", "bot.web.private_only",
	"bot.web.not_configured", "bot.web.superadmin", "bot.web.failed",
	"bot.web.signed_in", "bot.web.signout_btn", "bot.web.signed_out", "bot.web.signout_failed",
}

func init() {
	MessageKeys = append(MessageKeys, webKeys...)
}
