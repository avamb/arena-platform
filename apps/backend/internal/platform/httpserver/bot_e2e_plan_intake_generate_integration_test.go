//go:build integration

package httpserver

// End-to-end proof of the bot's side of wave 2 of the hall plan intake (spec 64):
// the organizer's file is built by the generator job, not by an administrator.
//
//   - the generator answers PASS: the draft is in review, the bot tells the
//     organizer (draft_ready), "Check the plan" opens the draft of the author
//     "generator", the organizer approves and the plan exists;
//   - the generator has no adapter: the bot tells the organizer, in the words of
//     the escalation, that an administrator builds the plan now.

import (
	"strings"
	"testing"
)

// The generator builds the draft, the organizer checks and approves it.
func TestBotE2E_PlanIntake_GeneratorDraftIsCheckedAndApproved(t *testing.T) {
	e := newPIEnv(t, planIntakeE2EOwnerTG+8)
	o := e.owner

	o.press("pi:up:"+e.venue.String(), "Пришлите файлы схемы")
	e.doc("pi-gen-xlsx", "hall.xlsx", vpgXLSX(t), "Файл 1 получен")
	text := o.press("pi:done", "Файл получен. Мы строим схему зала")
	if !strings.Contains(text, "несколько минут") || strings.Contains(text, "администратор") {
		t.Fatalf("the received text:\n%s", text)
	}
	id, state := e.intake()
	if state != "received" {
		t.Fatalf("intake state = %s, want received", state)
	}

	// An open request that waits for the generator says "we are building".
	open := o.press("pi:up:"+e.venue.String(), "уже есть заявка")
	if !strings.Contains(open, "Мы строим схему") || strings.Contains(open, "Администратор строит") {
		t.Fatalf("the open request of a received intake:\n%s", open)
	}

	svg := vpgAuthoringSVG(t)
	s := &vpgStub{job: func(int) (int, string) { return 200, vpgDone("ok", sp("PASS"), &svg, vpgUsedFile) }}
	mark := e.tg.mark()
	if got := runVenuePlanGenerateJobs(t, e.pool, e.srv.media, newVPGStub(t, s), id); len(got) != 1 {
		t.Fatalf("generate jobs run: %v", got)
	}
	if _, state := e.intake(); state != "review" {
		t.Fatalf("after the generator job: state %s, want review", state)
	}
	notice := e.tg.waitSince(t, mark, "готова. Сравните её с вашим планом")
	if !strings.Contains(notice, e.venueName) {
		t.Fatalf("the notice:\n%s", notice)
	}
	if !e.keyboardsHave("pi:rv:" + id) {
		t.Fatal("the notice has no \"Check the plan\" button")
	}

	// "Check the plan" opens the draft the generator wrote.
	o.press("pi:rv:"+id, "[photo plan.png]")
	if n := e.count(`SELECT count(*) FROM venue_plan_drafts WHERE intake_id = $1 AND n = 1 AND author = 'generator'`, id); n != 1 {
		t.Fatalf("generator drafts: %d, want 1", n)
	}
	if !e.keyboardsHave("pi:ok:"+id+":1") || !e.keyboardsHave("pi:fix:"+id+":1") {
		t.Fatal("the review lacks its buttons")
	}
	o.press("pi:ok:"+id+":1", "Схема зала сохранена")
	if _, state := e.intake(); state != "approved" {
		t.Fatalf("intake state = %s, want approved", state)
	}
	if n := e.count(`SELECT count(*) FROM venue_plan_intakes i JOIN seating_plans p ON p.id = i.plan_id WHERE i.id = $1`, id); n != 1 {
		t.Fatal("no plan was created")
	}
}

// The generator has no adapter for the file: the organizer is told an
// administrator builds the plan, and the intake waits for one.
func TestBotE2E_PlanIntake_NoAdapterTellsTheOrganizerAnAdministratorBuilds(t *testing.T) {
	e := newPIEnv(t, planIntakeE2EOwnerTG+9)
	o := e.owner

	o.press("pi:up:"+e.venue.String(), "Пришлите файлы схемы")
	e.photo("pi-gen-jpg", vpiJPEG(t), "Файл 1 получен")
	o.press("pi:done", "Файл получен. Мы строим схему зала")
	id, state := e.intake()
	if state != "received" {
		t.Fatalf("intake state = %s, want received", state)
	}

	s := &vpgStub{job: func(int) (int, string) {
		return 200, vpgDone("no_adapter", nil, nil,
			`[{"index":0,"declared_kind":"image","detected_kind":"image","outcome":"no_adapter","reason":"raster_profile_required"}]`)
	}}
	mark := e.tg.mark()
	runVenuePlanGenerateJobs(t, e.pool, e.srv.media, newVPGStub(t, s), id)
	if _, state := e.intake(); state != "operator" {
		t.Fatalf("after the generator job: state %s, want operator", state)
	}
	notice := e.tg.waitSince(t, mark, "автоматически")
	for _, want := range []string{e.venueName, "Её построит администратор платформы", "до одних рабочих суток"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("the escalated notice lacks %q:\n%s", want, notice)
		}
	}

	// The request is with an administrator now: the open screen says so.
	open := o.press("pi:up:"+e.venue.String(), "уже есть заявка")
	if !strings.Contains(open, "Администратор строит схему") {
		t.Fatalf("the open request of an operator intake:\n%s", open)
	}
}
