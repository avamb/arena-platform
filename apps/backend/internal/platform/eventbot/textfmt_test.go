package eventbot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// ─── tfCheck ──────────────────────────────────────────────────────────────────

func TestTfCheck_PlainTextBecomesARTAndRefusalsAreNamed(t *testing.T) {
	clean, plain, ref := tfCheck("  Doors open at **19:00**.\n\n- wine\n- cheese  ", tfKindDescription)
	if ref != nil || clean != "Doors open at **19:00**.\n\n- wine\n- cheese" {
		t.Fatalf("clean = %q ref = %+v", clean, ref)
	}
	if !strings.Contains(plain, "<strong>19:00</strong>") || !strings.Contains(plain, "<ul><li>wine</li><li>cheese</li></ul>") {
		t.Fatalf("plain = %q", plain)
	}

	long := strings.Repeat("я", 4001)
	if _, _, ref := tfCheck(long, tfKindDescription); ref == nil || ref.Key != "bot.tf.err_too_long" || ref.Limit != 4000 {
		t.Errorf("4001 characters of a description: %+v", ref)
	}
	if _, _, ref := tfCheck(strings.Repeat("я", 2001), tfKindEmailNote); ref == nil || ref.Key != "bot.tf.err_too_long" || ref.Limit != 2000 {
		t.Errorf("2001 characters of a letter text: %+v", ref)
	}
	if _, _, ref := tfCheck(strings.Repeat("я", 4000), tfKindDescription); ref != nil {
		t.Errorf("4000 characters are the limit, not over it: %+v", ref)
	}
	// A "http:" word in a letter text is refused as it always was.
	if _, _, ref := tfCheck("see http://example.com", tfKindEmailNote); ref == nil || ref.Key != "bot.tf.err_scheme" {
		t.Errorf("http: in a letter text: %+v", ref)
	}
	// A tag character (hidden text for a model) is refused.
	if _, _, ref := tfCheck("hello"+string(rune(0xE0041)), tfKindDescription); ref == nil || ref.Key != "bot.tf.err_chars" {
		t.Errorf("tag character: %+v", ref)
	}
	if _, _, ref := tfCheck("  \n ", tfKindDescription); ref == nil {
		t.Error("an empty text is refused")
	}
	// The bidi embedding marks a Hebrew organizer pastes are dropped, not refused.
	clean, _, ref = tfCheck(string(rune(0x202b))+"שלום"+string(rune(0x202c)), tfKindDescription)
	if ref != nil || clean != "שלום" {
		t.Errorf("bidi marks: %q %+v", clean, ref)
	}
}

// ─── tfDecide ─────────────────────────────────────────────────────────────────

func TestTfDecide_ValidARTInEveryCase(t *testing.T) {
	typed := "Doors open at 19:00. Tickets: https://example.com/t\n\n- wine\n- cheese"
	_, plain, ref := tfCheck(typed, tfKindDescription)
	if ref != nil {
		t.Fatalf("tfCheck: %+v", ref)
	}
	good := `<p>Doors open at <strong>19:00</strong>. Tickets: <a href="https://example.com/t">https://example.com/t</a></p><ul><li>wine</li><li>cheese</li></ul>`

	cases := []struct {
		name    string
		res     FormattedText
		err     error
		applied bool
		reason  string
	}{
		{"correct markup is kept", FormattedText{HTML: good, AIApplied: true}, nil, true, ""},
		{"the route fell back", FormattedText{HTML: plain, AIApplied: false, Reason: "quota"}, nil, false, "quota"},
		{"the route fell back without a reason", FormattedText{HTML: plain}, nil, false, "not_applied"},
		{"the call failed (timeout)", FormattedText{}, context.DeadlineExceeded, false, "unavailable"},
		{"the model changed a word", FormattedText{HTML: strings.Replace(good, "wine", "beer", 1), AIApplied: true}, nil, false, "words_changed"},
		{"the model changed a number", FormattedText{HTML: strings.Replace(good, "19:00</strong>", "20:00</strong>", 1), AIApplied: true}, nil, false, "words_changed"},
		{"the model substituted a link", FormattedText{HTML: strings.Replace(good, `href="https://example.com/t"`, `href="https://evil.example/t"`, 1), AIApplied: true}, nil, false, "link_not_typed"},
		{"the model invented a link", FormattedText{HTML: strings.Replace(good, "<li>wine</li>", `<li><a href="https://x.example">wine</a></li>`, 1), AIApplied: true}, nil, false, "link_not_typed"},
		{"the answer is not text at all", FormattedText{HTML: "", AIApplied: true}, nil, false, "words_changed"},
	}
	for _, tc := range cases {
		got := tfDecide(typed, plain, tc.res, tc.err, tfKindDescription)
		if got.Applied != tc.applied || got.Reason != tc.reason {
			t.Errorf("%s: applied=%v reason=%q, want %v %q", tc.name, got.Applied, got.Reason, tc.applied, tc.reason)
		}
		// In every case the art is valid for the profile, and it is the typed
		// text's own words.
		again, err := richtext.Normalize(got.Art, richtext.FormatART, richtext.ProfileDescription)
		if err != nil || again != got.Art {
			t.Errorf("%s: art %q is not stable valid ART: %q %v", tc.name, got.Art, again, err)
		}
		if strings.Join(richtext.Words(got.Art), " ") != strings.Join(richtext.Words(plain), " ") {
			t.Errorf("%s: the words changed: %q", tc.name, got.Art)
		}
		if !tc.applied && got.Art != plain {
			t.Errorf("%s: a fallback must be the plain text, got %q", tc.name, got.Art)
		}
	}
}

