package eventbot

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// userJWTTTL bounds the token the bot mints per interaction. It is never
// stored and never leaves the bot process except as a request header.
const userJWTTTL = 5 * time.Minute

// Identity is who a Telegram account speaks as.
type Identity struct {
	Link        gen.BotTelegramLinkRow
	Memberships []Membership
	Current     *Membership
}

// Locale is the language the bot answers this account in.
func (id *Identity) Locale() string {
	if id == nil {
		return "en"
	}
	return NormalizeLocale(id.Link.Locale)
}

// TokenMinter issues user JWTs the same way POST /v1/auth/login does: no
// roles in the claim, permissions resolved from memberships by the API.
type TokenMinter struct {
	secret   string
	issuer   string
	audience string
}

// NewTokenMinter configures the minter with the API's own signing values.
func NewTokenMinter(secret, issuer, audience string) *TokenMinter {
	return &TokenMinter{secret: secret, issuer: issuer, audience: audience}
}

// Mint returns a short-lived JWT for userID.
func (m *TokenMinter) Mint(userID uuid.UUID) (string, error) {
	tok, _, err := auth.IssueJWT(m.secret, userID, nil, nil, m.issuer, m.audience, userJWTTTL)
	return tok, err
}

// ErrNotLinked means the Telegram account has no active link.
var ErrNotLinked = errors.New("eventbot: telegram account is not linked")

// resolveIdentity loads the link of a Telegram account, mints its JWT and
// reads its memberships from /v1/me. The current organization is the
// remembered one when the user still belongs to it, otherwise the only
// membership, otherwise nil (the caller asks).
func (b *Bot) resolveIdentity(ctx context.Context, telegramUserID int64) (*Identity, string, error) {
	link, err := b.queries.GetBotTelegramLink(ctx, telegramUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrNotLinked
		}
		return nil, "", err
	}
	if link.RevokedAt != nil {
		return nil, "", ErrNotLinked
	}
	jwt, err := b.minter.Mint(link.UserID)
	if err != nil {
		return nil, "", err
	}
	memberships, err := b.arena.Me(ctx, jwt)
	if err != nil {
		return nil, "", err
	}
	id := &Identity{Link: link, Memberships: memberships}
	if link.CurrentOrgID != nil {
		for i := range memberships {
			if memberships[i].OrgID == *link.CurrentOrgID {
				id.Current = &memberships[i]
				break
			}
		}
	}
	if id.Current == nil && len(memberships) == 1 {
		id.Current = &memberships[0]
		orgID := memberships[0].OrgID
		_ = b.queries.SetBotTelegramLinkCurrentOrg(ctx, telegramUserID, &orgID)
	}
	return id, jwt, nil
}
