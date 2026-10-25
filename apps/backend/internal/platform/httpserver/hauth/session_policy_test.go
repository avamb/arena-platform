package hauth

import (
	"context"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

func TestNewSession_WebGetsTheIdleWindowOthersTheFixedLifetime(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	h.SetWebSessionIdle(72 * time.Hour)
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)

	web := audit.WithClientChannel(context.Background(), audit.ChannelEventCenterWeb)
	s, end := h.newSession(web, now)
	if s.IdleSeconds == nil || *s.IdleSeconds != 72*3600 || s.ClientChannel == nil || *s.ClientChannel != "event_center_web" {
		t.Fatalf("web session: %+v", s)
	}
	if !end.Equal(now.Add(72 * time.Hour)) {
		t.Fatalf("web end %v", end)
	}

	admin := audit.WithClientChannel(context.Background(), "admin_web")
	s, end = h.newSession(admin, now)
	if s.IdleSeconds != nil || !end.Equal(now.Add(refreshTokenTTL)) {
		t.Fatalf("admin session: %+v end %v", s, end)
	}
	s, end = h.newSession(context.Background(), now)
	if s.ClientChannel != nil || s.IdleSeconds != nil || !end.Equal(now.Add(refreshTokenTTL)) {
		t.Fatalf("no channel: %+v end %v", s, end)
	}

	off := &Handler{}
	off.SetWebSessionIdle(0)
	if s, _ := off.newSession(web, now); s.IdleSeconds != nil {
		t.Fatalf("idle window switched off, got %+v", s)
	}
	long := &Handler{}
	long.SetWebSessionIdle(90 * 24 * time.Hour)
	if s, _ := long.newSession(web, now); *s.IdleSeconds != int32(refreshTokenTTL/time.Second) {
		t.Fatalf("idle window above the cap: %+v", s)
	}
}

func TestSessionEnd_SlidesButNeverPastThirtyDays(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	h.SetWebSessionIdle(72 * time.Hour)
	started := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s, _ := h.newSession(audit.WithClientChannel(context.Background(), audit.ChannelEventCenterWeb), started)

	day5 := started.Add(5 * 24 * time.Hour)
	if got := sessionEnd(s, day5); !got.Equal(day5.Add(72 * time.Hour)) {
		t.Fatalf("day 5: %v", got)
	}
	day29 := started.Add(29 * 24 * time.Hour)
	if got := sessionEnd(s, day29); !got.Equal(started.Add(30 * 24 * time.Hour)) {
		t.Fatalf("day 29 must stop at 30 days from sign-in: %v", got)
	}
}

// A mini app session (spec 44, RT-12a) lasts 30 minutes from the exchange, a
// refresh inside it never moves the end, and its access token names the
// channel so the money routes still ask for the password.
func TestNewMiniappSession_ThirtyMinutesAndRefreshDoesNotExtendIt(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	h.SetWebSessionIdle(72 * time.Hour) // the web window must not leak in
	started := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)

	s, end := h.newMiniappSession(started)
	if s.ClientChannel == nil || *s.ClientChannel != "event_center_tg" || audit.ChannelEventCenterTG != "event_center_tg" {
		t.Fatalf("channel: %+v", s)
	}
	if s.IdleSeconds == nil || *s.IdleSeconds != 30*60 || s.SessionID == nil || s.SessionStartedAt == nil {
		t.Fatalf("session facts: %+v", s)
	}
	if !end.Equal(started.Add(30 * time.Minute)) {
		t.Fatalf("end %v", end)
	}
	// A refresh at minute 20 would slide an idle window to minute 50: the cap
	// holds it at minute 30.
	if got := sessionEnd(s, started.Add(20*time.Minute)); !got.Equal(started.Add(30 * time.Minute)) {
		t.Fatalf("refresh at minute 20 ends %v", got)
	}
	if c := sessionClaims(s); c.Channel != "event_center_tg" || c.SessionID != s.SessionID.String() {
		t.Fatalf("claims: %+v", c)
	}
	// The web session next to it is unchanged.
	web, _ := h.newSession(audit.WithClientChannel(context.Background(), audit.ChannelEventCenterWeb), started)
	if got := sessionEnd(web, started.Add(20*time.Minute)); !got.Equal(started.Add(20*time.Minute + 72*time.Hour)) {
		t.Fatalf("web refresh ends %v", got)
	}
	if c := sessionClaims(web); c.Channel != "event_center_web" {
		t.Fatalf("web claims: %+v", c)
	}
}
