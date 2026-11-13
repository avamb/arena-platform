package eventbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

func TestPlanCheckAddRefusesByDeclaredSizes(t *testing.T) {
	t.Parallel()
	small := planIntakeFileRef{FileID: "a", Size: 1 << 20}
	if key, _ := planCheckAdd(nil, small); key != "" {
		t.Fatalf("a small file was refused: %q", key)
	}
	full := make([]planIntakeFileRef, planIntakeMaxFiles)
	if key, vars := planCheckAdd(full, small); key != "bot.wz.plan_in_too_many" || vars["Count"] != planIntakeMaxFiles {
		t.Fatalf("eleventh file: %q %v", key, vars)
	}
	if key, _ := planCheckAdd(nil, planIntakeFileRef{Size: planIntakeFileMax + 1}); key != "bot.wz.plan_in_too_big" {
		t.Fatalf("a big file: %q", key)
	}
	held := []planIntakeFileRef{{Size: planIntakeFileMax}, {Size: planIntakeFileMax}, {Size: planIntakeFileMax}, {Size: planIntakeFileMax}}
	if key, _ := planCheckAdd(held, planIntakeFileRef{Size: 1}); key != "bot.wz.plan_in_too_big_total" {
		t.Fatalf("total over the limit: %q", key)
	}
	// An unknown size (0) is checked after the download, not here.
	if key, _ := planCheckAdd(held, planIntakeFileRef{Size: 0}); key != "" {
		t.Fatalf("a file of unknown size: %q", key)
	}
}

func TestPlanCheckListBeforeAnyDownload(t *testing.T) {
	t.Parallel()
	if key, _ := planCheckList([]planIntakeFileRef{{Size: 5}, {Size: 6}}); key != "" {
		t.Fatalf("a fine list: %q", key)
	}
	if key, _ := planCheckList(make([]planIntakeFileRef, planIntakeMaxFiles+1)); key != "bot.wz.plan_in_too_many" {
		t.Fatalf("too many: %q", key)
	}
	if key, _ := planCheckList([]planIntakeFileRef{{Size: planIntakeFileMax + 1}}); key != "bot.wz.plan_in_too_big" {
		t.Fatalf("one too big: %q", key)
	}
	five := []planIntakeFileRef{{Size: planIntakeFileMax}, {Size: planIntakeFileMax}, {Size: planIntakeFileMax}, {Size: planIntakeFileMax}, {Size: 1}}
	if key, _ := planCheckList(five); key != "bot.wz.plan_in_too_big_total" {
		t.Fatalf("too big together: %q", key)
	}
}

// What the server says about a post decides the text, and the file the server
// names is passed on. Every one of these keeps the collected files.
func TestPlanPostRefusalMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		key  string
		name string
		ok   bool
	}{
		{&APIError{Status: 415, Code: "venue_plan_intake.unsupported_format", FileName: "a.doc"}, "bot.wz.plan_in_bad_format", "a.doc", true},
		{&APIError{Status: 415}, "bot.wz.plan_in_bad_format", "", true},
		{&APIError{Status: 400, Code: "venue_plan_intake.empty_file", FileName: "e.pdf"}, "bot.wz.plan_in_bad_format", "e.pdf", true},
		{&APIError{Status: 413, Code: "venue_plan_intake.file_too_large", FileName: "big.pdf"}, "bot.wz.plan_in_too_big_server", "big.pdf", true},
		{&APIError{Status: 413, Code: "venue_plan_intake.total_too_large"}, "bot.wz.plan_in_too_big_server", "", true},
		{&APIError{Status: 422, FileName: "x.png"}, "bot.wz.plan_in_unreadable", "x.png", true},
		{&APIError{Status: 500}, "", "", false},
		{&APIError{Status: 403}, "", "", false},
		{errors.New("network"), "", "", false},
	}
	for _, c := range cases {
		key, name, ok := planPostRefusal(c.err)
		if key != c.key || name != c.name || ok != c.ok {
			t.Errorf("%v: got %q %q %v, want %q %q %v", c.err, key, name, ok, c.key, c.name, c.ok)
		}
	}
}

