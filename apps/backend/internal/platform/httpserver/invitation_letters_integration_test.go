//go:build integration

package httpserver

// Task 4-4: an invitation has no order, so the delivery status of its letter
// is read through the ticket's delivery job. `letters` on the invitations list
// and on the issue answer uses the same shape as the order card's `letters`
// (emailstatus.InvitationLetters). The scenario goes through the real router
// as an owner: issue two guests (one call per guest, qty 1, as the bot and the
// web do), see both letters queued, hand one to the "relay" and let Brevo
// report it delivered, fail the other, replay the issue, and check that a
// user of another organization sees none of it.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
)

type invLetter struct {
	ID        *string  `json:"id"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Recipient string   `json:"recipient"`
	TicketIDs []string `json:"ticket_ids"`
	QueuedAt  *string  `json:"queued_at"`
	SentAt    *string  `json:"sent_at"`
	Delivered *string  `json:"delivered_at"`
	Failure   *string  `json:"failure_event"`
}

type invListItem struct {
	ID      string `json:"id"`
	Letters []invLetter
	Tickets []struct {
		ID         string `json:"id"`
		HolderMail string `json:"holder_email"`
	} `json:"tickets"`
}

func TestInvitationLetters_ListAndIssueCarryTheLetterStatus(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	t.Cleanup(api.Close)
	seed := seedInviteBotOrg(t, pool, f.orgID)

	mint := func(user uuid.UUID) string {
		tok, err := eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience).Mint(user)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	call := func(user uuid.UUID, method, path string, body any) (int, []byte) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+mint(user))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}
	base := "/v1/organizations/" + f.orgID.String() + "/complimentary"
	batch := "web-" + uuid.NewString()
	issue := func(email, name string, idx int) (int, map[string]any) {
		code, raw := call(f.ownerID, http.MethodPost, base, map[string]any{
			"session_id": seed.session, "tier_id": seed.vip, "qty": 1,
			"batch_id": batch + "-" + string(rune('0'+idx)), "recipients": []string{email}, "recipient_names": []string{name},
		})
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return code, out
	}

	// ── Issue two guests: one call each ─────────────────────────────────────
	a := "inv-a-" + seed.suffix + "@example.test"
	b := "inv-b-" + seed.suffix + "@example.test"
	codeA, outA := issue(a, "Guest A", 1)
	codeB, outB := issue(b, "Guest B", 2)
	if codeA != http.StatusCreated || codeB != http.StatusCreated {
		t.Fatalf("issue: %d %d (%v %v)", codeA, codeB, outA, outB)
	}
	if _, ok := outA["letters"].([]any); !ok {
		t.Fatalf("the issue answer has no letters array: %v", outA)
	}

	list := func(user uuid.UUID, path string) []invListItem {
		t.Helper()
		code, raw := call(user, http.MethodGet, path, nil)
		if code != http.StatusOK {
			t.Fatalf("list: %d %s", code, raw)
		}
		var body struct {
			Issuances []invListItem `json:"issuances"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		return body.Issuances
	}
	byEmail := func(items []invListItem) map[string]invListItem {
		m := map[string]invListItem{}
		for _, it := range items {
			if len(it.Tickets) == 1 {
				m[it.Tickets[0].HolderMail] = it
			}
		}
		return m
	}

	items := byEmail(list(f.ownerID, base))
	ia, ib := items[a], items[b]
	if ia.ID == "" || ib.ID == "" {
		t.Fatalf("the list does not hold both guests: %v", items)
	}
	for _, it := range []invListItem{ia, ib} {
		if len(it.Letters) != 1 || it.Letters[0].Status != "queued" || it.Letters[0].Kind != "invitation" ||
			it.Letters[0].ID != nil || it.Letters[0].QueuedAt == nil ||
			len(it.Letters[0].TicketIDs) != 1 || it.Letters[0].TicketIDs[0] != it.Tickets[0].ID {
			t.Fatalf("a fresh invitation should have one queued invitation letter for its ticket: %+v", it.Letters)
		}
	}

	// ── The relay takes guest A's letter, Brevo reports it delivered ────────
	ctx := context.Background()
	var jobA, jobB uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM delivery_jobs WHERE ticket_id = $1`, ia.Tickets[0].ID).Scan(&jobA); err != nil {
		t.Fatalf("delivery job of A: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM delivery_jobs WHERE ticket_id = $1`, ib.Tickets[0].ID).Scan(&jobB); err != nil {
		t.Fatalf("delivery job of B: %v", err)
	}
	letter, err := emailstatus.Begin(ctx, pool, emailstatus.NewLetter{
		OrgID: f.orgID, Kind: emailstatus.KindInvitation, Recipient: a, DeliveryJobIDs: []uuid.UUID{jobA},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := emailstatus.Accepted(ctx, pool, letter, "inv-"+uuid.NewString()+"@relay", []uuid.UUID{jobA}); err != nil {
		t.Fatalf("Accepted: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'sent', sent_at = now() WHERE id = $1`, jobA); err != nil {
		t.Fatal(err)
	}
	ev, class := emailstatus.Normalize("delivered")
	if _, err := emailstatus.Apply(ctx, pool, emailstatus.ProviderEvent{
		Event: ev, RawEvent: "delivered", Class: class, LetterID: letter, At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Apply delivered: %v", err)
	}
	// Guest B's job ends failed: the relay refused it.
	if _, err := pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'failed', last_error = $2 WHERE id = $1`,
		jobB, "smtp refused for "+b); err != nil {
		t.Fatal(err)
	}

	items = byEmail(list(f.ownerID, base))
	la := items[a].Letters
	if len(la) != 1 || la[0].Status != "delivered" || la[0].Kind != "invitation" || la[0].ID == nil ||
		*la[0].ID != letter.String() || la[0].Recipient != a || la[0].SentAt == nil || la[0].Delivered == nil {
		t.Fatalf("A's letter should be the delivered invitation letter: %+v", la)
	}
	lb := items[b].Letters
	if len(lb) != 1 || lb[0].Status != "failed" || lb[0].Failure == nil || *lb[0].Failure != emailstatus.FailureSendFailed {
		t.Fatalf("B's letter should be failed (send_failed): %+v", lb)
	}
	if lb[0].Kind != "invitation" {
		t.Fatalf("a refused letter keeps the invitation kind: %+v", lb)
	}

	// ── A replay of the same batch answers the same letters ─────────────────
	code, replay := issue(a, "Guest A", 1)
	if code != http.StatusOK || replay["idempotent_replay"] != true {
		t.Fatalf("replay: %d %v", code, replay)
	}
	rl, _ := replay["letters"].([]any)
	if len(rl) != 1 || rl[0].(map[string]any)["status"] != "delivered" {
		t.Fatalf("the replay should carry A's delivered letter: %v", replay["letters"])
	}
	// The state filter and the session filter keep carrying letters.
	if got := list(f.ownerID, base+"?state=valid&session_id="+seed.session.String()); len(got) != 2 || len(got[0].Letters) != 1 {
		t.Fatalf("filtered list: %+v", got)
	}

	// ── Another organization's owner sees none of it ───────────────────────
	if got := list(other.ownerID, "/v1/organizations/"+other.orgID.String()+"/complimentary"); len(got) != 0 {
		t.Fatalf("the other organization sees %d issuances", len(got))
	}
	// The letters of a foreign organization are never read through ours: a
	// plain SQL check that A's letter belongs to our organization only.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_letters WHERE org_id = $1`, other.orgID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the other organization holds %d letters (err %v)", n, err)
	}
}
