//go:build integration

// A shop letter speaks for the shop when the shop has its own name and sender
// (migration 0200): the header and the From display name carry the shop's
// name and the From address is the shop's sender, while the legal block stays
// the organization's. Without them the letter is the organization's, as
// before. Live DB through DATABASE_URL, like presentation_integration_test.go.
package delivery

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestShopLetterPayload_ShopNameAndSender(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	q := gen.New(pool)
	slug := "shop-sender-" + uuid.NewString()[:8]
	org, err := q.InsertOrganization(ctx, "Vino Test "+slug, slug, "IL", "ru", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM shop_settings WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, org.ID)
	})
	if _, err := pool.Exec(ctx, `UPDATE organizations SET legal_name = 'Vino Test Ltd' WHERE id = $1`, org.ID); err != nil {
		t.Fatalf("legal name: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO shop_settings (org_id, currency, contact_email) VALUES ($1, 'ILS', 'office@einat.example')`, org.ID); err != nil {
		t.Fatalf("shop settings: %v", err)
	}

	// No own name or sender: the organization's letter, from the platform.
	p, err := shopLetterPayload(ctx, pool, org.ID)
	if err != nil {
		t.Fatalf("shopLetterPayload: %v", err)
	}
	msg := ticketEmailMessage(p, "buyer@example.test", "s", "<p>h</p>", "t")
	if p.OrgName != org.Name || msg.From != "" || msg.FromName != "" || msg.ReplyTo != "office@einat.example" {
		t.Fatalf("without shop identity: name %q from %q (%q) reply %q", p.OrgName, msg.From, msg.FromName, msg.ReplyTo)
	}

	if _, err := pool.Exec(ctx, `UPDATE shop_settings SET shop_name = 'Einat Winery', sender_email = 'shop@einat.example' WHERE org_id = $1`, org.ID); err != nil {
		t.Fatalf("set identity: %v", err)
	}
	p, err = shopLetterPayload(ctx, pool, org.ID)
	if err != nil {
		t.Fatalf("shopLetterPayload: %v", err)
	}
	msg = ticketEmailMessage(p, "buyer@example.test", "s", "<p>h</p>", "t")
	if msg.From != "shop@einat.example" || msg.FromName != "Einat Winery" || msg.ReplyTo != "office@einat.example" {
		t.Fatalf("with shop identity: from %q (%q) reply %q", msg.From, msg.FromName, msg.ReplyTo)
	}
	if p.OrgName != "Einat Winery" || p.OrgLegalName != "Vino Test Ltd" {
		t.Fatalf("header name %q legal %q; want the shop's name and the organization's legal name", p.OrgName, p.OrgLegalName)
	}
}