// The key follows the list of files: the same list is the same request, any
// other list is a new one.
func TestPlanPostKeyFollowsTheFileList(t *testing.T) {
	t.Parallel()
	st := planIntakeDialog{Key: uuid.NewString(), Files: []planIntakeFileRef{{FileID: "a"}, {FileID: "b"}}}
	same := st
	if planPostKey(st) != planPostKey(same) {
		t.Fatal("the same list gave two keys")
	}
	more := st
	more.Files = append(append([]planIntakeFileRef{}, st.Files...), planIntakeFileRef{FileID: "c"})
	less := st
	less.Files = st.Files[:1]
	other := st
	other.Key = uuid.NewString()
	keys := map[string]bool{planPostKey(st): true, planPostKey(more): true, planPostKey(less): true, planPostKey(other): true}
	if len(keys) != 4 {
		t.Fatalf("keys collide: %v", keys)
	}
}

func TestPlanPartFileNameHasNoBreaksOrQuotes(t *testing.T) {
	t.Parallel()
	got := planPartFileName("pl\"an\r\nX-Evil: 1\n.pdf")
	if strings.ContainsAny(got, "\r\n\"") || !strings.HasSuffix(got, ".pdf") {
		t.Fatalf("name = %q", got)
	}
	if planPartFileName("\r\n") != "file" {
		t.Fatal("an empty name falls back to file")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if _, err := mw.CreateFormFile("files", planPartFileName("a\"b\r\n.pdf")); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()
	if strings.Count(buf.String(), "\r\n\r\n") != 1 {
		t.Fatalf("the part header was split: %q", buf.String())
	}
}

// The refused file can be removed by its number, and the keyboard offers it
// when the server named the file.
func TestPlanCollectKeyboardOffersTheRefusedFile(t *testing.T) {
	t.Parallel()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{texts: NewTexts(bundle)}
	st := planIntakeDialog{Wizard: true, Files: []planIntakeFileRef{{FileID: "1", Name: "a.pdf"}, {FileID: "2", Name: "b.doc"}, {FileID: "3", Name: "c.png"}}}
	has := func(kb *models.InlineKeyboardMarkup, data string) bool {
		for _, row := range kb.InlineKeyboard {
			for _, btn := range row {
				if btn.CallbackData == data {
					return true
				}
			}
		}
		return false
	}
	if kb := b.planCollectKeyboardFor("en", st, "b.doc"); !has(kb, "pi:rm:2") || !has(kb, "pi:done") || has(kb, "pi:rm:last") {
		t.Fatalf("named: %+v", kb)
	}
	if kb := b.planCollectKeyboardFor("en", st, ""); !has(kb, "pi:rm:last") || !has(kb, "pi:done") {
		t.Fatalf("unnamed: %+v", kb)
	}
	if kb := b.planCollectKeyboard("en", planIntakeDialog{Wizard: true}); has(kb, "pi:done") || has(kb, "pi:rm:last") {
		t.Fatalf("no files: %+v", kb)
	}
}

// planSent is what the stub of the Telegram API got.
type planSent struct {
	Text    string
	Buttons []string // callback data in order
}

// planNoticeBot is a Bot whose Telegram is a stub that records every message.
func planNoticeBot(t *testing.T, arenaURL string) (*Bot, func() []planSent) {
	t.Helper()
	var mu sync.Mutex
	var got []planSent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var text, markup string
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
				_ = r.ParseMultipartForm(1 << 20)
				text, markup = r.FormValue("text"), r.FormValue("reply_markup")
			} else {
				raw, _ := io.ReadAll(r.Body)
				var body struct {
					Text        string          `json:"text"`
					ReplyMarkup json.RawMessage `json:"reply_markup"`
				}
				_ = json.Unmarshal(raw, &body)
				text, markup = body.Text, string(body.ReplyMarkup)
			}
			var kb struct {
				Keyboard [][]struct {
					Data string `json:"callback_data"`
				} `json:"inline_keyboard"`
			}
			_ = json.Unmarshal([]byte(markup), &kb)
			m := planSent{Text: text}
			for _, row := range kb.Keyboard {
				for _, btn := range row {
					m.Buttons = append(m.Buttons, btn.Data)
				}
			}
			mu.Lock()
			got = append(got, m)
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":42,"type":"private"}}}`))
	}))
	t.Cleanup(srv.Close)
	tg, err := tgbot.New("123:test", tgbot.WithServerURL(srv.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	return &Bot{
			tg:     tg,
			texts:  NewTexts(bundle),
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			feed:   newFeedMemory(),
			arena:  NewArenaClient(arenaURL, "svc", nil),
		}, func() []planSent {
			mu.Lock()
			defer mu.Unlock()
			return append([]planSent(nil), got...)
		}
}

func noticeOf(kind string, org uuid.UUID, note string) openapi.BotVenuePlanNotice {
	n := openapi.BotVenuePlanNotice{
		Kind: openapi.BotVenuePlanNoticeKind(kind), IntakeId: uuid.New(), VenueId: uuid.New(), OrgId: org, VenueName: "Hall <1>",
	}
	if note != "" {
		n.Note = &note
	}
	return n
}

func identityIn(current uuid.UUID, orgs ...Membership) *Identity {
	id := &Identity{Memberships: orgs}
	for i := range orgs {
		if orgs[i].OrgID == current {
			id.Current = &orgs[i]
		}
	}
	return id
}

func notFoundArena(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	t.Cleanup(srv.Close)
	return srv
}

// The three kinds of notice each say their own thing, in the organization the
// person works in now.
func TestPlanNoticeSayTheThreeKinds(t *testing.T) {
	b, sent := planNoticeBot(t, notFoundArena(t).URL)
	org := uuid.New()
	id := identityIn(org, Membership{OrgID: org, OrgName: "Acme"})
	ctx := context.Background()
	link := gen.BotTelegramLinkRow{}

	ready := noticeOf("draft_ready", org, "")
	b.planNoticeSay(ctx, 42, "en", link, id, "jwt", ready)
	returned := noticeOf("returned", org, "Row B has 12 seats")
	b.planNoticeSay(ctx, 42, "en", link, id, "jwt", returned)
	rejected := noticeOf("rejected", org, "The scan is too dark")
	b.planNoticeSay(ctx, 42, "en", link, id, "jwt", rejected)
	// The intake cannot be read here, so the plain message is sent.
	b.planNoticeSay(ctx, 42, "en", link, id, "jwt", noticeOf("approved", org, ""))

	got := sent()
	if len(got) != 4 {
		t.Fatalf("messages = %+v", got)
	}
	if !strings.Contains(got[0].Text, "is ready") || !strings.Contains(got[0].Text, "Hall &lt;1&gt;") || got[0].Buttons[0] != "pi:rv:"+ready.IntakeId.String() {
		t.Errorf("draft_ready = %+v", got[0])
	}
	if !strings.Contains(got[1].Text, "Row B has 12 seats") || got[1].Buttons[0] != "pi:rv:"+returned.IntakeId.String() {
		t.Errorf("returned = %+v", got[1])
	}
	if !strings.Contains(got[2].Text, "The scan is too dark") || got[2].Buttons[0] != "pi:again:"+rejected.IntakeId.String() {
		t.Errorf("rejected = %+v", got[2])
	}
	if !strings.Contains(got[3].Text, "saved the seating plan") {
		t.Errorf("approved = %+v", got[3])
	}
	for _, m := range got {
		if strings.Contains(m.Text, "the organization «") {
			t.Errorf("a notice of the current organization names none: %q", m.Text)
		}
	}
}

// The escalated notice says arena could not build the plan, names the venue
// and, for another organization, the organization.
func TestPlanNoticeSayEscalated(t *testing.T) {
	b, sent := planNoticeBot(t, notFoundArena(t).URL)
	here, there := uuid.New(), uuid.New()
	id := identityIn(here, Membership{OrgID: here, OrgName: "Acme"}, Membership{OrgID: there, OrgName: "Beta Events"})
	ctx := context.Background()
	link := gen.BotTelegramLinkRow{}
	b.planNoticeSay(ctx, 42, "en", link, id, "jwt", noticeOf("escalated", here, ""))
	b.planNoticeSay(ctx, 42, "en", link, id, "jwt", noticeOf("escalated", there, ""))
	got := sent()
	if len(got) != 2 {
		t.Fatalf("messages = %+v", got)
	}
	for _, m := range got {
		if !strings.Contains(m.Text, "could not build the seating plan") || !strings.Contains(m.Text, "Hall &lt;1&gt;") || !strings.Contains(m.Text, "one working day") {
			t.Errorf("escalated = %q", m.Text)
		}
	}
	if strings.Contains(got[0].Text, "Beta Events") || !strings.Contains(got[1].Text, "Beta Events") {
		t.Errorf("organization line: %q / %q", got[0].Text, got[1].Text)
	}
}

// A notice of another organization names it, and its review button says where
// the request lives instead of "not found".
func TestPlanNoticeOfAnotherOrganizationNamesItAndItsButtonExplains(t *testing.T) {
	b, sent := planNoticeBot(t, notFoundArena(t).URL)
	here, there := uuid.New(), uuid.New()
	id := identityIn(here, Membership{OrgID: here, OrgName: "Acme"}, Membership{OrgID: there, OrgName: "Beta Events"})
	ctx := context.Background()
	link := gen.BotTelegramLinkRow{}

	n := noticeOf("draft_ready", there, "")
	b.planNoticeSay(ctx, 42, "en", link, id, "jwt", n)
	b.planNoticeSay(ctx, 42, "en", link, id, "jwt", noticeOf("approved", there, ""))
	b.planNoticeSay(ctx, 42, "en", link, id, "jwt", noticeOf("rejected", there, ""))
	got := sent()
	if len(got) != 3 {
		t.Fatalf("messages = %+v", got)
	}
	for _, m := range got {
		if !strings.Contains(m.Text, "Beta Events") {
			t.Errorf("the organization is not named: %q", m.Text)
		}
	}
	data := got[0].Buttons[0]
	if want := "pi:ro:" + orgPrefix(there) + ":" + n.IntakeId.String(); data != want || len(data) > 64 {
		t.Fatalf("button = %q, want %q within 64 bytes", data, want)
	}
	for _, d := range got[2].Buttons {
		if strings.HasPrefix(d, "pi:") {
			t.Errorf("a rejected notice of another organization offers %q", d)
		}
	}

	// The press while still in the other organization: the person is told where
	// the request is.
	b.planOtherOrg(ctx, 42, nil, &models.User{ID: 42}, id, "jwt", orgPrefix(there)+":"+n.IntakeId.String())
	if all := sent(); len(all) != 4 || !strings.Contains(all[3].Text, "belongs to «Beta Events»") || !strings.Contains(all[3].Text, "Change organization") {
		t.Fatalf("answer = %+v", all)
	}
	stranger := orgPrefix(uuid.New()) + ":" + n.IntakeId.String()
	b.planOtherOrg(ctx, 42, nil, &models.User{ID: 42}, id, "jwt", stranger)
	if all := sent(); !strings.Contains(all[4].Text, "cannot find") {
		t.Fatalf("a request of an unknown organization: %+v", all[4])
	}
}

// One chat that fails does not stop the notices of the others.
func TestPlanNoticeSafelyGoesOnAfterAFailure(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := &Bot{logger: logger, serial: newUserSerial(logger)}
	calls := 0
	b.noticeSeam = func(context.Context, int64, openapi.BotVenuePlanNotice) {
		calls++
		if calls == 1 {
			panic("boom")
		}
	}
	n := noticeOf("returned", uuid.New(), "")
	b.planNoticeSafely(context.Background(), 1, n)
	b.planNoticeSafely(context.Background(), 2, n)
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}
