package eventbot

// arena_client_web.go — the routes behind "Open in browser" (web_login.go,
// spec 37 section 4.3).

import (
	"context"
	"net/http"
	"time"
)

// WebLoginLink is a one-time link to the web event center.
type WebLoginLink struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// WebLoginLink asks for a one-time link for the person behind a linked
// Telegram account, under the service token.
func (c *ArenaClient) WebLoginLink(ctx context.Context, telegramUserID int64) (WebLoginLink, error) {
	var out WebLoginLink
	body := struct {
		TelegramUserID int64 `json:"telegram_user_id"`
	}{telegramUserID}
	err := c.do(ctx, http.MethodPost, "/v1/bot/web-login-codes", c.serviceToken, body, &out)
	return out, err
}

// WebLoginNotice is a sign-in through a bot link the bot must report.
type WebLoginNotice struct {
	TelegramUserID int64     `json:"telegram_user_id"`
	SignedInAt     time.Time `json:"signed_in_at"`
}

// ClaimWebLoginNotices takes the sign-ins not reported yet; each comes once.
func (c *ArenaClient) ClaimWebLoginNotices(ctx context.Context) ([]WebLoginNotice, error) {
	var out struct {
		Notices []WebLoginNotice `json:"notices"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/bot/web-login-notices/claim", c.serviceToken, struct{}{}, &out)
	return out.Notices, err
}

// RevokeWebSessions ends every web event center session of the linked user
// ("Sign out everywhere").
func (c *ArenaClient) RevokeWebSessions(ctx context.Context, jwt string) error {
	var out struct {
		RevokedSessions int64 `json:"revoked_sessions"`
	}
	return c.do(ctx, http.MethodPost, "/v1/auth/web-sessions/revoke", jwt, struct{}{}, &out)
}
