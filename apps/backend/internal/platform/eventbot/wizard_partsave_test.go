package eventbot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Review of PR #151: a save that stopped half way must never let a change
// go silently, and a copied chain of stages must be one the import accepts.

// addDateAt adds a date at the same venue with the categories of the date
// before.
func addDateAt(r *wizardRun, date, at string) {
	r.t.Helper()
	r.press("e:date", stSDate)
	r.text(date, stSTime)
	r.text(at, stSSame)
	r.press("same", stSTickets)
	r.press("st:same", stSConfirm)
	r.press("sc:next", stSMore)
	r.press("next", stEditMenu)
}

// enterParallel answers the "at once" category questions with the category
// given and a second one, "Детский" for 30 with 5 places (at least two are
// needed).
func enterParallel(r *wizardRun, name, price, places string) {
	r.t.Helper()
	r.text(name, stTCatPrice)
	r.text(price, stTCatPlaces)
	r.text(places, stTCatMore)
	r.press("more", stTCatName)
	r.text("Детский", stTCatPrice)
	r.text("30", stTCatPlaces)
	r.text("5", stTCatMore)
	r.press("done", stEditMenu)
}

// Dates 3 and 4 are new; 3 is saved, the save fails on 4. Changing the
// categories of date 3 afterwards is a change the card lists and the save
// sends — the change of a saved date no longer brings back the undo buttons,
// and "undo date 4" can no longer wipe the list and hide date 3's change.
func TestPartSave_ChangedSavedNewDateIsNotHidden(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	addDateAt(r, "18.12.2026", "18:00")
	addDateAt(r, "19.12.2026", "18:00")
	ref4 := "tg:" + r.ws.OrgID.String() + ":d9:4"
	io := &fakeSave{failRef: map[string]error{ref4: errors.New("boom")}}
	if out := r.w.Save(context.Background(), io, r.ws, d, "d9"); out.Done || out.FailedIdx != 3 {
		t.Fatalf("first save = %+v", out)
	}
	if !d.partlySaved() || d.Sessions[2].SessionID == "" {
		t.Fatalf("date 3 should be on the site: %+v", d.Saved)
	}
	if got := changeKeys(d); got != "date:4" {
		t.Fatalf("changes after the part save = %q", got)
	}

	r.press("e:tickets", stTDate)
	r.press("td:3", stTCatName)
	enterParallel(r, "Новая категория", "40", "10")
	if d.savedDone(2) || !d.partlySaved() {
		t.Fatalf("date 3 changed: done=%v partly=%v", d.savedDone(2), d.partlySaved())
	}
	if got := changeKeys(d); got != "tickets:3 date:4" {
		t.Fatalf("changes = %q", got)
	}
	screen := render(t, r)
	if data := buttonsData(screen); strings.Contains(data, "e:undo:") || !strings.Contains(data, "publish") {
		t.Fatalf("card buttons after the part save = %s", data)
	}
	// A stale undo of date 4 does nothing: date 4 stays and so does date 3's
	// change.
	r.press("e:undo:date:4", stEditMenu)
	if len(d.Sessions) != 4 || !d.Sessions[2].Dirty || len(d.Changed) == 0 {
		t.Fatalf("after a stale undo: %d dates, dirty=%v changed=%v", len(d.Sessions), d.Sessions[2].Dirty, d.Changed)
	}
	if plan := d.sendPlan(); !plan[2] || !plan[3] {
		t.Fatalf("plan = %v", plan)
	}
	r.press("publish", stPreview)
	if text := render(t, r).Text; !strings.Contains(text, "18.12.2026 18:00, новые категории") || !strings.Contains(text, "Новая дата 19.12.2026 18:00") {
		t.Fatalf("preview:\n%s", text)
	}

	io.failRef = nil
	if out := r.w.Save(context.Background(), io, r.ws, d, "d9"); !out.Done {
		t.Fatalf("second save = %+v", out)
	}
	last := io.posts[len(io.posts)-2:]
	if last[0].ActionEvent.ArenaSessionID != d.Sessions[2].SessionID || last[0].CategoryList[0].CategoryPriceName != "Новая категория" {
		t.Fatalf("date 3 was not sent with its new categories: %+v", last[0])
	}
}

