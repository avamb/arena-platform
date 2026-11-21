package delivery

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
)

// Owner decision 2026-11-13: every buyer letter about an event is sent under
// the promoter's name, else the organization's, for every organization.
func TestApplyBrandSender(t *testing.T) {
	t.Run("promoter set -> promoter name", func(t *testing.T) {
		msg := email.Message{}
		applyBrandSender(&msg, brandSenderName(Payload{PromoterName: "  Promo Co  ", OrgName: "Org Ltd"}))
		if msg.FromName != "Promo Co" {
			t.Fatalf("FromName = %q, want promoter name", msg.FromName)
		}
	})

	t.Run("no promoter -> organization name", func(t *testing.T) {
		msg := email.Message{}
		applyBrandSender(&msg, brandSenderName(Payload{PromoterName: " ", OrgName: "  Org Ltd "}))
		if msg.FromName != "Org Ltd" {
			t.Fatalf("FromName = %q, want organization name", msg.FromName)
		}
	})

	t.Run("both empty -> FromName stays empty", func(t *testing.T) {
		msg := email.Message{}
		applyBrandSender(&msg, brandSenderName(Payload{}))
		if msg.FromName != "" {
			t.Fatalf("FromName = %q, want empty", msg.FromName)
		}
	})

	t.Run("preset FromName kept", func(t *testing.T) {
		msg := email.Message{FromName: "Einat Winery"}
		applyBrandSender(&msg, brandSenderName(Payload{PromoterName: "Promo Co", OrgName: "Org Ltd"}))
		if msg.FromName != "Einat Winery" {
			t.Fatalf("FromName = %q, want the preset name kept", msg.FromName)
		}
	})
}
