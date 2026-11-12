//go:build integration

// seat_batches_new_tiers_integration_test.go — a seat batch that creates
// categories (new_tiers) through the REAL router: the answer carries
// created_tiers, the audit event created_tier_ids, every invalid request is
// refused with its own code and leaves no category behind, a revert keeps
// the categories, and the second permission: a batch with new_tiers also
// needs tier.create (the create-category route's permission), decided per
// organization, an API key by its scopes, exactly as that route decides.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// memberToken adds a member of org with role to the fixture and returns a JWT.
func (x *seatBatchFixture) memberToken(t *testing.T, org uuid.UUID, role string) string {
	t.Helper()
	tok, _ := x.memberTokenID(t, org, role)
	return tok
}

func (x *seatBatchFixture) memberTokenID(t *testing.T, org uuid.UUID, role string) (string, uuid.UUID) {
	t.Helper()
	_, secret := productionIntegrationServer(t)
	ctx := context.Background()
	user, err := x.f.q.InsertUser(ctx, "s1c-nt-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if _, err := x.f.q.InsertMembership(ctx, user.ID, org, role); err != nil {
		t.Fatalf("InsertMembership: %v", err)
	}
	x.users = append(x.users, user.ID)
	tok, _, err := auth.IssueJWT(secret, user.ID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return tok, user.ID
}

func (x *seatBatchFixture) liveTiers(t *testing.T) int {
	t.Helper()
	var n int
	if err := x.f.q.DB().QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_tiers WHERE session_id = $1 AND deleted_at IS NULL`, x.session).Scan(&n); err != nil {
		t.Fatalf("count tiers: %v", err)
	}
	return n
}

func (x *seatBatchFixture) seatTier(t *testing.T, key string) string {
	t.Helper()
	var tier string
	if err := x.f.q.DB().QueryRow(context.Background(),
		`SELECT tier_id::text FROM session_seats WHERE session_id = $1 AND seat_key = $2`, x.session, key).Scan(&tier); err != nil {
		t.Fatalf("seat %s: %v", key, err)
	}
	return tier
}

func TestSeatBatches_NewTiers_CreateAuditRefuseRevert(t *testing.T) {
	x := newSeatBatchFixture(t)
	f := x.f
	owner := x.memberToken(t, x.org, "org_admin")
	base := x.base(x.org)

	body := `{"kind":"paint",
	  "new_tiers":[{"key":"vip","name":"VIP","price_amount":5000,"color":"#00FF00"},{"key":"side","name":"Side","price_amount":0,"sort_order":7}],
	  "changes":[{"seat_key":"S|A|1","new_tier_key":"vip"},{"seat_key":"S|A|2","new_tier_key":"side"},{"seat_key":"S|A|4","new_tier_key":"vip"}]}`
	code, resp := f.do(t, http.MethodPost, base+"/seat-batches", owner, body)
	if code != http.StatusCreated {
		t.Fatalf("apply = %d %v", code, resp)
	}
	created, _ := resp["created_tiers"].([]any)
	if len(created) != 2 {
		t.Fatalf("created_tiers = %v", resp["created_tiers"])
	}
	vip, side := created[0].(map[string]any), created[1].(map[string]any)
	if vip["key"] != "vip" || vip["name"] != "VIP" || vip["price_amount"].(float64) != 5000 || vip["color"] != "#00ff00" {
		t.Fatalf("vip = %v", vip)
	}
	if side["key"] != "side" || side["color"] != nil || side["price_amount"].(float64) != 0 {
		t.Fatalf("side = %v", side)
	}
	vipID, sideID := vip["tier_id"].(string), side["tier_id"].(string)
	if ch, _ := resp["changes"].([]any); len(ch) != 2 {
		t.Fatalf("changes = %v", resp["changes"])
	}
	if sk, _ := resp["skipped"].([]any); len(sk) != 1 || sk[0].(map[string]any)["reason"] != "sold" {
		t.Fatalf("skipped = %v", resp["skipped"])
	}
	if x.seatTier(t, "S|A|1") != vipID || x.seatTier(t, "S|A|2") != sideID || x.seatTier(t, "S|A|4") != x.tier1.String() {
		t.Fatal("seats not painted as asked (the sold seat must keep its category)")
	}
	var sortOrder int32
	if err := f.q.DB().QueryRow(context.Background(), `SELECT sort_order FROM ticket_tiers WHERE id = $1`, sideID).Scan(&sortOrder); err != nil || sortOrder != 7 {
		t.Fatalf("side sort_order = %d (%v)", sortOrder, err)
	}

	var meta []byte
	if err := f.q.DB().QueryRow(context.Background(),
		`SELECT metadata FROM audit_events WHERE action = 'v1.session.seat_batch.apply' AND resource_id = $1 ORDER BY occurred_at DESC LIMIT 1`,
		x.session.String()).Scan(&meta); err != nil {
		t.Fatalf("audit: %v", err)
	}
	var md struct {
		BatchID        string   `json:"batch_id"`
		CreatedTierIDs []string `json:"created_tier_ids"`
	}
	if err := json.Unmarshal(meta, &md); err != nil || md.BatchID != resp["id"] || fmt.Sprint(md.CreatedTierIDs) != fmt.Sprint([]string{vipID, sideID}) {
		t.Fatalf("audit metadata = %s (%v)", meta, err)
	}

	// The stored batch does not carry created_tiers.
	if code, got := f.do(t, http.MethodGet, base+"/seat-batches/"+resp["id"].(string), owner, ""); code != http.StatusOK || got["created_tiers"] != nil {
		t.Fatalf("get batch = %d %v", code, got)
	}

	tiers, versionBefore := x.liveTiers(t), seatVersion(t, x)
	// Live 4 (Parter, Balcony, VIP, Side) plus 12 new is above 15.
	twelve := make([]string, 0, 12)
	manyChanges := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		twelve = append(twelve, fmt.Sprintf(`{"key":"k%d","name":"Many %d","price_amount":1}`, i, i))
		manyChanges = append(manyChanges, fmt.Sprintf(`{"seat_key":"S|Z|%d","new_tier_key":"k%d"}`, i, i))
	}
	nt := func(tiers, changes string) string {
		return `{"kind":"paint","new_tiers":[` + tiers + `],"changes":[` + changes + `]}`
	}
	one := `{"key":"a","name":"Alpha","price_amount":100}`
	useA := `{"seat_key":"S|A|3","new_tier_key":"a"}`
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"tier_id and new_tier_key", nt(one, `{"seat_key":"S|A|3","new_tier_key":"a","tier_id":"`+x.tier1.String()+`"}`), 400, "seat_batch.invalid_change"},
		{"empty new_tier_key", nt(one, useA+`,{"seat_key":"S|A|2","new_tier_key":""}`), 400, "seat_batch.invalid_change"},
		{"unknown new_tier_key", nt(one, useA+`,{"seat_key":"S|A|2","new_tier_key":"zz"}`), 400, "seat_batch.unknown_new_tier_key"},
		{"unused new tier", nt(one+`,{"key":"b","name":"Beta","price_amount":1}`, useA), 400, "seat_batch.unused_new_tier"},
		{"duplicate key", nt(one+`,{"key":"a","name":"Beta","price_amount":1}`, useA), 400, "seat_batch.duplicate_new_tier_key"},
		{"bad key", nt(`{"key":"a b","name":"Alpha","price_amount":1}`, `{"seat_key":"S|A|3","new_tier_key":"a b"}`), 400, "seat_batch.invalid_new_tier"},
		{"empty name", nt(`{"key":"a","name":"   ","price_amount":1}`, useA), 400, "seat_batch.invalid_new_tier_name"},
		{"long name", nt(`{"key":"a","name":"`+strings.Repeat("x", 101)+`","price_amount":1}`, useA), 400, "seat_batch.invalid_new_tier_name"},
		{"negative price", nt(`{"key":"a","name":"Alpha","price_amount":-1}`, useA), 400, "seat_batch.invalid_new_tier_price"},
		{"bad colour", nt(`{"key":"a","name":"Alpha","price_amount":1,"color":"green"}`, useA), 400, "seat_batch.invalid_new_tier_color"},
		{"same name twice", nt(one+`,{"key":"b","name":" alpha ","price_amount":1}`, useA+`,{"seat_key":"S|A|2","new_tier_key":"b"}`), 400, "seat_batch.duplicate_new_tier_name"},
		{"name of a live category", nt(`{"key":"a","name":"vip","price_amount":1}`, useA), 409, "seat_batch.tier_name_taken"},
		{"name of a seeded live category", nt(`{"key":"a","name":"BALCONY","price_amount":1}`, useA), 409, "seat_batch.tier_name_taken"},
		{"close kind", `{"kind":"close","new_tiers":[` + one + `],"changes":[{"seat_key":"S|A|3","status":"unavailable"}]}`, 400, "seat_batch.new_tiers_not_allowed"},
		{"too many categories", nt(strings.Join(twelve, ","), strings.Join(manyChanges, ",")), 422, "seat_batch.too_many_tiers"},
		{"foreign category next to a new one", nt(one, useA+`,{"seat_key":"S|A|2","tier_id":"`+uuid.NewString()+`"}`), 422, "seat_batch.tier_not_in_session"},
	}
	for _, c := range cases {
		code, got := f.do(t, http.MethodPost, base+"/seat-batches", owner, c.body)
		if code != c.status || prom0113Code(got) != c.code {
			t.Fatalf("%s = %d %v, want %d %s", c.name, code, got, c.status, c.code)
		}
	}
	if x.liveTiers(t) != tiers || seatVersion(t, x) != versionBefore {
		t.Fatalf("refused batches left a trace: tiers %d -> %d, version %d -> %d", tiers, x.liveTiers(t), versionBefore, seatVersion(t, x))
	}

	// Revert: seats back, the created categories stay.
	code, rev := f.do(t, http.MethodPost, base+"/seat-batches/"+resp["id"].(string)+"/revert", owner, "")
	if code != http.StatusCreated {
		t.Fatalf("revert = %d %v", code, rev)
	}
	if ct, ok := rev["created_tiers"].([]any); !ok || len(ct) != 0 {
		t.Fatalf("revert created_tiers = %v", rev["created_tiers"])
	}
	if x.seatTier(t, "S|A|1") != x.tier1.String() || x.seatTier(t, "S|A|2") != x.tier1.String() {
		t.Fatal("revert did not put the seats back")
	}
	if x.liveTiers(t) != tiers {
		t.Fatalf("revert removed categories: %d, want %d", x.liveTiers(t), tiers)
	}
}

func seatVersion(t *testing.T, x *seatBatchFixture) int64 {
	t.Helper()
	var v int64
	if err := x.f.q.DB().QueryRow(context.Background(), `SELECT seat_status_version FROM sessions WHERE id = $1`, x.session).Scan(&v); err != nil {
		t.Fatalf("version: %v", err)
	}
	return v
}

// TestSeatBatches_NewTiers_NeedTierCreate: seat-batches is gated by
// session.seats.manage, the create-category route by tier.create. The
// manager (organizer) holds the first and not the second, so a manager's
// batch may repaint but not create; an API key decides by its scopes.
func TestSeatBatches_NewTiers_NeedTierCreate(t *testing.T) {
	x := newSeatBatchFixture(t)
	f := x.f
	base := x.base(x.org)
	tierRoute := "/v1/organizations/" + x.org.String() + "/events/" + x.event.String() + "/sessions/" + x.session.String() + "/tiers"
	withNew := func(name string) string {
		return `{"kind":"paint","new_tiers":[{"key":"n","name":"` + name + `","price_amount":100}],"changes":[{"seat_key":"S|A|1","new_tier_key":"n"}]}`
	}
	plain := `{"kind":"paint","changes":[{"seat_key":"S|A|2","tier_id":"` + x.tier2.String() + `"}]}`
	tiers := x.liveTiers(t)

	denied := func(label, token string) {
		t.Helper()
		code, resp := f.do(t, http.MethodPost, base+"/seat-batches", token, withNew("Denied "+label))
		if code != http.StatusForbidden || prom0113Code(resp) != "permissions.denied" {
			t.Fatalf("%s with new_tiers = %d %v, want 403 permissions.denied", label, code, resp)
		}
		// The create-category route answers the same.
		if code, resp := f.do(t, http.MethodPost, tierRoute, token, `{"name":"Probe `+label+`","pricing_mode":"free"}`); code != http.StatusForbidden {
			t.Fatalf("%s on the create-category route = %d %v, want 403", label, code, resp)
		}
		// The batch without new_tiers still works.
		if code, resp := f.do(t, http.MethodPost, base+"/seat-batches", token, plain); code != http.StatusCreated {
			t.Fatalf("%s plain batch = %d %v", label, code, resp)
		}
	}
	denied("manager", x.manager)
	denied("seats-only key", f.key(t, x.org, "session.seats.manage"))
	if x.liveTiers(t) != tiers {
		t.Fatalf("a refused caller created a category: %d -> %d", tiers, x.liveTiers(t))
	}

	// A key with both scopes, and the owner, may create.
	for label, token := range map[string]string{
		"key with tier.create": f.key(t, x.org, "session.seats.manage", "tier.create"),
		"owner":                x.memberToken(t, x.org, "org_admin"),
	} {
		code, resp := f.do(t, http.MethodPost, base+"/seat-batches", token, withNew("Allowed "+label))
		if code != http.StatusCreated {
			t.Fatalf("%s = %d %v", label, code, resp)
		}
		if ct, _ := resp["created_tiers"].([]any); len(ct) != 1 {
			t.Fatalf("%s created_tiers = %v", label, resp["created_tiers"])
		}
	}
	if x.liveTiers(t) != tiers+2 {
		t.Fatalf("tiers = %d, want %d", x.liveTiers(t), tiers+2)
	}

	// tier.create held in ANOTHER organization does not count here: the
	// outsider owns organization B, and A's batch is not theirs at all; a
	// user who is A's manager and B's owner still may not create in A.
	both, bothID := x.memberTokenID(t, x.other, "org_admin")
	if _, err := f.q.InsertMembership(context.Background(), bothID, x.org, "organizer"); err != nil {
		t.Fatalf("InsertMembership: %v", err)
	}
	if code, resp := f.do(t, http.MethodPost, base+"/seat-batches", both, withNew("Cross org")); code != http.StatusForbidden || prom0113Code(resp) != "permissions.denied" {
		t.Fatalf("manager of A + owner of B with new_tiers in A = %d %v, want 403", code, resp)
	}
}
