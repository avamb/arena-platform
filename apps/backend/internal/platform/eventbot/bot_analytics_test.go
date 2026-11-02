package eventbot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
)

// memSink is an in-memory analytics.Tracker.
type memSink struct {
	mu     sync.Mutex
	events []analytics.Event
}

func (s *memSink) Track(_ context.Context, ev analytics.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *memSink) all() []analytics.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]analytics.Event(nil), s.events...)
}

func (s *memSink) named(name string) []analytics.Event {
	var out []analytics.Event
	for _, e := range s.all() {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

// fakeAnalyticsDB answers the reads tracking makes.
type fakeAnalyticsDB struct {
	links   map[int64]gen.BotTelegramLinkRow
	drafts  map[int64]string // telegram id -> wizard step
	dialogs map[int64]gen.BotDialogRow
	reads   int
}

func (f *fakeAnalyticsDB) GetBotTelegramLink(_ context.Context, tg int64) (gen.BotTelegramLinkRow, error) {
	f.reads++
	if l, ok := f.links[tg]; ok {
		return l, nil
	}
	return gen.BotTelegramLinkRow{}, pgx.ErrNoRows
}

func (f *fakeAnalyticsDB) GetBotDraft(_ context.Context, tg int64, _ uuid.UUID) (gen.BotDraftRow, error) {
	if step, ok := f.drafts[tg]; ok {
		return gen.BotDraftRow{Step: step}, nil
	}
	return gen.BotDraftRow{}, pgx.ErrNoRows
}

func (f *fakeAnalyticsDB) ListBotDialogKindsByRecency(_ context.Context, tg int64) ([]string, error) {
	if d, ok := f.dialogs[tg]; ok {
		return []string{d.Kind}, nil
	}
	return nil, nil
}

func (f *fakeAnalyticsDB) GetBotDialog(_ context.Context, tg int64, _ string) (gen.BotDialogRow, error) {
	if d, ok := f.dialogs[tg]; ok {
		return d, nil
	}
	return gen.BotDialogRow{}, pgx.ErrNoRows
}

var analyticsTestNow = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

func newTestAnalytics(db *fakeAnalyticsDB) (*botAnalytics, *memSink) {
	sink := &memSink{}
	a := newBotAnalytics(sink, "test-signing-secret", db)
	a.now = func() time.Time { return analyticsTestNow }
	return a, sink
}

func TestBotAnalytics_NilIsANoOp(t *testing.T) {
	if newBotAnalytics(nil, "s", nil) != nil {
		t.Fatal("no sink must give a nil recorder")
	}
	if newBotAnalytics((*analytics.Writer)(nil), "s", nil) != nil {
		t.Fatal("a typed-nil writer must give a nil recorder")
	}
	var a *botAnalytics
	a.track(context.Background(), 1, nil, "bot_update", map[string]any{"kind": "message"})
	a.update(context.Background(), &models.Update{}, &models.User{ID: 1})
	b := &Bot{}
	b.trackWizard(context.Background(), 1, uuid.New(), wizardTurn{}, NewDraft(), ErrCancelled)
	b.trackPosterRefusal(context.Background(), 1, uuid.New(), NewDraft(), "poster_too_small")
}

func TestBotAnalytics_IdentityLinkedUserOrKeyedTelegramHash(t *testing.T) {
	user, org := uuid.New(), uuid.New()
	revoked := analyticsTestNow
	db := &fakeAnalyticsDB{links: map[int64]gen.BotTelegramLinkRow{
		10: {TelegramUserID: 10, UserID: user, CurrentOrgID: &org},
		11: {TelegramUserID: 11, UserID: uuid.New(), RevokedAt: &revoked},
	}}
	a, sink := newTestAnalytics(db)
	ctx := context.Background()

	a.track(ctx, 10, nil, "bot_update", map[string]any{"kind": "message"})
	a.track(ctx, 11, nil, "bot_update", map[string]any{"kind": "message"})
	a.track(ctx, 12, nil, "bot_update", map[string]any{"kind": "message"})
	evs := sink.all()
	if len(evs) != 3 {
		t.Fatalf("events = %d", len(evs))
	}
	if evs[0].IDKind != analytics.IDKindUser || evs[0].IDValue != user.String() || evs[0].OrgID == nil || *evs[0].OrgID != org {
		t.Fatalf("linked: %+v", evs[0])
	}
	want := analytics.NewTGHasher("test-signing-secret")
	for i, tg := range map[int]int64{1: 11, 2: 12} {
		e := evs[i]
		if e.IDKind != analytics.IDKindTG || e.IDValue != want.Hash(tg) {
			t.Fatalf("tg %d: %+v", tg, e)
		}
		if e.Surface != analytics.SurfaceBot {
			t.Fatalf("surface = %q", e.Surface)
		}
	}
	// The raw Telegram id appears nowhere in an event.
	raw, _ := json.Marshal(evs[1:])
	if strings.Contains(string(raw), `"11"`) || strings.Contains(string(raw), `:11,`) {
		t.Fatalf("raw telegram id leaked: %s", raw)
	}
	// An explicit organization wins over the account's current one.
	other := uuid.New()
	a.track(ctx, 10, &other, "bot_update", map[string]any{"kind": "message"})
	if e := sink.all()[3]; e.OrgID == nil || *e.OrgID != other {
		t.Fatalf("explicit org: %+v", e)
	}
}

func TestBotAnalytics_WhoIsCachedAndAFailedReadIsNot(t *testing.T) {
	db := &fakeAnalyticsDB{links: map[int64]gen.BotTelegramLinkRow{}}
	a, _ := newTestAnalytics(db)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		a.track(ctx, 5, nil, "bot_update", map[string]any{"kind": "message"})
	}
	if db.reads != 1 {
		t.Fatalf("link reads = %d, want 1 (cached)", db.reads)
	}
	// A stranger who then links is seen as the user at once after forget, and
	// otherwise once the cache expires.
	db.links[5] = gen.BotTelegramLinkRow{UserID: uuid.New()}
	if e := a.identify(ctx, 5); e.kind != analytics.IDKindTG {
		t.Fatalf("before forget: %+v", e)
	}
	a.forget(5)
	if e := a.identify(ctx, 5); e.kind != analytics.IDKindUser {
		t.Fatalf("after forget: %+v", e)
	}
	a.forget(5)
	db.links[5] = gen.BotTelegramLinkRow{}
	delete(db.links, 5)
	a.identify(ctx, 5)
	db.links[5] = gen.BotTelegramLinkRow{UserID: uuid.New()}
	a.now = func() time.Time { return analyticsTestNow.Add(analyticsWhoTTL + time.Second) }
	if e := a.identify(ctx, 5); e.kind != analytics.IDKindUser {
		t.Fatalf("after expiry: %+v", e)
	}
}

func TestUpdateKind(t *testing.T) {
	cb := func(data string) *models.Update {
		return &models.Update{CallbackQuery: &models.CallbackQuery{Data: data}}
	}
	msg := func(text string) *models.Update { return &models.Update{Message: &models.Message{Text: text}} }
	cases := []struct {
		u            *models.Update
		kind, prefix string
	}{
		{cb("wz:age:16+"), "callback", "wz"},
		{cb("wz:dc:2099-12-15"), "callback", "wz"},
		{cb("home"), "callback", "home"},
		{cb("event:" + uuid.NewString() + ":1"), "callback", "event"},
		{cb("weird data with spaces"), "callback", ""},
		{msg("/start inv_secretcode"), "command", "start"},
		{msg("/Menu@ArenaBot"), "command", "menu"},
		{msg("Бот-концерт secret title"), "message", ""},
		{&models.Update{}, "", ""},
	}
	for _, c := range cases {
		kind, prefix := updateKind(c.u)
		if kind != c.kind || prefix != c.prefix {
			t.Errorf("updateKind = (%q, %q), want (%q, %q)", kind, prefix, c.kind, c.prefix)
		}
	}
}

func TestBotAnalytics_CurrentStepFromDialogThenDraft(t *testing.T) {
	org := uuid.New()
	db := &fakeAnalyticsDB{
		links:   map[int64]gen.BotTelegramLinkRow{1: {UserID: uuid.New(), CurrentOrgID: &org}, 2: {UserID: uuid.New(), CurrentOrgID: &org}},
		drafts:  map[int64]string{1: stSDate, 2: stSDate},
		dialogs: map[int64]gen.BotDialogRow{2: {Kind: "team", Step: "email", ExpiresAt: analyticsTestNow.Add(time.Minute)}},
	}
	a, _ := newTestAnalytics(db)
	ctx := context.Background()
	if got := a.currentStep(ctx, 1); got != stSDate {
		t.Fatalf("draft step = %q", got)
	}
	if got := a.currentStep(ctx, 2); got != "team:email" {
		t.Fatalf("dialog step = %q", got)
	}
	db.dialogs[2] = gen.BotDialogRow{Kind: "team", Step: "email", ExpiresAt: analyticsTestNow.Add(-time.Minute)}
	if got := a.currentStep(ctx, 2); got != stSDate {
		t.Fatalf("expired dialog must fall back to the draft, got %q", got)
	}
	if got := a.currentStep(ctx, 3); got != "" {
		t.Fatalf("unknown account step = %q", got)
	}
}

// handleUpdate records bot_update inside the sender's turn, before the route.
func TestBot_HandleUpdateTracksBotUpdate(t *testing.T) {
	org := uuid.New()
	db := &fakeAnalyticsDB{
		links:  map[int64]gen.BotTelegramLinkRow{42: {UserID: uuid.New(), CurrentOrgID: &org}},
		drafts: map[int64]string{42: stVName},
	}
	stub := newStubBotAPI(t)
	b := newStubbedBot(t, stub)
	var sink *memSink
	b.an, sink = newTestAnalytics(db)

	routed := make(chan struct{}, 4)
	b.route = func(_ context.Context, _ *models.Update) { routed <- struct{}{} }
	secretTitle := "Моё секретное название"
	updates := []*models.Update{
		{Message: &models.Message{Text: secretTitle, Chat: models.Chat{ID: 42, Type: "private"}, From: &models.User{ID: 42}}},
		{Message: &models.Message{Text: "/new", Chat: models.Chat{ID: 42, Type: "private"}, From: &models.User{ID: 42}}},
		{CallbackQuery: &models.CallbackQuery{ID: "1", Data: "wz:age:16+", From: models.User{ID: 42}}},
	}
	for _, u := range updates {
		b.handleUpdate(context.Background(), nil, u)
	}
	for range updates {
		select {
		case <-routed:
		case <-time.After(5 * time.Second):
			t.Fatal("update not routed")
		}
	}
	evs := sink.named("bot_update")
	if len(evs) != 3 {
		t.Fatalf("bot_update events = %d", len(evs))
	}
	want := []struct{ kind, prefix string }{{"message", ""}, {"command", "new"}, {"callback", "wz"}}
	for i, w := range want {
		p := evs[i].Props
		if p["kind"] != w.kind || p["step"] != stVName {
			t.Errorf("event %d props = %v", i, p)
		}
		if w.prefix == "" {
			if _, has := p["prefix"]; has {
				t.Errorf("event %d must carry no prefix: %v", i, p)
			}
		} else if p["prefix"] != w.prefix {
			t.Errorf("event %d prefix = %v", i, p["prefix"])
		}
	}
	raw, _ := json.Marshal(sink.all())
	if strings.Contains(string(raw), "секретное") || strings.Contains(string(raw), "16+") {
		t.Fatalf("typed text or button payload leaked into events: %s", raw)
	}
}

// A refused (rate-limited) update is not an admitted one: no bot_update.
func TestBot_HandleUpdateRefusedIsNotTracked(t *testing.T) {
	stub := newStubBotAPI(t)
	b := newStubbedBot(t, stub)
	b.queries = nil // the refusal answer then reads no locale from a database
	var sink *memSink
	b.an, sink = newTestAnalytics(&fakeAnalyticsDB{})
	b.limiter = newUserRateLimiter(1, time.Hour)
	routed := make(chan struct{}, 4)
	b.route = func(context.Context, *models.Update) { routed <- struct{}{} }
	msg := func() *models.Update {
		return &models.Update{Message: &models.Message{Text: "hi", Chat: models.Chat{ID: 5, Type: "private"}, From: &models.User{ID: 5}}}
	}
	b.handleUpdate(context.Background(), nil, msg())
	b.handleUpdate(context.Background(), nil, msg())
	select {
	case <-routed:
	case <-time.After(5 * time.Second):
		t.Fatal("first update not routed")
	}
	// The refusal runs in the same lane after the first update; wait for it.
	if err := b.serial.submit(5, func() { routed <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-routed:
	case <-time.After(5 * time.Second):
		t.Fatal("lane did not drain")
	}
	if got := len(sink.named("bot_update")); got != 1 {
		t.Fatalf("bot_update events = %d, want 1 (the refused one is not tracked)", got)
	}
}

// trackedApply is wizardApply's analytics half: snapshot, Apply, record.
func trackedApply(t *testing.T, b *Bot, r *wizardRun, in WizInput) error {
	t.Helper()
	turn := newWizardTurn(r.d)
	_, err := r.w.Apply(context.Background(), r.ws, r.d, in)
	b.trackWizard(context.Background(), 7, r.ws.OrgID, turn, r.d, err)
	return err
}

func TestWizardAnalytics_StepsSecondsErrorsAndExits(t *testing.T) {
	r := newWizardRun(t, &fakeRefs{})
	b := &Bot{}
	var sink *memSink
	b.an, sink = newTestAnalytics(&fakeAnalyticsDB{})
	clock := analyticsTestNow
	b.an.now = func() time.Time { return clock }
	r.d.StepSince = clock.Unix()

	// An over-long name is refused: an input error with a code, no step move.
	clock = clock.Add(5 * time.Second)
	if err := trackedApply(t, b, r, WizInput{Text: strings.Repeat("Ы", 201)}); err != nil {
		t.Fatal(err)
	}
	if got := sink.named("wizard_input_error"); len(got) != 1 || got[0].Props["step"] != stEvName || got[0].Props["reason"] != "name_invalid" {
		t.Fatalf("input errors = %+v", got)
	}
	if len(sink.named("wizard_step")) != 0 {
		t.Fatal("a refused answer must not move the step")
	}

	// A good name moves ev_name -> ev_age after 8 seconds on the first step.
	clock = clock.Add(3 * time.Second)
	if err := trackedApply(t, b, r, WizInput{Text: "Секретный концерт"}); err != nil {
		t.Fatal(err)
	}
	steps := sink.named("wizard_step")
	if len(steps) != 1 || steps[0].Props["from"] != stEvName || steps[0].Props["to"] != stEvAge ||
		steps[0].Props["seconds"] != float64(8) || steps[0].Props["mode"] != ModeCreate {
		t.Fatalf("steps = %+v", steps)
	}
	if r.d.StepSince != clock.Unix() {
		t.Fatal("the draft must be stamped with the time it reached the new step")
	}

	// A typed date nobody can read is a date_unparsed.
	r.d.Step = stSDate
	if err := trackedApply(t, b, r, WizInput{Text: "32.13.2099"}); err != nil {
		t.Fatal(err)
	}
	// A date in the past is out of range.
	r.d.Step = stSDate
	if err := trackedApply(t, b, r, WizInput{Data: "d:2001-01-01"}); err != nil {
		t.Fatal(err)
	}
	reasons := []string{}
	for _, e := range sink.named("wizard_input_error") {
		reasons = append(reasons, e.Props["reason"].(string))
	}
	if got := strings.Join(reasons, ","); got != "name_invalid,date_unparsed,date_past" {
		t.Fatalf("reasons = %s", got)
	}

	// Cancel opens the confirmation (a step and an exit "cancel"), keep and
	// drop end the wizard.
	r.d.Step = stEvAge
	if err := trackedApply(t, b, r, WizInput{Data: "cancel"}); err != nil {
		t.Fatal(err)
	}
	if err := trackedApply(t, b, r, WizInput{Data: "cancel:keep"}); !errors.Is(err, ErrSavedExit) {
		t.Fatalf("keep err = %v", err)
	}
	r.d.Step = stEvAge
	_ = trackedApply(t, b, r, WizInput{Data: "cancel"})
	if err := trackedApply(t, b, r, WizInput{Data: "cancel:drop"}); !errors.Is(err, ErrCancelled) {
		t.Fatalf("drop err = %v", err)
	}
	var exits []string
	for _, e := range sink.named("wizard_exit") {
		exits = append(exits, e.Props["how"].(string)+"@"+e.Props["step"].(string))
	}
	if got := strings.Join(exits, ","); got != "cancel@ev_age,keep@ev_age,cancel@ev_age,drop@ev_age" {
		t.Fatalf("exits = %s", got)
	}

	// Nothing the person typed is in any event.
	raw, _ := json.Marshal(sink.all())
	for _, leak := range []string{"Секретный", "32.13", "ЫЫЫ"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("%q leaked into %s", leak, raw)
		}
	}
	// Every event passes the dictionary untouched: nothing it would drop.
	for _, e := range sink.all() {
		clean, ok := analytics.Sanitize(analytics.SurfaceBot, e.Name, e.Props, true)
		if !ok || len(clean) != len(e.Props) {
			t.Errorf("%s props %v are not all in the dictionary (kept %v)", e.Name, e.Props, clean)
		}
	}
}

func TestWizardAnalytics_PosterRefusalAndSecondsOmittedWhenUnknown(t *testing.T) {
	b := &Bot{}
	var sink *memSink
	b.an, sink = newTestAnalytics(&fakeAnalyticsDB{})
	d := NewDraft()
	d.Step = stEvPoster
	b.trackPosterRefusal(context.Background(), 7, uuid.New(), d, "bot.wz.poster_too_small")
	b.trackPosterRefusal(context.Background(), 7, uuid.New(), d, "poster_is_pdf")
	evs := sink.named("wizard_input_error")
	if len(evs) != 2 || evs[0].Props["reason"] != "poster_too_small" || evs[1].Props["reason"] != "poster_is_pdf" || evs[0].Props["step"] != stEvPoster {
		t.Fatalf("events = %+v", evs)
	}

	r := newWizardRun(t, &fakeRefs{})
	turn := newWizardTurn(r.d) // StepSince 0: unknown
	_, err := r.w.Apply(context.Background(), r.ws, r.d, WizInput{Text: "Концерт"})
	b.trackWizard(context.Background(), 7, r.ws.OrgID, turn, r.d, err)
	steps := sink.named("wizard_step")
	if len(steps) != 1 {
		t.Fatalf("steps = %d", len(steps))
	}
	if _, has := steps[0].Props["seconds"]; has {
		t.Fatalf("seconds must be left out when the step time is unknown: %v", steps[0].Props)
	}
}

// Every refusal message key of the wizard has an explicit reason code.
func TestWizardReasonCodes_CoverEveryErrKey(t *testing.T) {
	keys := regexp.MustCompile(`"bot\.wz\.(err_[a-z_]+)"`)
	for _, file := range []string{"wizard_keys.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range keys.FindAllStringSubmatch(string(src), -1) {
			if _, ok := wizardReasonCodes[m[1]]; !ok {
				t.Errorf("%s has no reason code in wizardReasonCodes", m[1])
			}
		}
	}
	// Codes are short lower-case tokens and unique per key.
	seen := map[string]string{}
	for key, code := range wizardReasonCodes {
		if !regexp.MustCompile(`^[a-z0-9_]{3,64}$`).MatchString(code) {
			t.Errorf("code %q of %s is not a plain token", code, key)
		}
		if prev, dup := seen[code]; dup {
			t.Errorf("code %q is used by both %s and %s", code, prev, key)
		}
		seen[code] = key
	}
}

