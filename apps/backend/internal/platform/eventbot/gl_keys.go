package eventbot

// glKeys lists every bot.gl.* key of the guest list (guest_list.go). The
// locale test checks them in every supported catalog together with
// MessageKeys.
var glKeys = []string{
	"bot.gl.btn", "bot.gl.private_only", "bot.gl.empty", "bot.gl.caption", "bot.gl.auto_hint",
	"bot.gl.notif_text", "bot.gl.toggle_on", "bot.gl.toggle_off", "bot.gl.toggle_failed",
	"bot.gl.auto_caption_h24", "bot.gl.auto_caption_h3", "bot.gl.auto_caption_doors", "bot.gl.auto_stop",
}

func init() {
	MessageKeys = append(MessageKeys, glKeys...)
}
