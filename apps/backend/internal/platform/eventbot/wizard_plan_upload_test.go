package eventbot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// uploadRefs is a reference source that can also store a hall plan.
type uploadRefs struct{ *fakeRefs }

func (uploadRefs) CanUploadPlans() bool { return true }

// failingPlanRefs cannot read the venue's plans at all.
type failingPlanRefs struct{ uploadRefs }

func (failingPlanRefs) VenuePlans(context.Context, string, uuid.UUID, string) ([]PlanRef, error) {
	return nil, errors.New("down")
}

func uploadedPlan() PlanRef {
	return PlanRef{
		VersionID: "22222222-2222-2222-2222-222222222222", Name: "Hall", Seats: 12,
		Categories: []PlanCategory{{Index: 1, Name: "Parterre", Places: 8}, {Index: 2, Name: "Balcony", Places: 4}},
	}
}

func uploadRun(t *testing.T, refs *fakeRefs) *wizardRun {
	t.Helper()
	return newWizardRun(t, uploadRefs{refs})
}

// A venue with no plan asks whether to send one; "continue without a plan"
// goes on to the places question as the venue always did.
func TestWizardPlanUpload_VenueWithoutPlanOffersTheUpload(t *testing.T) {
	r := uploadRun(t, newFakeRefs())
	seatedToVenue(r)
	r.press("venue:akr", stSPlanAsk)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "Palác Akropolis") {
		t.Fatalf("text = %q", screen.Text)
	}
	got := map[string]bool{}
	for _, row := range screen.Buttons {
		for _, b := range row {
			got[b.Data] = true
		}
	}
	if !got["plan:upload"] || !got["plan:skip"] {
		t.Fatalf("buttons = %v", screen.Buttons)
	}
	r.press("plan:skip", stSCapacity)
	r.press("keep", stSMore)
	if r.d.Sessions[0].seated() {
		t.Fatalf("session = %+v", r.d.Sessions[0])
	}
}

// The sent plan arrives as WizInput.Plan; the date's plan question then lists
// it first, and the date is sold by it like by any other plan.
func TestWizardPlanUpload_SentPlanLeadsThePlanQuestion(t *testing.T) {
	refs := newFakeRefs()
	r := uploadRun(t, refs)
	seatedToVenue(r)
	r.press("venue:akr", stSPlanAsk)
	r.press("plan:upload", stSPlanFile)
	// a typed text is no plan
	if note := r.text("hello", stSPlanFile); !strings.Contains(note, "SVG") {
		t.Fatalf("note = %q", note)
	}
	// the bot stored the plan, so the venue lists it from now on
	refs.plans = map[string][]PlanRef{"akr": {akropolisPlan(), uploadedPlan()}}
	note := r.step(WizInput{Plan: &PlanUploaded{Plan: uploadedPlan(), Report: "REPORT"}}, stSPlan)
	if note != "REPORT" {
		t.Fatalf("note = %q", note)
	}
	if got := r.d.Scratch.Plans; len(got) != 2 || got[0].VersionID != uploadedPlan().VersionID || got[1].VersionID != akropolisPlan().VersionID {
		t.Fatalf("plans = %+v", got)
	}
	r.press("plan:0", stSPlanPrice)
	r.text("20", stSPlanPrice)
	r.text("10", stSMore)
	s := r.d.Sessions[0]
	if s.PlanVersionID != uploadedPlan().VersionID || s.Capacity != 12 || len(s.Tickets.Categories) != 2 {
		t.Fatalf("session = %+v", s)
	}
}

// Back from the plan question goes to the question before the upload, not to a
// screen that waits for a file that was already sent.
func TestWizardPlanUpload_BackSkipsTheFileScreen(t *testing.T) {
	r := uploadRun(t, newFakeRefs())
	seatedToVenue(r)
	r.press("venue:akr", stSPlanAsk)
	r.press("plan:upload", stSPlanFile)
	r.step(WizInput{Plan: &PlanUploaded{Plan: uploadedPlan()}}, stSPlan)
	r.press("back", stSVenue)
}

// A plan question that already lists plans can take one more, and "without a
// plan" still works from it.
func TestWizardPlanUpload_AnotherPlanFromTheList(t *testing.T) {
	refs := newFakeRefs()
	refs.plans = map[string][]PlanRef{"akr": {akropolisPlan()}}
	r := uploadRun(t, refs)
	seatedToVenue(r)
	r.press("venue:akr", stSPlan)
	screen, _ := r.w.Render(context.Background(), r.ws, r.d)
	found := false
	for _, row := range screen.Buttons {
		for _, b := range row {
			found = found || b.Data == "plan:upload"
		}
	}
	if !found {
		t.Fatalf("no upload button: %v", screen.Buttons)
	}
	r.press("plan:upload", stSPlanFile)
	r.press("plan:skip", stSCapacity)
}

// A venue made in the wizard has no plan either: the places were asked already,
// so "without a plan" goes straight to "one more date?".
func TestWizardPlanUpload_NewVenueOffersTheUpload(t *testing.T) {
	refs := newFakeRefs()
	r := uploadRun(t, refs)
	seatedToVenue(r)
	r.press("venue:new", stVName)
	r.text("Teatro", stVAddress)
	r.text("Calle Mayor 1", stVCapacity)
	r.text("50", stSPlanAsk) // Czechia has one zone: the venue is made at once
	r.press("plan:skip", stSMore)
	if s := r.d.Sessions[0]; s.VenueID != "venue-teatro" || s.Capacity != 50 || s.seated() {
		t.Fatalf("session = %+v", s)
	}
}

// A new venue that gets a plan sold by it.
func TestWizardPlanUpload_NewVenueWithAnUploadedPlan(t *testing.T) {
	refs := newFakeRefs()
	r := uploadRun(t, refs)
	seatedToVenue(r)
	r.press("venue:new", stVName)
	r.text("Teatro", stVAddress)
	r.text("Calle Mayor 1", stVCapacity)
	r.text("50", stSPlanAsk) // Czechia has one zone: the venue is made at once
	r.press("plan:upload", stSPlanFile)
	refs.plans = map[string][]PlanRef{"venue-teatro": {uploadedPlan()}}
	r.step(WizInput{Plan: &PlanUploaded{Plan: uploadedPlan()}}, stSPlan)
	r.press("plan:none", stSMore) // the places were answered already
}

// A reference source that cannot store plans never offers the upload, and a
// failing plan lookup is not "the venue has no plan".
func TestWizardPlanUpload_NotOfferedWithoutTheAbilityOrTheAnswer(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	seatedToVenue(r)
	r.press("venue:akr", stSCapacity)

	r = newWizardRun(t, failingPlanRefs{uploadRefs{newFakeRefs()}})
	seatedToVenue(r)
	r.press("venue:akr", stSCapacity)
}