// The draft sweep reports its two events with the draft's step and org.
func TestDraftSweep_TracksReminderAndExpiry(t *testing.T) {
	gone := draftRow(99, "en", "Тайное имя", sweepNow.Add(-8*24*time.Hour))
	due := draftRow(98, "en", "Тайное имя", sweepNow.Add(-25*time.Hour))
	for _, r := range []*gen.BotDraftReminderRow{&gone, &due} {
		r.State, _ = json.Marshal(map[string]any{"version": 4, "step": stSDate, "event": map[string]any{"name": "Тайное имя"}})
	}
	a, sink := newTestAnalytics(&fakeAnalyticsDB{})
	s := newTestSweeper(t, newMemDrafts(gone, due), &fakeSender{})
	s.track = func(ctx context.Context, r gen.BotDraftReminderRow, event string, props map[string]any) {
		o := r.OrgID
		a.track(ctx, r.TelegramUserID, &o, event, props)
	}
	s.sweep(context.Background(), sweepNow)
	exit := sink.named("wizard_exit")
	rem := sink.named("draft_reminder")
	if len(exit) != 1 || exit[0].Props["how"] != "expired" || exit[0].Props["step"] != stSDate || *exit[0].OrgID != gone.OrgID {
		t.Fatalf("exit = %+v", exit)
	}
	if len(rem) != 1 || rem[0].Props["step"] != stSDate || *rem[0].OrgID != due.OrgID {
		t.Fatalf("reminder = %+v", rem)
	}
	raw, _ := json.Marshal(sink.all())
	if strings.Contains(string(raw), "Тайное") {
		t.Fatalf("event name leaked: %s", raw)
	}
}