func TestTfDecide_MailtoMustBeTypedAndTheLetterProfileHasNone(t *testing.T) {
	typed := "Write to info@example.com please"
	_, plain, _ := tfCheck(typed, tfKindDescription)
	ok := `<p>Write to <a href="mailto:info@example.com">info@example.com</a> please</p>`
	if got := tfDecide(typed, plain, FormattedText{HTML: ok, AIApplied: true}, nil, tfKindDescription); !got.Applied {
		t.Errorf("a typed address as a mailto link: %+v", got)
	}
	bad := strings.Replace(ok, "info@example.com\"", "boss@example.com\"", 1)
	if got := tfDecide(typed, plain, FormattedText{HTML: bad, AIApplied: true}, nil, tfKindDescription); got.Applied || got.Reason != "link_not_typed" {
		t.Errorf("another address: %+v", got)
	}
	// The letter profile takes https links only: the mailto link is stripped to
	// text by the filter, the words stay, so it is the plain text.
	_, plainNote, _ := tfCheck(typed, tfKindEmailNote)
	got := tfDecide(typed, plainNote, FormattedText{HTML: ok, AIApplied: true}, nil, tfKindEmailNote)
	if strings.Contains(got.Art, "mailto:") {
		t.Errorf("a mailto link in a letter text: %q", got.Art)
	}
}

// ─── Telegram messages ────────────────────────────────────────────────────────

