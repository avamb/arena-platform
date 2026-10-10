package eventbot

// teamKeys lists every bot.team.* key of the Team screen's invitation
// controls (spec 35 EC-16). The locale test checks them in every supported
// catalog together with MessageKeys.
var teamKeys = []string{
	"bot.team.state_waiting", "bot.team.state_expired", "bot.team.state_accepted",
	"bot.team.revoke_btn", "bot.team.resend_btn", "bot.team.revoke_confirm", "bot.team.revoke_yes_btn",
	"bot.team.revoked_removed", "bot.team.revoked_kept", "bot.team.resent", "bot.team.resend_too_soon",
	"bot.team.invitation_gone", "bot.team.revoke_failed", "bot.team.resend_failed",
	"bot.team.kept_accepted", "bot.team.kept_member_before", "bot.team.kept_other_invitation",
	"bot.team.kept_in_use", "bot.team.kept_role_changed", "bot.team.kept_last_owner",
}

func init() {
	MessageKeys = append(MessageKeys, teamKeys...)
}
