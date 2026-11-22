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

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// fakeSeriesRefs is a RefIO that also lists tours.
type fakeSeriesRefs struct {
	*fakeRefs
	series []SeriesItem
	err    error
}

func (f *fakeSeriesRefs) ActiveSeries(context.Context, string, uuid.UUID) ([]SeriesItem, error) {
	return f.series, f.err
}

// fakeLinkSave is a fakeSave that records the tour bind.
type fakeLinkSave struct {
	*fakeSave
	linked  [][2]string
	linkErr error
}

func (f *fakeLinkSave) LinkEventSeries(_ context.Context, _ string, _ uuid.UUID, eventID, seriesID string) error {
	f.linked = append(f.linked, [2]string{eventID, seriesID})
	return f.linkErr
}

func tourRun(t *testing.T, tours ...SeriesItem) *wizardRun {
	t.Helper()
	return newWizardRun(t, &fakeSeriesRefs{fakeRefs: newFakeRefs(), series: tours})
}

func TestWizard_SeriesStep_AskedOnlyWhenTheOrgHasTours(t *testing.T) {
	r := tourRun(t)
	r.text("Концерт", stEvAge)
	r.press("age:16+", stEvPromoter)
	r.press("prom:lmp", stEvPoster) // no tours: no step

	r = tourRun(t, SeriesItem{ID: "t1", Name: "Лето 2027"})
	r.text("Концерт", stEvAge)
	r.press("age:16+", stEvPromoter)
	r.press("prom:lmp", stEvSeries)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	var datas []string
	for _, row := range screen.Buttons {
		for _, b := range row {
			datas = append(datas, b.Data)
		}
	}
	joined := strings.Join(datas, ",")
	if !strings.Contains(joined, "ser:t1") || !strings.Contains(joined, "skip") {
		t.Errorf("tour question buttons = %v, want the tour and Skip", datas)
	}
	if !strings.Contains(screen.Text, "тур") {
		t.Errorf("tour question text = %q", screen.Text)
	}
}

func TestWizard_SeriesStep_ChooseAndSkip(t *testing.T) {
	r := tourRun(t, SeriesItem{ID: "t1", Name: "Лето 2027"})
	r.text("Концерт", stEvAge)
	r.press("age:16+", stEvPromoter)
	r.press("prom:lmp", stEvSeries)
	r.press("ser:t1", stEvPoster)
	if r.d.Event.SeriesID != "t1" || r.d.Event.SeriesName != "Лето 2027" {
		t.Errorf("draft tour = %q/%q", r.d.Event.SeriesID, r.d.Event.SeriesName)
	}

	r = tourRun(t, SeriesItem{ID: "t1", Name: "Лето 2027"})
	r.text("Концерт", stEvAge)
	r.press("age:16+", stEvPromoter)
	r.press("prom:lmp", stEvSeries)
	r.press("skip", stEvPoster)
	if r.d.Event.SeriesID != "" {
		t.Errorf("skip kept tour %q", r.d.Event.SeriesID)
	}

	// An unknown tour id (archived meanwhile) stays on the question.
	r = tourRun(t, SeriesItem{ID: "t1", Name: "Лето 2027"})
	r.text("Концерт", stEvAge)
	r.press("age:16+", stEvPromoter)
	r.press("prom:lmp", stEvSeries)
	r.press("ser:gone", stEvSeries)
}

func TestWizard_SeriesStep_FailedListSkipsTheStep(t *testing.T) {
	r := newWizardRun(t, &fakeSeriesRefs{fakeRefs: newFakeRefs(), err: errors.New("boom")})
	r.text("Концерт", stEvAge)
	r.press("age:16+", stEvPromoter)
	r.press("prom:lmp", stEvPoster)
}

func saveOneDate(t *testing.T, r *wizardRun, io SaveIO) SaveOutcome {
	t.Helper()
	r.text("Концерт", stEvAge)
	r.press("age:16+", stEvPromoter)
	r.press("prom:lmp", stEvSeries)
	r.press("ser:t1", stEvPoster)
	r.press("skip", stSDate)
	r.firstDate()
	r.press("next", stTMode)
	r.press("mode:single", stTName)
	r.press("default", stTPrice)
	r.text("10", stTChanges)
	r.press("no", stXDescription)
	r.press("skip", stXCurrency)
	r.press("cur:CZK", stXPublish)
	r.press("pub:now", stSummary)
	return r.w.Save(context.Background(), io, r.ws, r.d, "d1")
}

