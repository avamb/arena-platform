package eventbot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func TestStaffGuestName_FallsBackToTheAddress(t *testing.T) {
	t.Parallel()
	if got := staffGuestName(InviteRecipient{Name: " Anna Novak ", Email: "a@b.c"}); got != "Anna Novak" {
		t.Errorf("name = %q", got)
	}
	if got := staffGuestName(InviteRecipient{Email: "a@b.c"}); got != "a@b.c" {
		t.Errorf("a guest typed as an e-mail only is printed under the address, got %q", got)
	}
}

func TestInvitationsWebURL(t *testing.T) {
	t.Parallel()
	org, ev, se := uuid.New(), uuid.New(), uuid.New()
	want := "https://app.test/o/" + org.String() + "/events/" + ev.String() + "/sessions/" + se.String() + "/invitations"
	if got := invitationsWebURL("https://app.test/", org, ev, se); got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
	if got := invitationsWebURL("", org, ev, se); got != "" {
		t.Errorf("no base, no link: %q", got)
	}
}

func TestMiniappInvitesOffered(t *testing.T) {
	t.Parallel()
	b := &Bot{miniappInvites: true}
	if !b.miniappInvitesOffered(5, &Identity{}) {
		t.Error("flag on, private chat: offered")
	}
	if b.miniappInvitesOffered(-100, &Identity{}) {
		t.Error("a group chat has no web_app button")
	}
	if b.miniappInvitesOffered(5, &Identity{Superadmin: true}) {
		t.Error("arena-api refuses a code to a platform superadmin")
	}
	if (&Bot{}).miniappInvitesOffered(5, &Identity{}) {
		t.Error("flag off: not offered")
	}
}

func TestStaffInvRowAndCard(t *testing.T) {
	t.Parallel()
	raw := `{"order_id":"` + uuid.NewString() + `","system_id":1000000501,"status":"paid","guest_name":"Ana López","delivery":"pdf_only",
	"issued_via":"staff","issued_by":{"user_id":null,"email":"boss@example.com","first_name":"Eva","last_name":null},
	"created_at":"2099-01-01T00:00:00Z","invitation_batch_id":null,"tickets_total":2,"tickets_active":2,
	"places":[{"tier_id":"` + uuid.NewString() + `","tier_name":"Parterre","face_value":2500,"kind":"seat","seat_key":"A|1|5","sector":"A","row":"1","seat":"5","ticket_status":"active","vip":true},
	          {"tier_id":"` + uuid.NewString() + `","tier_name":"Standing","face_value":1500,"kind":"ga_unit","ticket_status":"cancelled","vip":false}],
	"session":{"id":"` + uuid.NewString() + `","start_at":"2099-10-15T18:00:00Z","event_id":"` + uuid.NewString() + `","event_name":"Swan Lake","venue_name":"Hall","timezone":"Europe/Madrid"}}`
	var it openapi.StaffInvitationOrgListItem
	if err := json.Unmarshal([]byte(raw), &it); err != nil {
		t.Fatal(err)
	}
	label := staffInvRowLabel(it)
	for _, w := range []string{"✔", "Ana López ×2", "15.10 20:00", "Swan Lake"} {
		if !strings.Contains(label, w) {
			t.Errorf("label %q lacks %q", label, w)
		}
	}
	if len([]rune(label)) > 64 {
		t.Errorf("label is too long for a button: %q", label)
	}
	lines := staffPlaceLines(it.Places)
	if !strings.Contains(lines, "🪑 A · 1 · 5 · Parterre") || !strings.Contains(lines, "✕ 🎟 Standing") {
		t.Errorf("places = %q", lines)
	}
	if !strings.Contains(lines, "⭐ VIP · 🪑 A") || strings.Count(lines, "VIP") != 1 {
		t.Errorf("only the VIP place carries the star: %q", lines)
	}
	if got := staffInvGuest(it); !strings.HasPrefix(got, "⭐ ") {
		t.Errorf("a VIP row starts with the star: %q", got)
	}
	if got := staffInvIssuer(it); got != "Eva · boss@example.com" {
		t.Errorf("issuer = %q", got)
	}
	it.TicketsActive = 0
	if staffInvChip(it) != "✕" {
		t.Error("every ticket annulled: ✕")
	}
	it.TicketsTotal = 0
	if staffInvChip(it) != "✔" {
		t.Error("tickets not issued yet counts as valid")
	}
}

func TestStaffInvitationCallbacks_FitTelegram(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"iv:ma", "iv:np:99", "iv:no:4", "iv:nrv", "iv:lg"} {
		if len(data) > 64 {
			t.Errorf("%q is %d bytes", data, len(data))
		}
	}
}

func TestStaffInvLetterLine_StateOnTheCardInEveryLanguage(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	raw := func(letter string, active int) openapi.StaffInvitationOrgListItem {
		var it openapi.StaffInvitationOrgListItem
		j := `{"order_id":"` + uuid.NewString() + `","system_id":7,"status":"paid","guest_name":"Ana","delivery":"guest_email","issued_via":"staff",
		"issued_by":{"user_id":null,"email":null,"first_name":null,"last_name":null},"created_at":"2099-01-01T00:00:00Z","invitation_batch_id":null,
		"tickets_total":2,"tickets_active":` + string(rune('0'+active)) + `,"places":[],"letter":` + letter + `,
		"session":{"id":"` + uuid.NewString() + `","start_at":"2099-10-15T18:00:00Z","event_id":"` + uuid.NewString() + `","event_name":"Swan Lake","venue_name":null,"timezone":"Europe/Madrid"}}`
		if err := json.Unmarshal([]byte(j), &it); err != nil {
			t.Fatal(err)
		}
		return it
	}
	sent := raw(`{"status":"sent","sent_at":"2026-10-29T14:05:00Z","last_error_code":null}`, 2)
	failed := raw(`{"status":"failed","sent_at":null,"last_error_code":"send_failed"}`, 2)
	for _, loc := range []string{"en", "ru", "es"} {
		if got := b.staffInvLetterLine(loc, sent); !strings.HasPrefix(got, "\n✉") || !strings.Contains(got, "29.10.2026 15:05") {
			t.Errorf("%s sent line = %q", loc, got)
		}
		if got := b.staffInvLetterLine(loc, failed); !strings.HasPrefix(got, "\n⚠") {
			t.Errorf("%s failed line = %q", loc, got)
		}
		for _, status := range []string{"pending", "skipped", "disabled"} {
			it := raw(`{"status":"`+status+`","sent_at":null,"last_error_code":null}`, 2)
			if got := b.staffInvLetterLine(loc, it); !strings.HasPrefix(got, "\n✉") {
				t.Errorf("%s %s line = %q", loc, status, got)
			}
		}
	}
	// No letter at all (pdf_only, tickets not issued yet) and a fully annulled invitation say nothing.
	if got := b.staffInvLetterLine("en", raw(`null`, 2)); got != "" {
		t.Errorf("no letter: %q", got)
	}
	if got := b.staffInvLetterLine("en", raw(`{"status":"failed","sent_at":null,"last_error_code":null}`, 0)); got != "" {
		t.Errorf("annulled invitation: %q", got)
	}
	// A failed letter marks the row, a sent one does not.
	if staffInvChip(failed) != "⚠" || staffInvChip(sent) != "✔" {
		t.Errorf("chips: failed %q sent %q", staffInvChip(failed), staffInvChip(sent))
	}
	if label := staffInvRowLabel(failed); !strings.HasPrefix(label, "⚠") {
		t.Errorf("row label = %q", label)
	}
}
