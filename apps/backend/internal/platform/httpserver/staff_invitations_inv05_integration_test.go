//go:build integration

// The organization-wide list of staff invitations, task INV-05 (spec 46,
// section 6.1): GET /v1/organizations/{org_id}/invitations through the real
// router - the session fields, the state / session / event filters, keyset
// pages, roles and organization isolation.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestINV05_OrgInvitationList_FiltersPagesAndIsolation(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	orgPath := "/v1/organizations/" + f.o.org.String()
	listPath := orgPath + "/invitations"

	// Three invitations: two on seats (one by the manager) and one GA.
	var orderIDs []string
	for _, c := range []struct {
		token string
		body  string
	}{
		{f.o.owner, inv02Body(uuid.New(), []string{"A|1|4"}, nil, 0, "pdf_only", "")},
		{f.manager, inv02Body(uuid.New(), []string{"A|1|5"}, nil, 0, "pdf_only", "")},
		{f.o.owner, inv02Body(uuid.New(), nil, &f.gaTier, 1, "pdf_only", "")},
	} {
		code, body := f.issue(c.token, c.body)
		if code != http.StatusCreated {
			t.Fatalf("issue: %d %s", code, body["_raw"])
		}
		orderIDs = append(orderIDs, body["order_id"].(string))
		// created_at must differ for the keyset order to be the issue order.
		time.Sleep(5 * time.Millisecond)
	}
	// Tickets are minted by a worker job; do its work for the first two.
	for _, id := range orderIDs[:2] {
		var csID uuid.UUID
		if err := f.pool.QueryRow(ctx, `SELECT checkout_session_id FROM orders WHERE id = $1`, id).Scan(&csID); err != nil {
			t.Fatalf("checkout session: %v", err)
		}
		cs, err := gen.New(f.pool).GetCheckoutSessionByID(ctx, csID)
		if err != nil {
			t.Fatalf("GetCheckoutSessionByID: %v", err)
		}
		if _, err := f.srv.ticketsHandler().IssueTicketsForCheckout(ctx, cs); err != nil {
			t.Fatalf("IssueTicketsForCheckout: %v", err)
		}
	}

	// Default state is active: all three, newest first, each with its session.
	code, body := f.do(f.manager, http.MethodGet, listPath, "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body["_raw"])
	}
	items := body["items"].([]any)
	if len(items) != 3 || body["next_cursor"] != nil {
		t.Fatalf("list: %d items, cursor %v: %s", len(items), body["next_cursor"], body["_raw"])
	}
	for i, want := range []string{orderIDs[2], orderIDs[1], orderIDs[0]} {
		if got := items[i].(map[string]any)["order_id"]; got != want {
			t.Fatalf("item %d = %v, want %s (newest first)", i, got, want)
		}
	}
	first := items[0].(map[string]any)
	sess, _ := first["session"].(map[string]any)
	if sess["id"] != f.session.String() || sess["event_id"] != f.event.String() || sess["event_name"] != "INV02 Event" ||
		sess["venue_name"] != "INV02 Venue" || sess["timezone"] != "Europe/Prague" || sess["start_at"] == nil {
		t.Fatalf("session block: %s", body["_raw"])
	}
	if first["issued_by"].(map[string]any)["user_id"] != f.ownerID.String() || len(first["places"].([]any)) != 1 {
		t.Fatalf("session list item fields missing: %s", body["_raw"])
	}

	// Pages of two: the cursor leads to the third and the last page has none.
	code, p1 := f.do(f.manager, http.MethodGet, listPath+"?limit=2", "")
	cursor, _ := p1["next_cursor"].(string)
	if code != http.StatusOK || len(p1["items"].([]any)) != 2 || cursor == "" {
		t.Fatalf("page 1: %d %s", code, p1["_raw"])
	}
	code, p2 := f.do(f.manager, http.MethodGet, listPath+"?limit=2&cursor="+cursor, "")
	if code != http.StatusOK || len(p2["items"].([]any)) != 1 || p2["next_cursor"] != nil ||
		p2["items"].([]any)[0].(map[string]any)["order_id"] != orderIDs[0] {
		t.Fatalf("page 2: %d %s", code, p2["_raw"])
	}

	// Cancel the manager's invitation: it leaves the default list and shows in cancelled.
	if code, b := f.do(f.manager, http.MethodPost, orgPath+"/invitations/"+orderIDs[1]+"/cancel", `{"notify_guest": false}`); code != http.StatusOK {
		t.Fatalf("cancel: %d %s", code, b["_raw"])
	}
	_, active := f.do(f.manager, http.MethodGet, listPath, "")
	if n := len(active["items"].([]any)); n != 2 {
		t.Fatalf("active after a cancel: %d items, want 2 (the one still issuing counts as active): %s", n, active["_raw"])
	}
	_, cancelled := f.do(f.manager, http.MethodGet, listPath+"?state=cancelled", "")
	if ci := cancelled["items"].([]any); len(ci) != 1 || ci[0].(map[string]any)["order_id"] != orderIDs[1] {
		t.Fatalf("cancelled: %s", cancelled["_raw"])
	}
	_, all := f.do(f.manager, http.MethodGet, listPath+"?state=all", "")
	if n := len(all["items"].([]any)); n != 3 {
		t.Fatalf("all: %d items, want 3", n)
	}

	// Session and event filters.
	_, bySession := f.do(f.manager, http.MethodGet, listPath+"?state=all&session_id="+f.session.String(), "")
	_, byOther := f.do(f.manager, http.MethodGet, listPath+"?state=all&session_id="+uuid.NewString(), "")
	_, byEvent := f.do(f.manager, http.MethodGet, listPath+"?state=all&event_id="+f.event.String(), "")
	_, byOtherEvent := f.do(f.manager, http.MethodGet, listPath+"?state=all&event_id="+uuid.NewString(), "")
	if len(bySession["items"].([]any)) != 3 || len(byEvent["items"].([]any)) != 3 ||
		len(byOther["items"].([]any)) != 0 || len(byOtherEvent["items"].([]any)) != 0 {
		t.Fatalf("filters: session %s other %s event %s other event %s", bySession["_raw"], byOther["_raw"], byEvent["_raw"], byOtherEvent["_raw"])
	}

	// Bad input.
	for _, q := range []string{"?state=weird", "?limit=0", "?limit=201", "?limit=x", "?cursor=!!", "?session_id=nope", "?event_id=nope"} {
		if code, b := f.do(f.manager, http.MethodGet, listPath+q, ""); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s, want 400", q, code, b["_raw"])
		}
	}

	// Roles: an agent has no complimentary.read.
	if code, b := f.do(f.agent, http.MethodGet, listPath, ""); code != http.StatusForbidden {
		t.Fatalf("agent list: %d %s", code, b["_raw"])
	}

	// Another organization sees nothing of this one, and cannot read this one's list.
	other := newINV01Org(t, ctx, f.pool, f.secret)
	code, foreign := f.do(other.owner, http.MethodGet, "/v1/organizations/"+other.org.String()+"/invitations?state=all", "")
	if code != http.StatusOK || len(foreign["items"].([]any)) != 0 {
		t.Fatalf("foreign org own list: %d %s", code, foreign["_raw"])
	}
	if code, b := f.do(other.owner, http.MethodGet, listPath, ""); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("foreign org reads this org's list: %d %s", code, b["_raw"])
	}
	// A filter naming another organization's session finds nothing in your own list.
	code, leak := f.do(other.owner, http.MethodGet, "/v1/organizations/"+other.org.String()+"/invitations?state=all&session_id="+f.session.String(), "")
	if code != http.StatusOK || strings.Contains(leak["_raw"].(string), orderIDs[0]) || len(leak["items"].([]any)) != 0 {
		t.Fatalf("foreign org filtered by this org's session: %d %s", code, leak["_raw"])
	}
}