func TestWizard_Save_BindsTheTourOnce(t *testing.T) {
	r := tourRun(t, SeriesItem{ID: "t1", Name: "Лето 2027"})
	io := &fakeLinkSave{fakeSave: &fakeSave{}}
	out := saveOneDate(t, r, io)
	if !out.Done {
		t.Fatalf("save not done: %+v", out)
	}
	if len(io.linked) != 1 || io.linked[0] != [2]string{"event-1", "t1"} {
		t.Fatalf("linked = %v, want one bind of event-1 to t1", io.linked)
	}
	// A second Save of the same draft does not bind again.
	r.w.Save(context.Background(), io, r.ws, r.d, "d1")
	if len(io.linked) != 1 {
		t.Errorf("linked again: %v", io.linked)
	}
}

func TestWizard_Save_RefusedTourBindIsOnlyAWarning(t *testing.T) {
	r := tourRun(t, SeriesItem{ID: "t1", Name: "Лето 2027"})
	io := &fakeLinkSave{fakeSave: &fakeSave{}, linkErr: &APIError{Status: 422, Code: "event.invalid_series"}}
	out := saveOneDate(t, r, io)
	if !out.Done {
		t.Fatalf("a refused tour must not fail the save: %+v", out)
	}
	found := false
	for _, w := range out.Warnings {
		found = found || strings.Contains(w, "тур")
	}
	if !found {
		t.Errorf("warnings = %v, want the tour notice", out.Warnings)
	}
}

func TestArenaClient_Series(t *testing.T) {
	orgID := uuid.New()
	eventID := uuid.New()
	var patched map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		base := "/v1/organizations/" + orgID.String()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base+"/series":
			_, _ = w.Write([]byte(`{"items":[{"id":"t1","name":"Лето","event_count":2},{"id":"t2","name":"Зима"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == base+"/events":
			_, _ = w.Write([]byte(`{"events":[{"id":"` + uuid.NewString() + `"},{"id":"` + eventID.String() + `","series_id":"t1","series_name":"Лето"}]}`))
		case r.Method == http.MethodPatch && r.URL.Path == base+"/events/"+eventID.String():
			b, _ := io.ReadAll(r.Body)
			patched = nil
			_ = json.Unmarshal(b, &patched)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c := NewArenaClient(srv.URL, "", srv.Client())
	ctx := context.Background()

	items, err := c.ActiveSeries(ctx, "jwt", orgID)
	if err != nil || len(items) != 2 || items[0].Name != "Лето" || items[0].EventCount != 2 {
		t.Fatalf("ActiveSeries = %+v, %v", items, err)
	}
	id, name, err := c.EventSeries(ctx, "jwt", orgID, eventID)
	if err != nil || id != "t1" || name != "Лето" {
		t.Fatalf("EventSeries = %q %q %v", id, name, err)
	}
	if err := c.LinkEventSeries(ctx, "jwt", orgID, eventID.String(), "t2"); err != nil || patched["series_id"] != "t2" {
		t.Fatalf("bind: body %v err %v", patched, err)
	}
	if err := c.LinkEventSeries(ctx, "jwt", orgID, eventID.String(), ""); err != nil {
		t.Fatal(err)
	}
	if v, ok := patched["series_id"]; !ok || v != nil {
		t.Errorf("unbind must send series_id:null, body %v", patched)
	}
}

func TestSeriesScreen(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{texts: NewTexts(bundle)}
	ev := uuid.New()
	items := []SeriesItem{{ID: "t1", Name: "Лето"}, {ID: "t2", Name: "Зима"}}

	text, rows := b.seriesScreen("ru", items, ev, "t2", "Зима")
	if !strings.Contains(text, "Зима") {
		t.Errorf("text = %q", text)
	}
	var data []string
	var labels []string
	for _, row := range rows {
		for _, btn := range row {
			data = append(data, btn.CallbackData)
			labels = append(labels, btn.Text)
		}
	}
	want := []string{"sr:s:" + ev.String() + ":0", "sr:s:" + ev.String() + ":1", "sr:u:" + ev.String(), "ec:o:" + ev.String()}
	if strings.Join(data, "|") != strings.Join(want, "|") {
		t.Errorf("callbacks = %v, want %v", data, want)
	}
	if !strings.HasPrefix(labels[1], "✔ ") {
		t.Errorf("the bound tour is not marked: %v", labels)
	}
	for _, d := range data {
		if len(d) > 64 {
			t.Errorf("callback %q is over 64 bytes", d)
		}
	}

	_, rows = b.seriesScreen("ru", nil, ev, "", "")
	if len(rows) != 1 {
		t.Errorf("no tours and no bind: want only Back, got %v", rows)
	}
}
