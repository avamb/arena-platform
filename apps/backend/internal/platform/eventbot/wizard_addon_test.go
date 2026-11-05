package eventbot

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Parallel categories can be marked as add-ons ("extra child" next to
// "child + adult", migration 0197): the mark shows in the list, a date made
// only of add-ons is refused, a new main category sends no flag, and an
// existing category sends an explicit one so a cleared mark reaches the
// server.
func TestWizard_AddonCategories(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	r.firstDate()
	r.press("next", stTMode)
	r.press("mode:multi", stTKind)
	r.press("kind:parallel", stTCatName)
	r.text("Ребёнок + взрослый", stTCatPrice)
	r.text("50", stTCatPlaces)
	r.text("11", stTCatMore)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(screen.Buttons), "addon") {
		t.Errorf("one category offers the add-on screen: %v", screen.Buttons)
	}
	r.press("addon", stTCatMore) // nothing to mark yet
	r.press("more", stTCatName)
	r.text("Дополнительный детский", stTCatPrice)
	r.text("35", stTCatPlaces)
	r.text("3", stTCatMore)

	r.press("addon", stTCatAddon)
	r.press("ad:1", stTCatAddon)
	screen, err = r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(screen.Buttons), "☑ Дополнительный детский") || !strings.Contains(fmt.Sprint(screen.Buttons), "☐ Ребёнок + взрослый") {
		t.Errorf("toggles = %v", screen.Buttons)
	}
	r.press("ad:0", stTCatAddon)
	r.press("done", stTCatAddon) // every category an add-on: refused
	r.press("ad:0", stTCatAddon)
	r.press("done", stTCatMore)
	screen, err = r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "Дополнительный детский 35") || !strings.Contains(screen.Text, "(доп.)") {
		t.Errorf("the list does not mark the add-on:\n%s", screen.Text)
	}
	r.press("done", stXDescription)

	req := BuildBundle(r.d, 0, 777, "", "ref1")
	if len(req.CategoryList) != 2 {
		t.Fatalf("categories = %+v", req.CategoryList)
	}
	if req.CategoryList[0].IsAddon != nil {
		t.Errorf("a new main category sends a flag: %v", *req.CategoryList[0].IsAddon)
	}
	if f := req.CategoryList[1].IsAddon; f == nil || !*f {
		t.Errorf("the add-on is not sent as one: %v", f)
	}

	// An existing category (read back on "Edit") whose mark was cleared.
	r.d.Sessions[0].SessionID = "sess-1"
	tk := r.d.ticketsOf(0)
	tk.Categories[1].TierID = "tier-extra"
	tk.Categories[1].Addon = false
	req = BuildBundle(r.d, 0, 777, "", "ref1")
	if f := req.CategoryList[1].IsAddon; f == nil || *f {
		t.Errorf("a cleared mark of an existing category must be an explicit false: %v", f)
	}
}
