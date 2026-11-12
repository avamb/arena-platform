//go:build integration

package httpserver

// End-to-end proof of the review fixes of the bot's hall plan intake:
//
//   - a draft that still holds the id of an intake reads the intake's real state:
//     an open one keeps the waiting screen and a file sent meanwhile is answered
//     in one line, a final one is forgotten and the normal question is asked;
//   - "Done" answered 415 keeps the collected files, names the refused file and
//     lets the person remove it, and a second "Done" says the files are out;
//   - "Continue without a plan" with an open request asks once (yes cancels the
//     request, no keeps it);
//   - a cancel of an intake that was approved meanwhile does not say "cancelled".

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// (2) Back, then "Send a file" again: the stale id is read, not trusted.
func TestBotE2E_PlanIntake_BackThenSendAFileAgain(t *testing.T) {
	e := newPIEnv(t, planIntakeE2EOwnerTG+3)
	e.wizardToVenue("Plan intake back " + e.suffix)
	o := e.owner

	o.press("wz:plan:upload", "Пришлите файлы схемы")
	e.doc("pi-back-pdf", "hall.pdf", vpiPDF(), "Файл 1 получен")
	o.press("pi:done", "Файл получен. Схему зала построит администратор")
	id, state := e.intake()
	if state != "operator" {
		t.Fatalf("intake state = %s, want operator", state)
	}

	// Back and "Send a file" again: the request is still open, so the person
	// sees the waiting screen, and a file sent now is answered in one line.
	o.press("wz:back", "нет схемы зала")
	text := o.press("wz:plan:upload", "Ваши файлы у администратора")
	if strings.Contains(text, "Пришлите файлы схемы") {
		t.Fatalf("a waiting draft asks for files again:\n%s", text)
	}
	e.doc("pi-back-pdf2", "other.pdf", vpiPDF(), "Ваша заявка уже у нас")
	if n := e.count(`SELECT count(*) FROM venue_plan_intakes WHERE org_id = $1`, e.f.orgID); n != 1 {
		t.Fatalf("%d intakes, want 1", n)
	}

	// The request ends meanwhile: the id is forgotten and the question is normal.
	if _, err := e.pool.Exec(context.Background(), `UPDATE venue_plan_intakes SET state = 'cancelled', closed_at = now() WHERE id = $1`, id); err != nil {
		t.Fatalf("cancel the intake: %v", err)
	}
	o.press("wz:back", "нет схемы зала")
	o.press("wz:plan:upload", "Пришлите файлы схемы")
	if step, _, intake := e.draftState(); step != "s_plan_file" || intake != "" {
		t.Fatalf("draft = %s intake=%q, want s_plan_file with no intake", step, intake)
	}
	e.doc("pi-back-pdf3", "again.pdf", vpiPDF(), "Файл 1 получен")
}

// (3) Three files, one refused: the files are kept, the refused one is named and
// can be removed, and then the post goes through.
func TestBotE2E_PlanIntake_RefusedFileKeepsTheFiles(t *testing.T) {
	e := newPIEnv(t, planIntakeE2EOwnerTG+4)
	e.wizardToVenue("Plan intake refused " + e.suffix)
	o := e.owner

	o.press("wz:plan:upload", "Пришлите файлы схемы")
	e.doc("pi-r1", "hall.pdf", vpiPDF(), "Файл 1 получен")
	e.doc("pi-r2", "notes.bin", []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x00, 0x7f, 0x80}, "Файл 2 получен")
	e.doc("pi-r3", "balcony.pdf", vpiPDF(), "Файл 3 получен")
	text := o.press("pi:done", "Не получается прочитать файл «notes.bin»")
	if !strings.Contains(text, "сохранил") {
		t.Fatalf("the refusal does not say the files are kept:\n%s", text)
	}
	if n := e.count(`SELECT count(*) FROM venue_plan_intakes WHERE org_id = $1`, e.f.orgID); n != 0 {
		t.Fatalf("%d intakes were created for a refused post", n)
	}
	if !e.keyboardsHave("pi:rm:2") || !e.keyboardsHave("pi:done") {
		t.Fatal("the refusal offers no way to remove the refused file or to press Done again")
	}

	// The person removes the refused file; the other two stay and go out.
	o.press("pi:rm:2", "Файлов сейчас: 2")
	o.press("pi:done", "Файл получен. Схему зала построит администратор")
	id, _ := e.intake()
	if n := e.count(`SELECT count(*) FROM venue_plan_intake_sources WHERE intake_id = $1`, id); n != 2 {
		t.Fatalf("sources = %d, want 2", n)
	}
	// A second "Done" is told the files are out, and sends nothing.
	o.press("pi:done", "уже отправил")
	if n := e.count(`SELECT count(*) FROM venue_plan_intakes WHERE org_id = $1`, e.f.orgID); n != 1 {
		t.Fatalf("%d intakes after a second Done, want 1", n)
	}
}

