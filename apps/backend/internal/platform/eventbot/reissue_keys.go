package eventbot

// reissueKeys lists every bot.reissue.* key of the "Replace tickets" dialog
// (spec 63, stage R-4). The locale test checks them in every supported
// catalog together with MessageKeys.
var reissueKeys = []string{
	"bot.reissue.word", "bot.reissue.btn", "bot.reissue.private_only", "bot.reissue.site",
	"bot.reissue.err_not_paid", "bot.reissue.err_nothing", "bot.reissue.err_closed", "bot.reissue.err_in_progress",
	"bot.reissue.err_ticket_review", "bot.reissue.err_ticket_refund", "bot.reissue.err_key_reused", "bot.reissue.err_applied",
	"bot.reissue.bad_email", "bot.reissue.err_reason", "bot.reissue.note_bad", "bot.reissue.err_refused",
	"bot.reissue.maybe_done", "bot.reissue.cancelled",
	"bot.reissue.pending_head", "bot.reissue.pending_queued", "bot.reissue.pending_failed", "bot.reissue.pending_resend_btn",
	"bot.reissue.no_address", "bot.reissue.case", "bot.reissue.case_order_btn", "bot.reissue.case_other_btn",
	"bot.reissue.email_ask", "bot.reissue.reason_ask",
	"bot.reissue.reason_sent_to_wrong_address", "bot.reissue.reason_forwarded_to_stranger", "bot.reissue.reason_other",
	"bot.reissue.note_ask", "bot.reissue.no_note_btn", "bot.reissue.cancel_btn",
	"bot.reissue.check", "bot.reissue.check_note", "bot.reissue.check_scanned", "bot.reissue.check_provider",
	"bot.reissue.confirm_btn", "bot.reissue.word_ask", "bot.reissue.word_wrong",
	"bot.reissue.err_unknown", "bot.reissue.err_forbidden", "bot.reissue.err_unavailable",
	"bot.reissue.replayed", "bot.reissue.done_head", "bot.reissue.done_line", "bot.reissue.done_letter",
	"bot.reissue.resent",
	// An earlier invitation's own wording (stage R-5, rxKey).
	"bot.reissue.inv_btn", "bot.reissue.inv_email_ask", "bot.reissue.inv_reason_ask", "bot.reissue.inv_note_ask",
	"bot.reissue.inv_check", "bot.reissue.inv_word_ask", "bot.reissue.inv_done_head", "bot.reissue.inv_done_letter",
	"bot.reissue.inv_cancelled", "bot.reissue.inv_pending_head", "bot.reissue.inv_pending_queued",
	"bot.reissue.inv_err_nothing", "bot.reissue.inv_err_closed", "bot.reissue.inv_err_in_progress",
	"bot.reissue.inv_err_ticket_review", "bot.reissue.inv_err_ticket_refund", "bot.reissue.inv_err_applied",
	"bot.reissue.inv_err_refused", "bot.reissue.inv_err_reason", "bot.reissue.inv_err_not_issued", "bot.reissue.inv_resent",
}

func init() {
	MessageKeys = append(MessageKeys, reissueKeys...)
}