// A draft stored before Save recorded its own dates in Loaded: a saved new
// date it changed afterwards is still listed, with no undo.
func TestPartSave_SavedNewDateMissingFromLoadedIsListed(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	addDateAt(r, "18.12.2026", "18:00")
	d.Sessions[2].SessionID = "s-3"
	d.Saved.Sessions = []SavedSession{{}, {}, {SessionID: "s-3"}}
	d.refreshChanged()
	if got := changeKeys(d); got != "tickets:3" {
		t.Fatalf("changes = %q", got)
	}
	if d.canUndo() || d.undo("tickets:3") {
		t.Fatal("undo offered for a date with nothing loaded")
	}
}

// "Publish changes" stays while the save has a date left to post.
func TestPartSave_PublishOfferedWhileADateIsUnsent(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	d.Sessions[1].Dirty = true
	delete(d.Loaded.Dates, x.s2.Id.String())
	d.Saved = DraftSaved{ActionID: 7, Sessions: []SavedSession{{SessionID: x.s1.Id.String(), Done: true}, {}}}
	if !d.unsent() || !d.partlySaved() {
		t.Fatalf("unsent=%v partly=%v", d.unsent(), d.partlySaved())
	}
	if data := buttonsData(render(t, r)); !strings.Contains(data, "publish") || strings.Contains(data, "e:undo:") {
		t.Fatalf("card buttons = %s", data)
	}
	r.press("publish", stPreview)
	d.Sessions[1].Dirty = false
	if d.unsent() {
		t.Fatal("a date on the site counted as unsent")
	}
}

// Categories of dates 1 and 2 changed; 1 is saved, 2 fails. Date 2 is put
// back as it was and the age changes: the age still reaches the site, on a
// date sent again, and the preview says so.
func TestPartSave_EventFieldAfterAPartSaveStillGoesOut(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	r.press("e:tickets", stTDate)
	r.press("td:1", stTCatName)
	enterParallel(r, "Взрослый", "60", "20")
	r.press("e:tickets", stTDate)
	r.press("td:2", stTCatName)
	enterParallel(r, "Взрослый", "60", "20")
	io := &fakeSave{failSession: map[string]error{x.s2.Id.String(): errors.New("boom")}}
	if out := r.w.Save(context.Background(), io, r.ws, d, "d9"); out.Done || out.FailedIdx != 1 {
		t.Fatalf("first save = %+v", out)
	}
	// Date 1 is what the site has now: it is no longer a change.
	if got := changeKeys(d); got != "tickets:2" {
		t.Fatalf("changes after the part save = %q", got)
	}

	// Date 2 back to what it was (as loaded, tier ids included): no longer
	// sent.
	back := cloneTickets(d.Loaded.Dates[x.s2.Id.String()].Tickets)
	d.Sessions[1].Tickets = &back
	d.refreshChanged()
	if d.Sessions[1].Dirty {
		t.Fatal("date 2 is still sent")
	}

	r.press("e:age", stEvAge)
	r.press("age:6+", stEditMenu)
	if d.savedDone(0) {
		t.Fatal("no date left to carry the new age")
	}
	if data := buttonsData(render(t, r)); !strings.Contains(data, "publish") {
		t.Fatalf("card buttons = %s", data)
	}
	r.press("publish", stPreview)
	text := render(t, r).Text
	if !strings.Contains(text, "Сеанс 15.12.2026 20:00 отправится заново") || strings.Contains(text, "Уже на сайте: 15.12.2026") {
		t.Fatalf("preview does not send date 1 again:\n%s", text)
	}

	io.failSession = nil
	before := len(io.posts)
	if out := r.w.Save(context.Background(), io, r.ws, d, "d9"); !out.Done {
		t.Fatalf("second save = %+v", out)
	}
	if len(io.posts) != before+1 || io.posts[before].Action.Age != "6+" {
		t.Fatalf("the age did not go out: %d posts, %+v", len(io.posts)-before, io.posts[len(io.posts)-1].Action)
	}
}

