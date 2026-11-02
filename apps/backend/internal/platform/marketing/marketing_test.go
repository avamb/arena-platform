package marketing

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestHashToken_TrimsAndIsStable(t *testing.T) {
	a := HashToken("abc")
	if a != HashToken("  abc\n") {
		t.Fatal("surrounding spaces must not change the hash")
	}
	if len(a) != 64 || a == HashToken("abd") {
		t.Fatalf("unexpected hash %q", a)
	}
}

func TestNewToken_IsRandomHex(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if len(a) != 64 || a == b || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a) {
		t.Fatalf("tokens %q %q", a, b)
	}
}

func TestOffer_IsRewardCode(t *testing.T) {
	id := uuid.New()
	o := Offer{Enabled: true, Code: &gen.PromoCodeRow{ID: id}}
	if !o.IsRewardCode(id) || o.IsRewardCode(uuid.New()) {
		t.Fatal("reward code is the linked code only")
	}
	if (Offer{}).IsRewardCode(id) {
		t.Fatal("an offer without a code has no reward code")
	}
}

func TestDefaultSettings_ReminderOnOptInOff(t *testing.T) {
	s := DefaultSettings()
	if !s.CartReminder || s.OptInEnabled || s.ConsentPromoCodeID != nil {
		t.Fatalf("defaults %+v", s)
	}
}

func TestValidStatusFilter(t *testing.T) {
	for _, s := range []string{"", "all", StatusPending, StatusConfirmed, StatusWithdrawn} {
		if !ValidStatusFilter(s) {
			t.Fatalf("%q must be valid", s)
		}
	}
	if ValidStatusFilter("deleted") {
		t.Fatal("unknown status accepted")
	}
}

func TestNormalizeEmail(t *testing.T) {
	got, err := NormalizeEmail("  Anna@Example.COM ")
	if err != nil || got != "anna@example.com" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := NormalizeEmail("not an address"); err != ErrInvalidEmail {
		t.Fatalf("want ErrInvalidEmail, got %v", err)
	}
}

// The Go sources and statuses must stay inside the CHECKs of migration 0191.
func TestSourcesAndStatuses_MatchMigrationChecks(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "sql", "0191_cart_reminders_marketing_consent.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, v := range []string{SourceWidget, SourceConfirmLink, SourceWithdraw, SourceOrganizer, SourceErasure,
		StatusPending, StatusConfirmed, StatusWithdrawn} {
		if !strings.Contains(sql, "'"+v+"'") {
			t.Errorf("migration 0191 does not list %q", v)
		}
	}
}

func TestLinks(t *testing.T) {
	if WithdrawPath("t") != "/v1/public/marketing/withdraw?token=t" || ConfirmPath("t") != "/v1/public/marketing/confirm?token=t" {
		t.Fatal("link paths changed: the routes must change with them")
	}
}
