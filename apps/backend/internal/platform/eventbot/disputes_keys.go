package eventbot

// disputes_keys.go — every bot.dp.* key of the Disputes screens (spec 51.3
// section 9.2, DS-11). The locale test checks them in every supported catalog
// together with MessageKeys.

var disputeKeys = []string{
	// the menu row, the list
	"bot.dp.btn", "bot.dp.title", "bot.dp.open_line", "bot.dp.f_open", "bot.dp.f_review", "bot.dp.f_closed",
	"bot.dp.empty", "bot.dp.row", "bot.dp.row_due", "bot.dp.open_btn",
	"bot.dp.when_madrid", "bot.dp.when_venue",
	// reasons, states, outcomes
	"bot.dp.reason_fraud", "bot.dp.reason_not_received", "bot.dp.reason_not_recognized", "bot.dp.reason_refund",
	"bot.dp.reason_quality", "bot.dp.reason_duplicate", "bot.dp.reason_other",
	"bot.dp.state_needs_response", "bot.dp.state_draft_ready", "bot.dp.state_submitted", "bot.dp.state_under_review",
	"bot.dp.state_accepted", "bot.dp.state_expired", "bot.dp.state_closed",
	"bot.dp.outcome_won", "bot.dp.outcome_lost", "bot.dp.outcome_accepted", "bot.dp.outcome_withdrawn", "bot.dp.outcome_expired",
	// the card
	"bot.dp.card_title", "bot.dp.card_event", "bot.dp.card_amount", "bot.dp.card_reason", "bot.dp.card_state",
	"bot.dp.card_outcome", "bot.dp.card_buyer", "bot.dp.card_due", "bot.dp.card_due_estimate", "bot.dp.card_overdue",
	"bot.dp.card_lowvalue", "bot.dp.card_gaps_head", "bot.dp.card_files_head", "bot.dp.card_file", "bot.dp.file_full_tag",
	"bot.dp.card_owner_only", "bot.dp.no_package",
	"bot.dp.gap_no_order", "bot.dp.gap_no_letters", "bot.dp.gap_no_opens", "bot.dp.gap_no_access", "bot.dp.gap_access_off",
	"bot.dp.gap_no_scans", "bot.dp.gap_no_consent", "bot.dp.gap_no_terms", "bot.dp.gap_no_refunds", "bot.dp.gap_reissued",
	"bot.dp.gap_other",
	// buttons
	"bot.dp.package_btn", "bot.dp.order_btn", "bot.dp.submit_btn", "bot.dp.accept_btn", "bot.dp.own_btn", "bot.dp.rebuild_btn",
	"bot.dp.back_card_btn", "bot.dp.back_list_btn", "bot.dp.cancel_btn", "bot.dp.submit_yes_btn", "bot.dp.accept_yes_btn",
	"bot.dp.test_win_btn", "bot.dp.test_lose_btn", "bot.dp.own_yes_btn", "bot.dp.own_no_btn",
	// the package
	"bot.dp.file_caption", "bot.dp.file_caption_full", "bot.dp.files_sent", "bot.dp.files_failed",
	"bot.dp.rebuilding", "bot.dp.rebuilt", "bot.dp.rebuilt_draft", "bot.dp.rebuilt_nodraft",
	// the typed word and the final screen
	disputeSubmitWordKey, disputeAcceptWordKey,
	"bot.dp.submit_ask_api", "bot.dp.submit_ask_email", "bot.dp.accept_ask", "bot.dp.word_wrong",
	"bot.dp.submit_final_api", "bot.dp.submit_final_email", "bot.dp.accept_final", "bot.dp.stale_btn",
	"bot.dp.sending", "bot.dp.done_submit_api", "bot.dp.done_submit_email", "bot.dp.done_accept", "bot.dp.done_own",
	"bot.dp.own_ask", "bot.dp.cancelled", "bot.dp.gone", "bot.dp.expired", "bot.dp.not_open", "bot.dp.private_only",
	// refusals
	"bot.dp.err_forbidden", "bot.dp.err_already", "bot.dp.err_closed", "bot.dp.err_no_package", "bot.dp.err_key",
	"bot.dp.err_recipient", "bot.dp.err_test_live", "bot.dp.err_channel", "bot.dp.err_unknown", "bot.dp.err_refused",
	"bot.dp.err_unavailable",
}

func init() {
	MessageKeys = append(MessageKeys, disputeKeys...)
}
