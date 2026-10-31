//go:build integration

// Telegram sales bot and staff invitations, task INV-12 (spec 46, section
// 3.7, decision R-11): an invitation issued and then cancelled through the
// real router is read back by the sales bot's store with WHO issued it and
// WHO cancelled it.
package httpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
)

// inv12Issue issues one pdf_only invitation for a seat, mints its tickets
// (the worker's job) and answers the order and its ticket.
func inv12Issue(t *testing.T, f *inv02Fx, token, seat string) (orderID, ticketID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	code, body := f.issue(token, inv02Body(uuid.New(), []string{seat}, nil, 0, "pdf_only", ""))
	if code != http.StatusCreated {
		t.Fatalf("issue %s: %d %s", seat, code, body["_raw"])
	}
	orderID = uuid.MustParse(body["order_id"].(string))
	var csID uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT checkout_session_id FROM orders WHERE id = $1`, orderID).Scan(&csID); err != nil {
		t.Fatalf("checkout session: %v", err)
	}
	cs, err := gen.New(f.pool).GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("checkout session row: %v", err)
	}
	tickets, err := f.srv.ticketsHandler().IssueTicketsForCheckout(ctx, cs)
	if err != nil || len(tickets) != 1 {
		t.Fatalf("IssueTicketsForCheckout: %d tickets, %v", len(tickets), err)
	}
	return orderID, tickets[0].ID
}

func inv12Cancel(t *testing.T, f *inv02Fx, token string, orderID uuid.UUID) {
	t.Helper()
	path := "/v1/organizations/" + f.o.org.String() + "/invitations/" + orderID.String() + "/cancel"
	if code, body := f.do(token, http.MethodPost, path, `{"notify_guest": false}`); code != http.StatusOK {
		t.Fatalf("cancel: %d %s", code, body["_raw"])
	}
}

func inv12Email(t *testing.T, f *inv02Fx, id uuid.UUID) string {
	t.Helper()
	var email string
	if err := f.pool.QueryRow(context.Background(), `SELECT email FROM users WHERE id = $1`, id).Scan(&email); err != nil {
		t.Fatalf("user email: %v", err)
	}
	return email
}

func TestINV12_SalesBotNamesWhoIssuedAndCancelledAnInvitation(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	q := gen.New(f.pool)
	store := salesnotify.NewPGStore(f.pool)

	var managerID uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT user_id FROM memberships WHERE org_id = $1 AND role = 'organizer'`, f.o.org).Scan(&managerID); err != nil {
		t.Fatalf("manager id: %v", err)
	}
	ownerEmail, managerEmail := inv12Email(t, f, f.ownerID), inv12Email(t, f, managerID)

	// An organization API key that may issue and revoke invitations.
	creator, err := q.InsertUser(ctx, "inv12-key-owner-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	keyName := "WP site " + uuid.NewString()[:6]
	_, rawKey, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(q), apikeys.IssueInput{
		OrgID: f.o.org, Name: keyName, Scopes: []string{"complimentary.issue", "complimentary.revoke", "complimentary.read"}, CreatedBy: creator.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}

	ownerOrder, ownerTicket := inv12Issue(t, f, f.o.owner, "A|1|4")
	mgrOrder, mgrTicket := inv12Issue(t, f, f.manager, "A|1|5")
	keyOrder, keyTicket := inv12Issue(t, f, rawKey, "A|1|6")

	// The metadata carries the key's name; a user's invitation does not.
	var keyMeta, ownerMeta string
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(metadata->'invitation'->>'issued_by_api_key', '') FROM orders WHERE id = $1`, keyOrder).Scan(&keyMeta); err != nil || keyMeta != keyName {
		t.Fatalf("issued_by_api_key = %q (%v), want %q", keyMeta, err, keyName)
	}
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(metadata->'invitation'->>'issued_by_api_key', '') FROM orders WHERE id = $1`, ownerOrder).Scan(&ownerMeta); err != nil || ownerMeta != "" {
		t.Fatalf("a user's invitation carries issued_by_api_key %q (%v)", ownerMeta, err)
	}

	// Issued by.
	for _, c := range []struct {
		name  string
		order uuid.UUID
		want  salesnotify.Staff
	}{
		{"owner", ownerOrder, salesnotify.Staff{Role: "Owner", Email: ownerEmail}},
		{"manager", mgrOrder, salesnotify.Staff{Role: "Manager", Email: managerEmail}},
		{"api key", keyOrder, salesnotify.Staff{Role: "API key", Label: keyName}},
	} {
		sale, err := store.SaleByOrder(ctx, c.order.String())
		if err != nil {
			t.Fatalf("%s: SaleByOrder: %v", c.name, err)
		}
		if sale.IssuedBy != c.want || sale.Source != "complimentary" {
			t.Fatalf("%s: IssuedBy = %+v source %s, want %+v", c.name, sale.IssuedBy, sale.Source, c.want)
		}
		if msg := salesnotify.FormatSale(sale); !strings.Contains(msg, "🙋 Issued by: "+c.want.Role) || !strings.Contains(msg, "Total: <b>0.00") {
			t.Fatalf("%s: message\n%s", c.name, msg)
		}
	}

	// A platform operator without a membership: the issuer is a user holding
	// a platform-wide role.
	op, err := q.InsertUser(ctx, "inv12-op-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser operator: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL LIMIT 1`, op.ID); err != nil {
		t.Fatalf("grant platform role: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM user_roles WHERE user_id = $1`, op.ID)
	})
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET issued_by = $2 WHERE id = $1`, ownerOrder, op.ID); err != nil {
		t.Fatalf("re-point issuer: %v", err)
	}
	sale, err := store.SaleByOrder(ctx, ownerOrder.String())
	if err != nil || sale.IssuedBy != (salesnotify.Staff{Role: "Platform operator", Email: op.Email}) {
		t.Fatalf("operator: IssuedBy = %+v (%v)", sale.IssuedBy, err)
	}

	// An order without issued_via (an old gateway invitation) prints nothing.
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET issued_via = NULL WHERE id = $1`, mgrOrder); err != nil {
		t.Fatalf("clear issued_via: %v", err)
	}
	if sale, err := store.SaleByOrder(ctx, mgrOrder.String()); err != nil || !sale.IssuedBy.IsZero() {
		t.Fatalf("an order without issued_via: IssuedBy = %+v (%v)", sale.IssuedBy, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET issued_via = 'staff' WHERE id = $1`, mgrOrder); err != nil {
		t.Fatalf("restore issued_via: %v", err)
	}

	// Before any cancellation nobody is named as the canceller.
	if r, err := store.RefundByTicket(ctx, ownerTicket.String()); err != nil || !r.CancelledBy.IsZero() {
		t.Fatalf("before the cancel: CancelledBy = %+v (%v)", r.CancelledBy, err)
	}

	// Cancelled by: the manager cancels the owner's invitation, the owner the
	// manager's, the key its own.
	inv12Cancel(t, f, f.manager, ownerOrder)
	inv12Cancel(t, f, f.o.owner, mgrOrder)
	inv12Cancel(t, f, rawKey, keyOrder)
	for _, c := range []struct {
		name   string
		ticket uuid.UUID
		want   salesnotify.Staff
	}{
		{"manager cancelled", ownerTicket, salesnotify.Staff{Role: "Manager", Email: managerEmail}},
		{"owner cancelled", mgrTicket, salesnotify.Staff{Role: "Owner", Email: ownerEmail}},
		{"key cancelled", keyTicket, salesnotify.Staff{Role: "API key", Label: keyName}},
	} {
		r, err := store.RefundByTicket(ctx, c.ticket.String())
		if err != nil {
			t.Fatalf("%s: RefundByTicket: %v", c.name, err)
		}
		if r.CancelledBy != c.want || r.Amount != 0 {
			t.Fatalf("%s: CancelledBy = %+v amount %d, want %+v", c.name, r.CancelledBy, r.Amount, c.want)
		}
		if msg := salesnotify.FormatRefund(r); !strings.Contains(msg, "🙋 Cancelled by: "+c.want.Role) || !strings.Contains(msg, "Ticket cancelled") {
			t.Fatalf("%s: message\n%s", c.name, msg)
		}
	}
	// The issuer is untouched by the cancellation.
	if sale, err := store.SaleByOrder(ctx, keyOrder.String()); err != nil || sale.IssuedBy.Label != keyName {
		t.Fatalf("after the cancel: IssuedBy = %+v (%v)", sale.IssuedBy, err)
	}
}