// The Save itself also finds a date to carry a field changed after the part
// save, even when nothing called refreshChanged (a draft stored earlier).
func TestPartSave_SaveFindsADateForTheEventFields(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	d.Saved = DraftSaved{ActionID: 7, EventID: "e", Sessions: []SavedSession{{SessionID: x.s1.Id.String(), Done: true}, {}}}
	d.Event.Age = "6+"
	io := &fakeSave{}
	if out := r.w.Save(context.Background(), io, r.ws, d, "d9"); !out.Done || len(io.posts) != 1 || io.posts[0].Action.Age != "6+" {
		t.Fatalf("save = %+v, posts = %d", out, len(io.posts))
	}
}

// After a part save the name that went out is what the site has: typing the
// old name back in is a change, and it is sent.
func TestPartSave_OldNameTypedBackIsSent(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	old := d.Event.Name
	r.press("e:name", stEvName)
	r.text("Новое имя", stEditMenu)
	r.press("e:tickets", stTDate)
	r.press("td:2", stTCatName)
	enterParallel(r, "Взрослый", "60", "20")
	// Plan: date 1 carries the name (no change of its own), date 2 its
	// categories. Date 1 goes out, date 2 fails.
	d.Sessions[0].Dirty = true
	io := &fakeSave{failSession: map[string]error{x.s2.Id.String(): errors.New("boom")}}
	if out := r.w.Save(context.Background(), io, r.ws, d, "d9"); out.Done {
		t.Fatalf("first save = %+v", out)
	}
	if d.Loaded.Event.Name != "Новое имя" || strings.Contains(changeKeys(d), "name") {
		t.Fatalf("loaded name = %q, changes = %q", d.Loaded.Event.Name, changeKeys(d))
	}
	r.press("e:name", stEvName)
	r.text(old, stEditMenu)
	if !strings.Contains(changeKeys(d), "name") {
		t.Fatalf("the old name typed back is no change: %q", changeKeys(d))
	}
	io.failSession = nil
	before := len(io.posts)
	if out := r.w.Save(context.Background(), io, r.ws, d, "d9"); !out.Done {
		t.Fatalf("second save = %+v", out)
	}
	for _, p := range io.posts[before:] {
		if p.Action.ActionName != old {
			t.Fatalf("posted name = %q, want %q", p.Action.ActionName, old)
		}
	}
}

// fitStagesCase is a chain of stages copied onto a date.
type fitStagesCase struct {
	name   string
	cats   []DraftCategory
	mode   string
	want   []DraftCategory
	sendOK bool
}

