package generate

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/generator"
)

// An SVG over the limit is an invalid draft, whatever the validation status;
// an oversized preview alone changes nothing.
func TestDecide_OversizedSVGIsAnInvalidDraft(t *testing.T) {
	want := Decision{State: venueplan.StateOperator, Reason: venueplan.EscalationDraftInvalid}
	for _, status := range []string{generator.StatusPass, generator.StatusQuarantine, ""} {
		res := generator.Result{Result: generator.ResultOK, ValidationStatus: status, SVGTooLarge: generator.MaxSVGBytes + 1}
		if got := Decide(res, nil, false, 1); got != want {
			t.Errorf("status %q: got %+v, want %+v", status, got, want)
		}
	}
	res := generator.Result{Result: generator.ResultOK, ValidationStatus: generator.StatusPass,
		SVG: []byte("<svg/>"), PreviewTooLarge: generator.MaxPreviewBytes + 1}
	if got := Decide(res, nil, true, 1); got != (Decision{State: venueplan.StateReview, KeepDraft: true}) {
		t.Errorf("oversized preview lost the draft: %+v", got)
	}
}
