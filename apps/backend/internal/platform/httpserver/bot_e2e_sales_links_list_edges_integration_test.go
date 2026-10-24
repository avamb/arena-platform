//go:build integration

package httpserver

// The edges of the bot's list of links (QL-9): a text typed while the list is
// open goes on to whoever else takes it, a list that ran out says so with the
// menu button, a foreign event narrows nothing, an agent is turned away from
// every list press, and a cursor the API no longer takes falls back to the
// first page.

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const qlEdgesOwnerTG, qlEdgesAgentTG = int64(7863), int64(7864)

func TestBotE2E_SalesLinksListEdges(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv, api := qlListServer(t, pool)
	seed := seedPromoOrg(t, pool, f.orgID)
	qlCleanup(t, pool, f.orgID)
	linkECBotUser(t, f, srv, qlEdgesOwnerTG, "owner")

	// An agent, linked to Telegram directly (the bot invitation never makes one).
	ctx := context.Background()
	q := gen.New(pool)
	agent, err := q.InsertUser(ctx, f.newEmail("agent"), "x", "ru")
	if err != nil {
		t.Fatalf("InsertUser agent: %v", err)
	}
	if _, err := q.InsertMembership(ctx, agent.ID, f.orgID, "agent"); err != nil {
		t.Fatalf("InsertMembership agent: %v", err)
	}
	if _, err := q.UpsertBotTelegramLink(ctx, qlEdgesAgentTG, agent.ID, nil, "ru", &f.orgID); err != nil {
		t.Fatalf("link agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, qlEdgesAgentTG)
	})

	tg := newStubTelegram(t)
	startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, qlEdgesOwnerTG}
	agentD := ecDriver{t, tg, qlEdgesAgentTG}

	qlMake(owner, seed.eventA, "Афиша E1")
	owner.press("ql:menu", "Фильтр: Активные")

	// ── a text typed while the list is open is nobody's answer ────────────────
	owner.say("Афиша для кассы", "Я не понял")
	if n := countQLDialogs(t, pool, qlEdgesOwnerTG, "sales_link"); n != 0 {
		t.Errorf("a typed text must not start or keep a sales_link dialog, rows = %d", n)
	}
	if st := readQLListState(t, pool, qlEdgesOwnerTG); st.Step != "list" || len(st.Rows) != 1 {
		t.Errorf("the list must survive a stray text: %+v", st)
	}

	// ── a foreign event narrows nothing ───────────────────────────────────────
	owner.press("ql:le:"+uuid.NewString(), "Не найдено")
	if st := readQLListState(t, pool, qlEdgesOwnerTG); st.Event != nil || st.Filter != "active" {
		t.Errorf("an unknown event changed the list: %+v", st)
	}

	// ── a cursor the API refuses falls back to the first page ─────────────────
	if _, err := pool.Exec(ctx, `UPDATE bot_dialogs SET state = jsonb_set(state, '{ln}', '"garbage"') WHERE telegram_user_id = $1 AND kind = 'sales_link_list'`, qlEdgesOwnerTG); err != nil {
		t.Fatal(err)
	}
	text := owner.press("ql:lp:2", "Страница 1.")
	if st := readQLListState(t, pool, qlEdgesOwnerTG); st.Page != 1 || st.Next != "" {
		t.Errorf("state after a refused cursor = %+v\n%s", st, text)
	}

	// ── a list that ran out says so, with the way back ────────────────────────
	if _, err := pool.Exec(ctx, `UPDATE bot_dialogs SET expires_at = now() - interval '1 minute' WHERE telegram_user_id = $1 AND kind = 'sales_link_list'`, qlEdgesOwnerTG); err != nil {
		t.Fatal(err)
	}
	owner.press("ql:o:0", "Этот диалог устарел")
	if kb := qlNewestButtons(tg, qlEdgesOwnerTG); !slices.Contains(kb, "ql:menu") {
		t.Errorf("the lapse notice must offer the menu: %v", kb)
	}
	owner.press("ql:fp:2", "Этот диалог устарел")
	// The menu entry starts a fresh list.
	owner.press("ql:menu", "Афиша E1")

	// ── an agent is turned away from every list press ─────────────────────────
	for _, data := range []string{"ql:list", "ql:o:0", "ql:le:" + seed.eventA.String(), "ql:new"} {
		agentD.press(data, "Ваша роль")
	}
	if n := countQLDialogs(t, pool, qlEdgesAgentTG, "sales_link_list"); n != 0 {
		t.Errorf("an agent got a list dialog: %d", n)
	}
}

func countQLDialogs(t *testing.T, pool *pgxpool.Pool, tg int64, kind string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = $2`, tg, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
