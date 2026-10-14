package hauth

// session_policy.go — how long a sign-in lasts (migration 0150, spec
// 08_architecture/37 section 4.4, owner decision 2026-10-14).
//
// The web event center keeps a session while it is used: every refresh
// pushes the end to now + the idle window (72 hours by default), never past
// 30 days from the sign-in. Every other client keeps the fixed 30-day token.

import (
	"context"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// DefaultWebSessionIdle is the idle window of a web event center session.
const DefaultWebSessionIdle = 72 * time.Hour

// maxSessionAge caps every session, idle or not: it equals the fixed
// lifetime of a classic refresh token.
const maxSessionAge = refreshTokenTTL

// SetWebSessionIdle sets the idle window of a web event center session.
// Zero or negative switches the web to the classic fixed lifetime.
func (h *Handler) SetWebSessionIdle(d time.Duration) {
	if d > maxSessionAge {
		d = maxSessionAge
	}
	h.webSessionIdle = d
}

// newSession returns the session facts and the end of the first refresh
// token of a sign-in made now by the client of ctx.
func (h *Handler) newSession(ctx context.Context, now time.Time) (gen.RefreshTokenSession, time.Time) {
	s := gen.RefreshTokenSession{SessionStartedAt: &now}
	channel, _ := audit.ClientChannelFromContext(ctx)
	if channel != "" {
		s.ClientChannel = &channel
	}
	if channel == audit.ChannelEventCenterWeb && h.webSessionIdle > 0 {
		idle := int32(h.webSessionIdle / time.Second) // #nosec G115 -- capped at maxSessionAge (30 days)
		s.IdleSeconds = &idle
	}
	return s, sessionEnd(s, now)
}

// sessionEnd is the end of a refresh token issued now in session s: now plus
// the idle window, capped at 30 days from the sign-in; the fixed 30 days for
// a session without an idle window.
func sessionEnd(s gen.RefreshTokenSession, now time.Time) time.Time {
	if s.IdleSeconds == nil {
		return now.Add(refreshTokenTTL)
	}
	end := now.Add(time.Duration(*s.IdleSeconds) * time.Second)
	if s.SessionStartedAt != nil {
		if limit := s.SessionStartedAt.Add(maxSessionAge); end.After(limit) {
			end = limit
		}
	}
	return end
}