func TestTgParsedLen_CountsWhatTelegramCounts(t *testing.T) {
	cases := map[string]int{
		"abc":                           3,
		"<b>a&amp;b</b>":                3,
		`<a href="https://x.y/z">q</a>`: 1,
		"😀":                             2,
		"a &lt; b":                      5,
		"<blockquote>x\ny</blockquote>": 3,
	}
	for in, want := range cases {
		if got := tgParsedLen(in); got != want {
			t.Errorf("tgParsedLen(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTgSplit_NeverCutsATagAndNeverPassesTheLimit(t *testing.T) {
	para := func(n int, c string) string { return "<b>" + strings.Repeat(c, n) + "</b>" }
	// Three paragraphs of 1900: two messages, cut between paragraphs.
	in := para(1900, "a") + "\n\n" + para(1900, "b") + "\n\n" + para(1900, "c")
	parts := tgSplit(in, 4000)
	if len(parts) != 2 || strings.Join(parts, "\n\n") != in {
		t.Fatalf("parts = %d, rejoined equal = %v", len(parts), strings.Join(parts, "\n\n") == in)
	}
	for _, p := range parts {
		if tgParsedLen(p) > 4000 || strings.Count(p, "<b>") != strings.Count(p, "</b>") {
			t.Errorf("part of %d characters, tags %d/%d", tgParsedLen(p), strings.Count(p, "<b>"), strings.Count(p, "</b>"))
		}
	}
	// A short text stays whole.
	if got := tgSplit("short", 4000); len(got) != 1 || got[0] != "short" {
		t.Errorf("short = %v", got)
	}
	// One huge list: cut between its lines.
	var lines []string
	for i := 0; i < 900; i++ {
		lines = append(lines, "• item number "+strings.Repeat("x", 10))
	}
	for _, p := range tgSplit(strings.Join(lines, "\n"), 4000) {
		if tgParsedLen(p) > 4000 {
			t.Errorf("list part of %d characters", tgParsedLen(p))
		}
	}
	// A quotation keeps its wrapper in every piece.
	var quote []string
	for i := 0; i < 600; i++ {
		quote = append(quote, "line "+strings.Repeat("q", 12))
	}
	for _, p := range tgSplit("<blockquote>"+strings.Join(quote, "\n")+"</blockquote>", 4000) {
		if !strings.HasPrefix(p, "<blockquote>") || !strings.HasSuffix(p, "</blockquote>") || tgParsedLen(p) > 4000 {
			t.Errorf("quote piece broken: %d characters", tgParsedLen(p))
		}
	}
	// One endless line: plain pieces, entities whole.
	long := strings.Repeat("a&amp;", 3000)
	for _, p := range tgSplit(long, 4000) {
		if tgParsedLen(p) > 4000 || strings.HasSuffix(p, "&amp") || strings.HasSuffix(p, "&") {
			t.Errorf("endless-line piece ends badly or is too long: %d", tgParsedLen(p))
		}
	}
}

func TestTfScreens_ShortInOneMessageLongInTwoKinds(t *testing.T) {
	texts, last := tfScreens("<b>hi</b>", "note\n\n", "Save?")
	if len(texts) != 0 || last != "<b>hi</b>\n\nnote\n\nSave?" {
		t.Fatalf("short: %v %q", texts, last)
	}
	long := strings.Repeat("я", 3995)
	texts, last = tfScreens(long, "", "Save?")
	if len(texts) != 1 || texts[0] != long || last != "Save?" {
		t.Fatalf("long: %d texts, last %d characters", len(texts), len(last))
	}
	// 4000 characters in a few paragraphs: the preview alone fits one message,
	// the buttons come in a second one.
	big := strings.Repeat(strings.Repeat("w", 1000)+"\n\n", 3) + strings.Repeat("w", 1000)
	texts, last = tfScreens(big, "n\n\n", "Save?")
	for _, p := range append(append([]string{}, texts...), last) {
		if tgParsedLen(p) > 4096 {
			t.Errorf("a message of %d characters", tgParsedLen(p))
		}
	}
	if len(texts) != 2 || last != "n\n\nSave?" {
		t.Errorf("big: %d texts, last %d characters", len(texts), len(last))
	}
}

// The four buttons carry the callbacks the dialog answers, short enough for
// Telegram, and a label in every language.
func TestTfKeyboard_FourButtonsWithShortCallbacks(t *testing.T) {
	b := &Bot{texts: nil}
	kb := b.tfKeyboard("en")
	var data []string
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			data = append(data, btn.CallbackData)
			if len(btn.CallbackData) > 64 {
				t.Errorf("callback %q is longer than 64 bytes", btn.CallbackData)
			}
		}
	}
	if strings.Join(data, ",") != "tf:s,tf:p,tf:a,tf:c" {
		t.Errorf("callbacks = %v", data)
	}
}

// ─── the dialog state ─────────────────────────────────────────────────────────

func TestTextFmtDialog_StateSurvivesAStoreRoundTripAndExpiresOnce(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	b := newTeamDialogBot(clock)
	org := uuid.New()
	art := `<p>Hello <strong>world</strong></p>`
	if err := b.tfSave(ctx, 42, org, tfStepPreview, tfDialog{Kind: string(tfKindDescription), Text: "Hello **world**", Art: art, Applied: true}); err != nil {
		t.Fatal(err)
	}
	dlg, step, found, expired := b.tfLoad(ctx, 42)
	if !found || expired || step != tfStepPreview || dlg.Art != art || dlg.Text != "Hello **world**" || !dlg.Applied || dlg.OrgID != org.String() || dlg.kind() != tfKindDescription {
		t.Fatalf("loaded = %+v step=%q found=%v expired=%v", dlg, step, found, expired)
	}
	// 29 minutes later it is still there and the clock slides on a save.
	clock.advance(29 * time.Minute)
	if _, _, found, _ := b.tfLoad(ctx, 42); !found {
		t.Fatal("the preview is gone before 30 minutes")
	}
	clock.advance(dialogTTL + time.Second)
	if _, _, found, expired := b.tfLoad(ctx, 42); found || !expired {
		t.Fatalf("found=%v expired=%v, want the lapse reported", found, expired)
	}
	if _, _, found, expired := b.tfLoad(ctx, 42); found || expired {
		t.Fatal("the lapse is reported once")
	}
	// Ending the dialog is idempotent.
	b.clearTextFmtDialog(ctx, 42)
}

// ─── the client ───────────────────────────────────────────────────────────────

func TestArenaClient_FormatText_SendsTheTextAndReadsTheAnswer(t *testing.T) {
	org := uuid.New()
	var gotPath, gotAuth string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"html":"<p>x</p>","ai_applied":true,"prompt_version":"rt04-v1"}`))
	}))
	defer srv.Close()
	c := NewArenaClient(srv.URL, "svc", srv.Client())
	out, err := c.FormatText(context.Background(), "jwt-9", org, "x", "description", "ru")
	if err != nil {
		t.Fatalf("FormatText: %v", err)
	}
	if gotPath != "/v1/organizations/"+org.String()+"/texts/format" || gotAuth != "Bearer jwt-9" {
		t.Errorf("request = %s auth=%q", gotPath, gotAuth)
	}
	if gotBody["text"] != "x" || gotBody["kind"] != "description" || gotBody["locale"] != "ru" {
		t.Errorf("body = %v", gotBody)
	}
	if out.HTML != "<p>x</p>" || !out.AIApplied || out.PromptVersion != "rt04-v1" {
		t.Errorf("out = %+v", out)
	}
	if c.http.Timeout == formatCallTimeout {
		t.Error("FormatText changed the shared client's timeout")
	}
}

func TestArenaClient_FormatText_ASlowRouteIsAnErrorTheBotAnswersWithThePlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"html":"<p>x</p>","ai_applied":true}`))
	}))
	defer srv.Close()
	c := NewArenaClient(srv.URL, "svc", srv.Client()).WithFormatTimeout(50 * time.Millisecond)
	_, err := c.FormatText(context.Background(), "jwt", uuid.New(), "x", "description", "en")
	if err == nil {
		t.Fatal("a route slower than the timeout must fail")
	}
	_, plain, _ := tfCheck("x", tfKindDescription)
	if got := tfDecide("x", plain, FormattedText{}, err, tfKindDescription); got.Applied || got.Art != plain || got.Reason != "unavailable" {
		t.Errorf("decide after a timeout = %+v", got)
	}
	var api *APIError
	if errors.As(err, &api) {
		t.Errorf("a timeout is not an API answer: %v", err)
	}
}

