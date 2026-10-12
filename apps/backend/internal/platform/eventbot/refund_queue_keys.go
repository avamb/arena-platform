package eventbot

// queueKeys lists every bot.rq.* key of the refunds queue (spec 35 EC-23).
var queueKeys = []string{
	"bot.rq.btn", "bot.rq.title", "bot.rq.f_review", "bot.rq.f_failed", "bot.rq.f_pending", "bot.rq.f_done",
	"bot.rq.empty", "bot.rq.row", "bot.rq.retry_btn", "bot.rq.order_btn", "bot.rq.retry_ask", "bot.rq.retry_yes",
	"bot.rq.retry_cancels_ticket", "bot.rq.retry_keeps_ticket", "bot.rq.retry_letter", "bot.rq.retry_no_letter",
	"bot.rq.retry_done", "bot.rq.not_retryable", "bot.rq.provider_word",
	"bot.rq.retry_no", "bot.rq.already_replaced",
	"bot.rq.still_sending", "bot.rq.err_progress", "bot.rq.err_ticket", "bot.rq.err_refused",
}

func init() {
	MessageKeys = append(MessageKeys, queueKeys...)
}
