package hseating

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating/layoutgen"
)

func ptr(v int64) *int64 { return &v }

func TestSessionLayoutState_FreezesSoldAndHeldAtTheirPrice(t *testing.T) {
	st := SessionLayoutState("CZK", []gen.SessionSeatLayoutStateRow{
		{SeatKey: "a", Status: "sold", OrderPrice: ptr(1234), TierPrice: ptr(1000)},
		{SeatKey: "b", Status: "sold", TierPrice: ptr(1000)}, // sold upstream: list price
		{SeatKey: "c", Status: "held", TierPrice: ptr(900)},  // held, no order yet
		{SeatKey: "d", Status: "held", OrderPrice: ptr(800)},
		{SeatKey: "e", Status: "unavailable", TierPrice: ptr(700)},
		{SeatKey: "f", Status: "available", TierPrice: ptr(700)},
		{SeatKey: "g", Status: "sold"}, // no tier, no order
		{SeatKey: "h", Status: "held", TierPrice: ptr(700), HoldExpired: true},
	})
	if _, frozen := st.SoldHeld["h"]; frozen || st.Closed["h"] {
		t.Fatal("a held seat on an expired reservation must be free")
	}
	want := map[string]layoutgen.FrozenSeat{
		"a": {Status: "sold", PriceMinor: 1234},
		"b": {Status: "sold", PriceMinor: 1000},
		"c": {Status: "held", PriceMinor: 900},
		"d": {Status: "held", PriceMinor: 800},
		"g": {Status: "sold", PriceMinor: 0},
	}
	if st.Currency != "CZK" || len(st.SoldHeld) != len(want) {
		t.Fatalf("state = %+v", st)
	}
	for k, v := range want {
		if st.SoldHeld[k] != v {
			t.Fatalf("%s = %+v, want %+v", k, st.SoldHeld[k], v)
		}
	}
	if !st.Closed["e"] || st.Closed["f"] || len(st.Closed) != 1 {
		t.Fatalf("closed = %v", st.Closed)
	}
}

func TestWriteLayoutError_Statuses(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{&layoutgen.ParamError{Param: "categories", Reason: "x"}, http.StatusBadRequest, "seat_layout.invalid_param"},
		{&layoutgen.AnnotationError{Annotation: "section_weights", Reason: "x"}, http.StatusBadRequest, "seat_layout.invalid_annotation"},
		{&layoutgen.AnnotationRequiredError{Annotation: "focal_point"}, http.StatusUnprocessableEntity, "seat_layout.annotation_required"},
		{layoutgen.ErrNoSeats, http.StatusUnprocessableEntity, "seat_layout.no_seats"},
		{&layoutgen.ContractError{Algorithm: "x", Reason: "y"}, http.StatusInternalServerError, "seat_layout.algorithm_failed"},
		{errors.New("boom"), http.StatusInternalServerError, "seat_layout.suggest_failed"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		writeLayoutError(w, httptest.NewRequest(http.MethodPost, "/", nil), logger, c.err)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != c.status || body.Error.Code != c.code {
			t.Fatalf("%v: %d %s, want %d %s", c.err, w.Code, body.Error.Code, c.status, c.code)
		}
	}
}

func TestLayoutAlgorithmItems_ListsTheRegistry(t *testing.T) {
	items := LayoutAlgorithmItems()
	found := false
	for _, it := range items {
		if it.ID == layoutgen.DesirabilityID {
			found = true
			if len(it.Params) == 0 || it.RequiredAnnotations[0] != layoutgen.AnnotationFocalPoint {
				t.Fatalf("desirability item = %+v", it)
			}
		}
	}
	if !found {
		t.Fatal("desirability is not listed")
	}
}