// ─── the wizard's side ────────────────────────────────────────────────────────

// An approved description (ART from the preview) answers the question: it is
// stored as ART, goes out as "art" and the wizard moves on.
func TestWizard_ApprovedDescriptionAnswersTheQuestion(t *testing.T) {
	r, d := loadedWith(t, artDescription, "art")
	r.press("e:desc", stXDescription)
	art := `<p>Тот самый <strong>вечер</strong></p>`
	if _, err := r.w.Apply(context.Background(), r.ws, d, WizInput{Art: art}); err != nil {
		t.Fatal(err)
	}
	if d.Step != stEditMenu || d.Event.Description != art || d.Event.DescriptionFormat != "art" {
		t.Fatalf("step %q, description %q / %q", d.Step, d.Event.Description, d.Event.DescriptionFormat)
	}
	if !d.fieldChanged("description") {
		t.Fatal("an approved description is a change")
	}
	if text, format := d.bundleDescription(); text != art || format != "art" {
		t.Fatalf("bundleDescription = %q / %q", text, format)
	}
}

// A description that enters without the preview (a poster hint) is the old
// notation read by FromLegacy: **bold** is real bold, not stars.
func TestDraft_TypedDescriptionIsConvertedLikeTheServerDoes(t *testing.T) {
	d := &Draft{}
	d.setTypedDescription("Привет, **мир**\n\n- раз\n- два")
	if d.Event.DescriptionFormat != "art" || !strings.Contains(d.Event.Description, "<strong>мир</strong>") || !strings.Contains(d.Event.Description, "<ul>") {
		t.Fatalf("description = %q / %q", d.Event.Description, d.Event.DescriptionFormat)
	}
	d.setTypedDescription("")
	if d.Event.Description != "" || d.Event.DescriptionFormat != "" {
		t.Fatalf("empty description = %q / %q", d.Event.Description, d.Event.DescriptionFormat)
	}
}