// Stages sold one after another, copied onto an earlier date: the chain is
// re-linked so that every stage the import gets has a window that ends after
// it starts (422 import.invalid_sale_window otherwise), the last stage has no
// end, and a single stage left is one category.
func TestFitStagesToDate_ChainTheImportAccepts(t *testing.T) {
	const date = "2026-12-20"
	cases := []fitStagesCase{
		{
			name: "date-only stages: the first one reaches the date, the rest would open after it",
			cats: []DraftCategory{{Name: "A", SellUntil: "2026-12-25"}, {Name: "B", SellUntil: "2026-12-28"}, {Name: "C"}},
			mode: ModeSingle,
			want: []DraftCategory{{Name: "A"}},
		},
		{
			name: "a count limit keeps the next stage: it sells once the first is gone",
			cats: []DraftCategory{{Name: "A", SellUntil: "2026-12-25", SellLimit: 20}, {Name: "B", SellUntil: "2026-12-28"}, {Name: "C"}},
			mode: ModeSequence,
			want: []DraftCategory{{Name: "A", SellUntil: date, SellLimit: 20}, {Name: "B"}},
		},
		{
			name: "four stages, two with limits",
			cats: []DraftCategory{{Name: "A", SellUntil: "2026-12-22", SellLimit: 10}, {Name: "B", SellUntil: "2026-12-24", SellLimit: 10},
				{Name: "C", SellUntil: "2026-12-26"}, {Name: "D"}},
			mode: ModeSequence,
			want: []DraftCategory{{Name: "A", SellUntil: date, SellLimit: 10}, {Name: "B", SellUntil: date, SellLimit: 10}, {Name: "C"}},
		},
		{
			name: "a stage ending with the date-only one before it never sells; its limit goes on",
			cats: []DraftCategory{{Name: "A", SellUntil: "2026-12-10"}, {Name: "B", SellUntil: "2026-12-10", SellLimit: 5},
				{Name: "C", SellUntil: "2026-12-15", SellLimit: 7}, {Name: "D"}},
			mode: ModeSequence,
			want: []DraftCategory{{Name: "A", SellUntil: "2026-12-10"}, {Name: "C", SellUntil: "2026-12-15", SellLimit: 12}, {Name: "D"}},
		},
		{
			name: "an early stage stays, the one reaching the date becomes the last",
			cats: []DraftCategory{{Name: "A", SellUntil: "2026-12-15"}, {Name: "B", SellUntil: "2026-12-25"}, {Name: "C"}},
			mode: ModeSequence,
			want: []DraftCategory{{Name: "A", SellUntil: "2026-12-15"}, {Name: "B"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tk := DraftTickets{Mode: ModeSequence, Categories: append([]DraftCategory(nil), c.cats...)}
			if n := fitTicketsToDate(&tk, date); n == 0 {
				t.Fatal("nothing reported as changed")
			}
			if tk.Mode != c.mode || len(tk.Categories) != len(c.want) {
				t.Fatalf("got mode %s %+v, want %s %+v", tk.Mode, tk.Categories, c.mode, c.want)
			}
			for i := range c.want {
				if tk.Categories[i] != c.want[i] {
					t.Fatalf("stage %d = %+v, want %+v", i, tk.Categories[i], c.want[i])
				}
			}
			assertImportAccepts(t, tk, date)
		})
	}
}

// assertImportAccepts builds the bundle of a date selling tk and runs every
// category's window through the import's own check.
func assertImportAccepts(t *testing.T, tk DraftTickets, date string) {
	t.Helper()
	d := &Draft{Mode: ModeEdit, Currency: "CZK", Tickets: tk, Sessions: []DraftSession{{
		Date: date, Time: "18:00", Timezone: "Europe/Prague", Capacity: 100, VenueID: "v", VenueName: "Hall", Tickets: &tk,
	}}}
	req := BuildBundle(d, 0, 0, "", "ref")
	if len(req.CategoryList) != len(tk.Categories) {
		t.Fatalf("bundle has %d categories, want %d", len(req.CategoryList), len(tk.Categories))
	}
	salesStart := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range req.CategoryList {
		if _, _, err := c.ParseSellWindow(&salesStart, nil); err != nil {
			t.Fatalf("category %d (%s): %v", i, c.CategoryPriceName, err)
		}
		if i == len(req.CategoryList)-1 && (c.SellEndTime != "" || *c.NextCategoryIndex != -1) {
			t.Fatalf("the last stage has an end or a next: %+v", c)
		}
	}
}

// "Same as the date before" on a chain of three stages ending after the new
// date gives a date the import accepts, and says the stages changed.
func TestSameTickets_ChainOfStagesFitsTheNewDate(t *testing.T) {
	x := loadTwoDates(t)
	r, d := x.r, x.d
	chain := DraftTickets{Mode: ModeSequence, Categories: []DraftCategory{
		{Name: "Ранний", PriceMinor: 100000, SellUntil: "2026-12-25", SellLimit: 20},
		{Name: "Обычный", PriceMinor: 150000, SellUntil: "2026-12-28"},
		{Name: "Поздний", PriceMinor: 200000},
	}}
	d.Sessions[1].Tickets = &chain
	r.press("e:date", stSDate)
	r.text("14.12.2026", stSTime)
	r.text("18:00", stSSame)
	r.press("same", stSTickets)
	note := r.press("st:same", stSConfirm)
	if !strings.Contains(note, "14.12.2026") || !strings.Contains(note, "этапы продаж") {
		t.Fatalf("note = %q", note)
	}
	got := *d.Sessions[2].Tickets
	if len(got.Categories) != 2 || got.Categories[0].SellUntil != "2026-12-14" || got.Categories[1].SellUntil != "" {
		t.Fatalf("copied chain = %+v", got.Categories)
	}
	if len(chain.Categories) != 3 || chain.Categories[0].SellUntil != "2026-12-25" {
		t.Fatalf("the source date changed: %+v", chain.Categories)
	}
	req := BuildBundle(d, 2, 0, "", "ref")
	for i, c := range req.CategoryList {
		if _, _, err := c.ParseSellWindow(nil, nil); err != nil {
			t.Fatalf("category %d: %v", i, err)
		}
	}
}
