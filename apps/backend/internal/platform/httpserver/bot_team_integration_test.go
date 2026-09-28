//go:build integration

package httpserver

// GET /v1/organizations/{org_id}/bot-team — the "Team" screen of the bot:
// owners first, then managers, each flagged with whether a Telegram account
// is linked and whether an invitation is still open. Run against a migrated
// database (see bot_e2e_integration_test.go for the DSN recipe).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbot"
)

func TestBotTeamIntegration_ListsOwnersFirstWithLinkAndInvitationFlags(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	ctx := context.Background()
	q := gen.New(pool)

	// A manager who accepted (Telegram linked), a manager whose invitation
	// is still open, and the owner without any invitation.
	linkedEmail := f.newEmail("linked")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager"}`, linkedEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite linked: %d %s", rec.Code, rec.Body.String())
	}
	code, payload := f.queuedCode(linkedEmail)
	const tgID = int64(790)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, tgID)
	})
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d}`, code, linkedEmail, tgID)); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	pendingEmail := f.newEmail("pending")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager"}`, pendingEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite pending: %d %s", rec.Code, rec.Body.String())
	}

	list := func(asUser uuid.UUID) (*httptest.ResponseRecorder, hbot.TeamResponse) {
		t.Helper()
		token, _, err := auth.IssueJWT(botTestJWTSecret, asUser, nil, nil, botTestJWTIssuer, botTestJWTAudience, time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+f.orgID.String()+"/bot-team", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		var out hbot.TeamResponse
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v %s", err, rec.Body.String())
			}
		}
		return rec, out
	}

	rec, team := list(f.ownerID)
	if rec.Code != http.StatusOK {
		t.Fatalf("list as owner: %d %s", rec.Code, rec.Body.String())
	}
	if len(team.Members) != 3 {
		t.Fatalf("members = %+v", team.Members)
	}
	if team.Members[0].UserID != f.ownerID || team.Members[0].Role != "owner" || team.Members[0].MembershipRole != "org_admin" || team.Members[0].TelegramLinked || team.Members[0].InvitationPending {
		t.Fatalf("owner first: %+v", team.Members[0])
	}
	byEmail := map[string]hbot.TeamMember{}
	for _, m := range team.Members[1:] {
		byEmail[m.Email] = m
	}
	if m := byEmail[linkedEmail]; m.UserID.String() != payload.UserID || m.Role != "manager" || !m.TelegramLinked || m.InvitationPending {
		t.Fatalf("linked manager: %+v", m)
	}
	if m := byEmail[pendingEmail]; m.Role != "manager" || m.TelegramLinked || !m.InvitationPending {
		t.Fatalf("pending manager: %+v", m)
	}

	// A manager may read the team too (membership.read); an outsider may not.
	if rec, _ := list(uuid.MustParse(payload.UserID)); rec.Code != http.StatusOK {
		t.Fatalf("list as manager: %d %s", rec.Code, rec.Body.String())
	}
	outsider, err := q.InsertUser(ctx, f.newEmail("outsider"), "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := list(outsider.ID); rec.Code != http.StatusForbidden {
		t.Fatalf("list as outsider: %d %s", rec.Code, rec.Body.String())
	}
}
