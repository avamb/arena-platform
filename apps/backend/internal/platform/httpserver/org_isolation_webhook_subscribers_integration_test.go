//go:build integration

package httpserver

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// The webhook subscriber registry is one table for every organization, so
// only a platform superadmin may use it.
func TestOrgIsolation_WebhookSubscribersAreSuperadminOnly(t *testing.T) {
	e := newSec1Env(t)
	orgB := e.org(t, "B")
	owner := e.user(t, "owner")
	e.member(t, owner, orgB, "org_admin")
	manager := e.user(t, "manager")
	e.member(t, manager, orgB, "organizer")
	ownerTok := e.token(t, owner)
	managerTok := e.token(t, manager)
	keyTok := e.key(t, orgB, "webhook.subscriber.manage")
	_, superTok := e.superadmin(t)

	count := func() int {
		var n int
		if err := e.srv.pgxPool.QueryRow(context.Background(),
			`SELECT count(*) FROM webhook_subscribers`).Scan(&n); err != nil {
			t.Fatalf("count webhook_subscribers: %v", err)
		}
		return n
	}
	before := count()
	someID := uuid.NewString()
	body := `{"url":"https://example.test/hook","events":["event.created"]}`

	for name, tok := range map[string]string{"owner": ownerTok, "manager": managerTok, "key": keyTok} {
		e.expect(t, 403, "GET", "/v1/webhooks/subscribers", tok, "", "")
		e.expect(t, 403, "GET", "/v1/webhooks/subscribers/"+someID, tok, "", "")
		e.expect(t, 403, "GET", "/v1/webhooks/subscribers/"+someID+"/recent-deliveries", tok, "", "")
		e.expect(t, 403, "POST", "/v1/webhooks/subscribers", tok, "sec3", body)
		e.expect(t, 403, "PATCH", "/v1/webhooks/subscribers/"+someID, tok, "sec3", `{"active":false}`)
		e.expect(t, 403, "DELETE", "/v1/webhooks/subscribers/"+someID, tok, "sec3", "")
		_ = name
	}
	if after := count(); after != before {
		t.Fatalf("webhook_subscribers rows changed: %d -> %d", before, after)
	}

	// Control: the superadmin console keeps working.
	e.expect(t, 200, "GET", "/v1/webhooks/subscribers", superTok, "", "")
}