// inv05WithVIP is an issue body with the optional vip flag set.
func inv05WithVIP(body string, vip bool) string {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(body), &m)
	m["vip"] = vip
	raw, _ := json.Marshal(m)
	return string(raw)
}

// An invitation asked for as VIP (spec 46 section 4): the order remembers it,
// every ticket minted for it carries the mark attributed to the issuer, a
// plain invitation does not, and the flag is part of the request fingerprint.
func TestINV05_VIPInvitation_TicketsMarkedAndFingerprint(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()

	rid := uuid.New()
	vipBody := inv05WithVIP(inv02Body(rid, []string{"A|1|4", "A|1|5"}, nil, 0, "pdf_only", ""), true)
	orderID, tickets := f.issueAndMint(t, vipBody)
	if len(tickets) != 2 {
		t.Fatalf("tickets = %d, want 2", len(tickets))
	}
	var marked int
	var setBy *uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE vip), max(vip_set_by::text)::uuid FROM tickets WHERE order_id = $1`, orderID).Scan(&marked, &setBy); err != nil {
		t.Fatalf("read vip: %v", err)
	}
	if marked != 2 || setBy == nil || *setBy != f.ownerID {
		t.Fatalf("vip tickets = %d set by %v, want both marked by the owner %s", marked, setBy, f.ownerID)
	}
	var meta string
	if err := f.pool.QueryRow(ctx, `SELECT metadata->'invitation'->>'vip' FROM orders WHERE id = $1`, orderID).Scan(&meta); err != nil || meta != "true" {
		t.Fatalf("order metadata vip = %q (%v)", meta, err)
	}

	// The same request_id and body replays; with a different vip it is refused.
	if code, body := f.issue(f.o.owner, vipBody); code != http.StatusOK || body["replayed"] != true {
		t.Fatalf("replay: %d %s", code, body["_raw"])
	}
	if code, body := f.issue(f.o.owner, inv05WithVIP(vipBody, false)); code != http.StatusConflict || !strings.Contains(body["_raw"].(string), "invitation.request_id_reused") {
		t.Fatalf("replay with another vip: %d %s, want 409 invitation.request_id_reused", code, body["_raw"])
	}

	// A plain invitation (no flag at all) is not marked, and keeps working as before.
	plainID, plain := f.issueAndMint(t, inv02Body(uuid.New(), []string{"A|1|6"}, nil, 0, "pdf_only", ""))
	if len(plain) != 1 {
		t.Fatalf("plain tickets = %d", len(plain))
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE vip) FROM tickets WHERE order_id = $1`, plainID).Scan(&marked); err != nil || marked != 0 {
		t.Fatalf("a plain invitation has %d VIP tickets (%v)", marked, err)
	}
}
