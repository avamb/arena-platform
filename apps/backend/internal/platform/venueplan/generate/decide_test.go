package generate

import (
	"errors"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/generator"
)

// TestDecide_Table walks the whole outcome table of the wave-2 design.
func TestDecide_Table(t *testing.T) {
	svg := []byte("<svg/>")
	ok := func(status string, s []byte) generator.Result {
		return generator.Result{Result: generator.ResultOK, ValidationStatus: status, SVG: s}
	}
	retryErr := &generator.Error{Code: "timeout", Retry: true}
	finalErr := &generator.Error{Code: "memory_limit"}
	review := Decision{State: venueplan.StateReview, KeepDraft: true}
	op := func(r venueplan.EscalationReason, keep bool) Decision {
		return Decision{State: venueplan.StateOperator, Reason: r, KeepDraft: keep}
	}
	cases := []struct {
		name     string
		res      generator.Result
		err      error
		svgValid bool
		attempt  int
		want     Decision
	}{
		{"PASS and a valid SVG", ok(generator.StatusPass, svg), nil, true, 1, review},
		{"QUARANTINE and a valid SVG", ok(generator.StatusQuarantine, svg), nil, true, 1, op(venueplan.EscalationTooManyQuestions, true)},
		{"QUARANTINE and a bad SVG", ok(generator.StatusQuarantine, svg), nil, false, 1, op(venueplan.EscalationTooManyQuestions, false)},
		{"ok without an SVG", ok(generator.StatusPass, nil), nil, false, 1, op(venueplan.EscalationTooManyQuestions, false)},
		{"ok QUARANTINE without an SVG", ok(generator.StatusQuarantine, nil), nil, false, 2, op(venueplan.EscalationTooManyQuestions, false)},
		{"PASS and a bad SVG", ok(generator.StatusPass, svg), nil, false, 1, op(venueplan.EscalationDraftInvalid, false)},
		{"no_adapter with a failed file", generator.Result{Result: generator.ResultNoAdapter, Files: []generator.FileReport{
			{Outcome: "no_adapter"}, {Outcome: "failed"}}}, nil, false, 1, op(venueplan.EscalationGeneratorFailed, false)},
		{"no_adapter", generator.Result{Result: generator.ResultNoAdapter, Files: []generator.FileReport{
			{Outcome: "no_adapter"}}}, nil, false, 1, op(venueplan.EscalationUnsupportedFormat, false)},
		{"final error", generator.Result{}, finalErr, false, 1, op(venueplan.EscalationGeneratorFailed, false)},
		{"401", generator.Result{}, &generator.Error{Status: 401, Code: "auth.required"}, false, 1, op(venueplan.EscalationGeneratorFailed, false)},
		{"retry on attempt 1", generator.Result{}, retryErr, false, 1, Decision{Retry: true}},
		{"retry on attempt 2", generator.Result{}, retryErr, false, 2, Decision{Retry: true}},
		{"retry exhausted on attempt 3", generator.Result{}, retryErr, false, 3, op(venueplan.EscalationGeneratorFailed, false)},
		{"a storage error is retried", generator.Result{}, errors.New("storage down"), false, 1, Decision{Retry: true}},
		{"unknown result", generator.Result{Result: "maybe"}, nil, false, 1, op(venueplan.EscalationGeneratorFailed, false)},
	}
	for _, tc := range cases {
		if got := Decide(tc.res, tc.err, tc.svgValid, tc.attempt); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
	if len(RetryDelays) != MaxAttempts-1 || RetryDelays[0].Minutes() != 1 || RetryDelays[1].Minutes() != 5 {
		t.Errorf("retry delays %v", RetryDelays)
	}
}

func TestSendKind(t *testing.T) {
	cases := []struct {
		kind     venueplan.SourceKind
		png      bool
		want     generator.Kind
		usePNG   bool
		sendable bool
	}{
		{venueplan.SourcePDF, false, generator.KindPDF, false, true},
		{venueplan.SourceXLSX, false, generator.KindXLSX, false, true},
		{venueplan.SourceJPEG, false, generator.KindImage, false, true},
		{venueplan.SourcePNG, false, generator.KindImage, false, true},
		{venueplan.SourceWEBP, true, generator.KindImage, true, true},
		{venueplan.SourceHEIC, true, generator.KindImage, true, true},
		{venueplan.SourceTIFF, true, generator.KindImage, true, true},
		{venueplan.SourceBMP, true, generator.KindImage, true, true},
		{venueplan.SourceGIF, true, generator.KindImage, true, true},
		{venueplan.SourceWEBP, false, "", false, false},
		{venueplan.SourceXLS, false, "", false, false},
		{venueplan.SourceCSV, false, "", false, false},
		{venueplan.SourceSVG, false, "", false, false},
	}
	for _, tc := range cases {
		k, usePNG, ok := SendKind(tc.kind, tc.png)
		if k != tc.want || usePNG != tc.usePNG || ok != tc.sendable {
			t.Errorf("SendKind(%s, png=%v) = %s %v %v", tc.kind, tc.png, k, usePNG, ok)
		}
	}
	// Every kind the CHECK allows is decided.
	for _, k := range venueplan.SourceKinds {
		_, _, _ = SendKind(k, true)
	}
}

func TestSelect_Limits(t *testing.T) {
	const mb = 1 << 20
	c := func(pos int, size int64) Candidate { return Candidate{Position: pos, Size: size} }
	positions := func(cs []Candidate) []int {
		var out []int
		for _, x := range cs {
			out = append(out, x.Position)
		}
		return out
	}
	eq := func(a, b []int) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	// A file over 10 MB is not sent; exactly 10 MB is.
	if got := positions(Select([]Candidate{c(0, 10*mb+1), c(1, 10*mb), c(2, 0)})); !eq(got, []int{1}) {
		t.Errorf("per-file limit: %v", got)
	}
	// Over 30 MB together: files go from the tail.
	if got := positions(Select([]Candidate{c(0, 9*mb), c(1, 9*mb), c(2, 9*mb), c(3, 9*mb)})); !eq(got, []int{0, 1, 2}) {
		t.Errorf("body limit: %v", got)
	}
	if got := positions(Select([]Candidate{c(0, 10*mb), c(1, 10*mb), c(2, 10*mb)})); !eq(got, []int{0, 1, 2}) {
		t.Errorf("exactly 30 MB: %v", got)
	}
	// At most 10 files.
	var many []Candidate
	for i := 0; i < 12; i++ {
		many = append(many, c(i, 1))
	}
	if got := Select(many); len(got) != 10 || got[9].Position != 9 {
		t.Errorf("file count: %v", positions(got))
	}
	if got := Select(nil); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}
