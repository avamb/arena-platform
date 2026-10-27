package observability

// Telegram Mini App sign-in label values (task S3b). POST
// /v1/auth/telegram-miniapp is public and unauthenticated, so the only label
// is a CLOSED set: no Telegram id, user id, organization, address or any part
// of the signed data is ever a label.

const subsystemAuth = "auth"

// telegramMiniappOutcomes is the allowlist of the outcome label; anything
// else is counted as "error" rather than minting a new series.
var telegramMiniappOutcomes = map[string]bool{
	"ok":             true, // a session was issued
	"invalid":        true, // malformed data or a wrong signature
	"expired":        true, // a genuine signature older than five minutes
	"not_linked":     true, // a genuine Telegram account that is not linked
	"forbidden":      true, // deactivated account or platform superadmin
	"not_configured": true, // TELEGRAM_MINIAPP_SECRET_KEY is unset
	"rate_limited":   true,
	"error":          true,
}

// RecordTelegramMiniappLogin counts one request of the Mini App sign-in. Safe
// on a nil receiver or a Metrics without the counter (bare test servers).
func (m *Metrics) RecordTelegramMiniappLogin(outcome string) {
	if m == nil || m.TelegramMiniappLoginTotal == nil {
		return
	}
	if !telegramMiniappOutcomes[outcome] {
		outcome = "error"
	}
	m.TelegramMiniappLoginTotal.WithLabelValues(outcome).Inc()
}
