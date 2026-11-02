package eventbot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func TestDisputeErrorKey_EveryCodeHasItsOwnText(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	cases := []struct {
		err  error
		want string
	}{
		{&APIError{Status: 409, Code: "dispute.already_submitted"}, "bot.dp.err_already"},
		{&APIError{Status: 409, Code: "dispute.closed_for_evidence"}, "bot.dp.err_closed"},
		{&APIError{Status: 422, Code: "dispute.package_missing"}, "bot.dp.err_no_package"},
		{&APIError{Status: 422, Code: "dispute.key_not_writable"}, "bot.dp.err_key"},
		{&APIError{Status: 422, Code: "dispute.recipient_required"}, "bot.dp.err_recipient"},
		{&APIError{Status: 422, Code: "dispute.test_outcome_live"}, "bot.dp.err_test_live"},
		{&APIError{Status: 422, Code: "dispute.wrong_channel"}, "bot.dp.err_channel"},
		{&APIError{Status: 403, Code: "permissions.denied"}, "bot.dp.err_forbidden"},
		{&APIError{Status: 404, Code: "dispute.not_found"}, "bot.ec.not_found"},
		// The provider did not answer, or the server failed: the outcome is
		// unknown, so the text says "do not press again".
		{&APIError{Status: 503, Code: "dispute.provider_unavailable"}, "bot.dp.err_unknown"},
		{&APIError{Status: 500, Code: "internal"}, "bot.dp.err_unknown"},
		{errors.New("connection reset"), "bot.dp.err_unknown"},
		{&APIError{Status: 400, Code: "validation.failed"}, "bot.dp.err_refused"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		got := disputeErrorKey(c.err)
		if got != c.want {
			t.Errorf("%v: key %q, want %q", c.err, got, c.want)
		}
		seen[got] = true
		for _, loc := range SupportedLocales {
			if text := b.texts.T(loc, got, nil); text == "" || text == got {
				t.Errorf("%s: %q has no text", loc, got)
			}
		}
	}
	if len(seen) < 11 {
		t.Errorf("only %d different texts for the refusals", len(seen))
	}
}

// The words differ from each other and from every other typed word, the
// English one is accepted in every language, and a wrong word confirms nothing.
func TestDisputeWords_OwnPhrasesAndEnglishEverywhere(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for _, loc := range SupportedLocales {
		sub, acc := b.disputeWord(loc, dpActionSubmit), b.disputeWord(loc, dpActionAccept)
		if sub == "" || acc == "" || sub == disputeSubmitWordKey || acc == disputeAcceptWordKey {
			t.Fatalf("%s: a dispute word is missing: %q / %q", loc, sub, acc)
		}
		others := map[string]string{
			"submit": sub, "accept": acc, "refund": b.texts.T(loc, "bot.ref.word", nil),
			"order": b.ordCancelWord(loc), "session": b.sesCancelWord(loc),
		}
		seen := map[string]string{}
		for name, w := range others {
			n := normalizePhrase(w)
			if other, dup := seen[n]; dup {
				t.Fatalf("%s: the %s and %s words are the same: %q", loc, name, other, w)
			}
			seen[n] = name
		}
		if !b.isDisputeWord(loc, dpActionSubmit, sub) || b.isDisputeWord(loc, dpActionAccept, sub) {
			t.Errorf("%s: %q must submit and only submit", loc, sub)
		}
		if !b.isDisputeWord(loc, dpActionAccept, acc) || b.isDisputeWord(loc, dpActionSubmit, acc) {
			t.Errorf("%s: %q must accept and only accept", loc, acc)
		}
		if !b.isDisputeWord(loc, dpActionSubmit, " Submit. ") || !b.isDisputeWord(loc, dpActionAccept, "«accept»") {
			t.Errorf("%s: the English words must be accepted", loc)
		}
		for _, typed := range []string{"", "yes", "да", "ok", "отправить спор", "cancel"} {
			if b.isDisputeWord(loc, dpActionSubmit, typed) || b.isDisputeWord(loc, dpActionAccept, typed) {
				t.Errorf("%s: %q confirms a dispute answer", loc, typed)
			}
		}
	}
}

func TestDisputeHelpers(t *testing.T) {
	t.Parallel()
	for state, group := range map[string]string{
		"needs_response": "open", "draft_ready": "open", "submitted": "review", "under_review": "review",
		"accepted": "closed", "expired": "closed", "closed": "closed", "": "closed",
	} {
		if got := disputeGroupOf(state); got != group {
			t.Errorf("state %q is in group %q, want %q", state, got, group)
		}
	}
	if !disputeOpen("needs_response") || !disputeOpen("draft_ready") || disputeOpen("submitted") || disputeOpen("closed") {
		t.Error("only needs_response and draft_ready wait for the organizer")
	}
	if disputeKeyTag("dp-0123456789abcdef") != "0123456789ab" || disputeKeyTag("dp-abc") != "abc" {
		t.Error("the key tag is the first 12 characters after the prefix")
	}
	for n, want := range map[int]string{512: "512 B", 2048: "2 KB", 3 << 20: "3.0 MB"} {
		if got := disputeFileSize(n); got != want {
			t.Errorf("size %d = %q, want %q", n, got, want)
		}
	}
	won, lost := "won", "lost"
	if disputeChip(openapi.Dispute{State: "needs_response"}) != "⚠️" ||
		disputeChip(openapi.Dispute{State: "under_review"}) != "⏳" ||
		disputeChip(openapi.Dispute{State: "closed", Outcome: &won}) != "✅" ||
		disputeChip(openapi.Dispute{State: "closed", Outcome: &lost}) != "❌" {
		t.Error("a chip does not match the state")
	}
}

func disputeFixture(state, channel string) openapi.DisputeCard {
	due := time.Now().Add(5 * 24 * time.Hour).UTC().Format(time.RFC3339)
	oid := uuid.NewString()
	outcome := (*string)(nil)
	return openapi.DisputeCard{
		Dispute: openapi.Dispute{
			Id: uuid.New(), OrderId: &oid, OrderNumber: 1000000777, Amount: 5000, Currency: "EUR",
			EventName: "Swan Lake", ReasonGroup: "fraud", State: state, Channel: channel,
			DueBy: &due, DueBySource: "default", VenueTimezone: "Asia/Jerusalem",
			BuyerName: "Anna Buyer", BuyerEmail: "anna@example.test", Outcome: outcome,
		},
		Package: &openapi.DisputePackage{
			Gaps: []string{"gap_no_scans", "gap_no_consent", "gap_made_up"},
			Files: []openapi.DisputeFile{
				{Id: uuid.New(), Filename: "receipt.pdf", Kind: "receipt", Pages: 2, Bytes: 4096, ForProvider: true},
				{Id: uuid.New(), Filename: "full.pdf", Kind: "full", Pages: 9, Bytes: 90000, ForProvider: false},
			},
		},
		LowValue: openapi.DisputeLowValue{Enabled: true, Advised: true, Threshold: 2000},
	}
}

func disputeIdentity(role string) *Identity {
	return &Identity{Current: &Membership{Role: role}}
}

func TestDisputeCard_TextAndButtonsByRole(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	owner := disputeIdentity(membershipRoleOwner)
	manager := disputeIdentity(membershipRoleManager)
	card := disputeFixture("draft_ready", "email")

	text := b.disputeCardText("en", owner, true, card)
	for _, want := range []string{
		"№1000000777", "Swan Lake", "50 EUR", "The cardholder says they did not pay", "Evidence ready",
		"Anna Buyer", "anna@example.test", "an estimate", "Madrid time", "venue time (Asia/Jerusalem)",
		"We found no entry scan", "acceptance of the terms", "gap_made_up", "receipt.pdf", "full copy for you", "small-amount limit of 20 EUR",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the owner's card lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Only the owner") {
		t.Errorf("the owner is told that only the owner answers:\n%s", text)
	}
	// The buyer is named in a private chat only.
	group := b.disputeCardText("en", owner, false, card)
	if strings.Contains(group, "anna@example.test") || strings.Contains(group, "Anna Buyer") {
		t.Errorf("the buyer's data is in a group chat:\n%s", group)
	}
	// An estimate is not presented as the provider's date.
	card.Dispute.DueBySource = "provider"
	if strings.Contains(b.disputeCardText("en", owner, true, card), "an estimate") {
		t.Error("a provider's date is called an estimate")
	}
	card.Dispute.DueBySource = "default"

	has := func(kb string, data string) bool { return strings.Contains(kb, data) }
	flat := func(id *Identity, private bool, c openapi.DisputeCard) string {
		var sb strings.Builder
		for _, row := range b.disputeCardKeyboard("en", id, private, c).InlineKeyboard {
			for _, btn := range row {
				sb.WriteString(btn.CallbackData + " ")
			}
		}
		return sb.String()
	}
	ownerKB := flat(owner, true, card)
	for _, want := range []string{"dp:pk:", "or:v:", "dp:sub:", "dp:acc:", "dp:own:", "dp:rb:", "dp:l:open:1"} {
		if !has(ownerKB, want) {
			t.Errorf("the owner's buttons lack %q: %s", want, ownerKB)
		}
	}
	mgrKB := flat(manager, true, card)
	for _, banned := range []string{"dp:sub:", "dp:acc:", "dp:own:", "dp:rb:"} {
		if has(mgrKB, banned) {
			t.Errorf("the manager is offered %q: %s", banned, mgrKB)
		}
	}
	if !has(mgrKB, "dp:pk:") || !has(mgrKB, "or:v:") {
		t.Errorf("the manager must read the package and open the order: %s", mgrKB)
	}
	if !strings.Contains(b.disputeCardText("en", manager, true, card), "Only the owner") {
		t.Error("the manager's card does not say that only the owner answers")
	}
	// No package files in a group chat, and no answer buttons either.
	groupKB := flat(owner, false, card)
	if has(groupKB, "dp:pk:") || has(groupKB, "dp:sub:") || has(groupKB, "dp:acc:") {
		t.Errorf("a group chat gets private buttons: %s", groupKB)
	}
	// An API dispute has no "I answered myself"; an answered one has no answer buttons.
	api := disputeFixture("needs_response", "api")
	if has(flat(owner, true, api), "dp:own:") {
		t.Error("an API dispute offers 'I answered myself'")
	}
	done := disputeFixture("under_review", "api")
	if k := flat(owner, true, done); has(k, "dp:sub:") || has(k, "dp:acc:") || has(k, "dp:rb:") || !has(k, "dp:l:review:1") {
		t.Errorf("an answered dispute: %s", k)
	}
}

// Going back to the card or the list ends the typed-word step, so a word typed
// afterwards answers nothing; the final buttons and "no" keep it.
func TestNarrowDispute_OnlyItsOwnPressesKeepTheDialog(t *testing.T) {
	t.Parallel()
	clock := &testClock{t: time.Now()}
	b := ecTestBot(t)
	b.dialogs = newMemDialogStore(clock)
	ctx := context.Background()
	org := uuid.New()
	save := func() {
		st := disputeDialog{OrgID: org, DisputeID: uuid.New(), Action: dpActionSubmit, Key: "dp-x"}
		if err := b.dialogs.Save(ctx, 7, &org, disputeDialogKind, dpStepFinal, st, dialogTTL); err != nil {
			t.Fatal(err)
		}
	}
	open := func() bool {
		var st disputeDialog
		_, found, _, err := b.dialogs.Load(ctx, 7, disputeDialogKind, &st)
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	for _, keep := range []string{"dp:ok:abc", "dp:okw:abc", "dp:okl:abc", "dp:no"} {
		save()
		b.narrowDispute(ctx, 7, keep)
		if !open() {
			t.Errorf("%q ended the dialog", keep)
		}
	}
	for _, end := range []string{"dp:v:" + uuid.NewString(), "dp:l:open:1", "dp:sub:" + uuid.NewString(), "dp:pk:" + uuid.NewString()} {
		save()
		b.narrowDispute(ctx, 7, end)
		if open() {
			t.Errorf("%q left the dialog open", end)
		}
	}
}

func TestDisputeFinalButtonsAreOfThisScreen(t *testing.T) {
	t.Parallel()
	// A tag belongs to one key: a button of an older final screen has another.
	a, c := "dp-"+uuid.NewString(), "dp-"+uuid.NewString()
	if disputeKeyTag(a) == disputeKeyTag(c) {
		t.Fatal("two keys share a tag")
	}
	// The longest callback of the screens stays under Telegram's 64 bytes.
	for _, data := range []string{
		"dp:owny:" + uuid.NewString(), "dp:sub:" + uuid.NewString(), "dp:okw:" + disputeKeyTag(a), "dp:l:review:99",
	} {
		if len(data) > 64 {
			t.Errorf("callback %q is %d bytes", data, len(data))
		}
	}
}
