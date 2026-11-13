package generate

import (
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/generator"
)

// MaxAttempts is how many generator attempts one intake gets (spec 64
// section 4.4): attempt 2 runs 1 minute after attempt 1 failed, attempt 3 five
// minutes after attempt 2; a failed attempt 3 goes to an administrator.
const MaxAttempts = 3

// RetryDelays[i] is the wait before attempt i+2.
var RetryDelays = []time.Duration{time.Minute, 5 * time.Minute}

// Decision is what one generator outcome does to the intake.
type Decision struct {
	// Retry: the intake stays generating and attempt+1 is queued.
	Retry bool
	// State is the new state (review or operator) when not Retry.
	State venueplan.State
	// Reason is the escalation reason when State is operator.
	Reason venueplan.EscalationReason
	// KeepDraft: the generated SVG becomes a draft of the intake.
	KeepDraft bool
}

// Decide maps one generator outcome to the intake (the table of the wave-2
// design): res and genErr are what Generate returned, svgValid whether the
// returned SVG passed intake.CheckPlanSVG, attempt the number of this attempt
// (1..MaxAttempts).
func Decide(res generator.Result, genErr error, svgValid bool, attempt int) Decision {
	operator := func(r venueplan.EscalationReason, keep bool) Decision {
		return Decision{State: venueplan.StateOperator, Reason: r, KeepDraft: keep}
	}
	if genErr != nil {
		if generator.Retryable(genErr) && attempt < MaxAttempts {
			return Decision{Retry: true}
		}
		return operator(venueplan.EscalationGeneratorFailed, false)
	}
	switch res.Result {
	case generator.ResultNoAdapter:
		for _, f := range res.Files {
			if f.Outcome == "failed" {
				return operator(venueplan.EscalationGeneratorFailed, false)
			}
		}
		return operator(venueplan.EscalationUnsupportedFormat, false)
	case generator.ResultOK:
		if len(res.SVG) == 0 {
			// No seat has an identity (a vector PDF without row labels).
			return operator(venueplan.EscalationTooManyQuestions, false)
		}
		if res.ValidationStatus == generator.StatusPass {
			if svgValid {
				return Decision{State: venueplan.StateReview, KeepDraft: true}
			}
			return operator(venueplan.EscalationDraftInvalid, false)
		}
		// QUARANTINE (or no status): the question loop does not exist yet, so
		// an administrator finishes it; a valid draft is kept for them.
		return operator(venueplan.EscalationTooManyQuestions, svgValid)
	}
	return operator(venueplan.EscalationGeneratorFailed, false)
}

// Candidate is one source file of an intake the generator could take.
type Candidate struct {
	Position int
	Name     string
	// Send is the declared kind for POST /generate.
	Send generator.Kind
	// StorageKey, ContentType and Size describe the object sent: the source
	// itself or its PNG copy.
	StorageKey  string
	ContentType string
	Size        int64
}

// SendKind says how a source of kind k is sent: as which generator kind and
// whether its PNG copy is sent instead of the file. ok=false: the generator
// has no reader for it (xls, csv, svg, or a picture without its PNG copy).
func SendKind(k venueplan.SourceKind, hasPNG bool) (kind generator.Kind, usePNG, ok bool) {
	switch k {
	case venueplan.SourcePDF:
		return generator.KindPDF, false, true
	case venueplan.SourceXLSX:
		return generator.KindXLSX, false, true
	case venueplan.SourceJPEG, venueplan.SourcePNG:
		return generator.KindImage, false, true
	case venueplan.SourceWEBP, venueplan.SourceHEIC, venueplan.SourceTIFF, venueplan.SourceBMP, venueplan.SourceGIF:
		if hasPNG {
			return generator.KindImage, true, true
		}
	}
	return "", false, false
}

// Select applies the generator's limits: a file over 10 MB is not sent, at
// most 10 files go, and files are dropped from the tail while the body is
// over 30 MB. The order is kept.
func Select(in []Candidate) []Candidate {
	var out []Candidate
	for _, c := range in {
		if c.Size <= 0 || c.Size > generator.MaxFileBytes {
			continue
		}
		out = append(out, c)
		if len(out) == generator.MaxFiles {
			break
		}
	}
	for len(out) > 0 && total(out) > generator.MaxBodyBytes {
		out = out[:len(out)-1]
	}
	return out
}

func total(cs []Candidate) int64 {
	var n int64
	for _, c := range cs {
		n += c.Size
	}
	return n
}