func (e *piEnv) wizardWaitingOnAnIntake(name string) string {
	e.t.Helper()
	e.wizardToVenue(name)
	o := e.owner
	o.press("wz:plan:upload", "Пришлите файлы схемы")
	e.doc("pi-w-pdf-"+e.suffix, "hall.pdf", vpiPDF(), "Файл 1 получен")
	o.press("pi:done", "Файл получен. Схему зала построит администратор")
	id, state := e.intake()
	if state != "operator" {
		e.t.Fatalf("intake state = %s, want operator", state)
	}
	return id
}

// (5) "Continue without a plan" while the request is open asks once. Yes cancels
// the request and goes on.
func TestBotE2E_PlanIntake_SkipWithCancel(t *testing.T) {
	e := newPIEnv(t, planIntakeE2EOwnerTG+5)
	id := e.wizardWaitingOnAnIntake("Plan intake skip yes " + e.suffix)
	o := e.owner

	o.press("wz:plan:skip", "Отменить заявку на схему зала?")
	if !e.keyboardsHave("pi:sky:"+id) || !e.keyboardsHave("pi:skn:"+id) {
		t.Fatal("the question has no Yes and No buttons")
	}
	o.press("pi:sky:"+id, "нет загруженной схемы")
	if _, state := e.intake(); state != "cancelled" {
		t.Fatalf("state = %s, want cancelled", state)
	}
	if step, paused, intake := e.draftState(); step != "s_capacity" || paused || intake != "" {
		t.Fatalf("draft = %s paused=%v intake=%q, want s_capacity", step, paused, intake)
	}
}

// (5) No: the draft goes on without a plan and the request stays.
func TestBotE2E_PlanIntake_SkipKeepingTheRequest(t *testing.T) {
	e := newPIEnv(t, planIntakeE2EOwnerTG+6)
	id := e.wizardWaitingOnAnIntake("Plan intake skip no " + e.suffix)
	o := e.owner

	o.press("wz:plan:skip", "Отменить заявку на схему зала?")
	o.press("pi:skn:"+id, "нет загруженной схемы")
	if _, state := e.intake(); state != "operator" {
		t.Fatalf("state = %s, want the request kept (operator)", state)
	}
	if step, paused, intake := e.draftState(); step != "s_capacity" || paused || intake != "" {
		t.Fatalf("draft = %s paused=%v intake=%q, want s_capacity", step, paused, intake)
	}
}

// (5) A cancel of a request that was approved meanwhile does not say "cancelled":
// the real state is shown (the saved plan).
func TestBotE2E_PlanIntake_CancelOnAnApprovedIntake(t *testing.T) {
	e := newPIEnv(t, planIntakeE2EOwnerTG+7)
	o := e.owner

	o.press("pi:up:"+e.venue.String(), "Пришлите файлы схемы")
	e.doc("pi-ca-svg", "plan.svg", []byte(planUpSVG), "Файл 1 получен")
	o.press("pi:done", "[photo plan.png]")
	id, state := e.intake()
	if state != "review" {
		t.Fatalf("a valid SVG: state = %s, want review", state)
	}

	// An administrator approves it while the person still holds the old screen.
	adm := "/v1/admin/plan-intakes/" + id
	e.adminOK(http.MethodPost, adm+"/claim", "taking it", "")
	e.adminOK(http.MethodPost, adm+"/approve", "checked", `{"draft":1}`)
	if _, state := e.intake(); state != "approved" {
		t.Fatalf("state = %s, want approved", state)
	}

	mark := e.tg.mark()
	o.press("pi:cx:"+id, "Схема зала сохранена")
	if strings.Contains(strings.Join(e.sentSince(mark), "\n"), "Заявка отменена") {
		t.Fatal("the cancel of an approved request was reported as cancelled")
	}
}

// sentSince is every message the bot sent after the mark.
func (e *piEnv) sentSince(mark int) []string {
	e.tg.mu.Lock()
	defer e.tg.mu.Unlock()
	return append([]string(nil), e.tg.sent[mark:]...)
}
