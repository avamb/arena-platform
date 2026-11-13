// Package venueplan is the domain of a hall plan intake (spec
// 08_architecture/64): an organizer sends the files of a hall plan, arena
// builds a draft, the organizer checks it, a platform administrator steps in
// when the draft cannot be built, and the approved draft becomes a seating
// plan that a platform superadmin later verifies.
//
// The package holds the state machine of spec section 4.2, the values the
// CHECK constraints of migration 0216 allow, and the seams of the later
// waves (Renderer, TextInterpreter). It has no database and no HTTP: the
// storage is gen/venue_plan_intakes.sql.go, the routes come in stage B.
package venueplan

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// State is venue_plan_intakes.state.
type State string

// States of an intake (spec 4.2).
const (
	// StateReceived: the files are stored, nothing was built yet.
	StateReceived State = "received"
	// StateGenerating: the generator builds a draft (wave 2).
	StateGenerating State = "generating"
	// StateReview: the organizer checks the current draft.
	StateReview State = "review"
	// StateOperator: a platform administrator builds or fixes the plan.
	StateOperator State = "operator"
	// StateApproved: final, a seating plan was made from a draft.
	StateApproved State = "approved"
	// StateRejected: final, the administrator could not build a plan.
	StateRejected State = "rejected"
	// StateCancelled: final, the organizer cancelled the intake.
	StateCancelled State = "cancelled"
)

// States lists every state, in the order of the diagram.
var States = []State{StateReceived, StateGenerating, StateReview, StateOperator, StateApproved, StateRejected, StateCancelled}

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	for _, x := range States {
		if s == x {
			return true
		}
	}
	return false
}

// IsFinal reports whether s is a final state: approved, rejected or
// cancelled. A final intake never changes again; its closed_at is set.
func IsFinal(s State) bool {
	return s == StateApproved || s == StateRejected || s == StateCancelled
}

// transitions is the diagram of spec section 4.2:
//
//	received ─▶ generating ─▶ review ◀──────────┐
//	               │            │  │  ▲          │
//	               │            │  │  └ answers ─┘ (new draft)
//	               ▼            ▼  ▼
//	            operator ◀── escalate   approved (final)
//	               │
//	               ├─▶ review (returned to the organizer)
//	               └─▶ rejected (final)
//
//	every non-final state ─▶ cancelled (final)
//
// Additions the wave-1 rules need, both read from the spec text:
//   - received -> review: a source SVG that imports cleanly is draft 1 at
//     once, there is nothing to generate;
//   - received -> operator: no generator (wave 1) or a format no adapter
//     reads goes to an administrator at once;
//   - review -> generating: answers or a text correction are rendered into a
//     new draft by a worker job (wave 2);
//   - operator -> approved: an administrator approves a draft himself.
//
// A new draft that keeps the intake in review (the answers loop of the
// diagram) or in operator (an administrator's SVG) is NOT a transition: the
// state does not change, only current_draft. So no state goes to itself.
var transitions = map[State][]State{
	StateReceived:   {StateGenerating, StateReview, StateOperator, StateCancelled},
	StateGenerating: {StateReview, StateOperator, StateCancelled},
	StateReview:     {StateGenerating, StateOperator, StateApproved, StateCancelled},
	StateOperator:   {StateReview, StateApproved, StateRejected, StateCancelled},
}

// CanTransition reports whether an intake may move from one state to the
// other. Final states go nowhere; a state never goes to itself.
func CanTransition(from, to State) bool {
	for _, x := range transitions[from] {
		if x == to {
			return true
		}
	}
	return false
}

// Channel is venue_plan_intakes.channel: where the intake was sent from.
type Channel string

const (
	ChannelTelegramBot    Channel = "telegram_bot"
	ChannelEventCenterWeb Channel = "event_center_web"
)

// Valid reports whether c is a known channel.
func (c Channel) Valid() bool { return c == ChannelTelegramBot || c == ChannelEventCenterWeb }

// EscalationReason is venue_plan_intakes.escalation_reason: why the intake
// went to an administrator.
type EscalationReason string

const (
	// EscalationNoGenerator: no generator is connected (wave 1), every
	// non-SVG source goes to an administrator.
	EscalationNoGenerator EscalationReason = "no_generator"
	// EscalationGeneratorFailed: the generator failed after its retries.
	EscalationGeneratorFailed EscalationReason = "generator_failed"
	// EscalationDraftInvalid: the generated SVG did not pass ImportSVG.
	EscalationDraftInvalid EscalationReason = "draft_invalid"
	// EscalationTooManyQuestions: the draft needs more answers than an
	// organizer can reasonably give.
	EscalationTooManyQuestions EscalationReason = "too_many_questions"
	// EscalationOrganizerRequest: the organizer pressed "Call an
	// administrator" (escalate, with an optional text).
	EscalationOrganizerRequest EscalationReason = "organizer_request"
	// EscalationOrganizerText: a text correction no interpreter could apply
	// (wave 1 has none).
	EscalationOrganizerText EscalationReason = "organizer_text"
	// EscalationAnswers: answers no renderer could apply (wave 1 has none).
	EscalationAnswers EscalationReason = "answers"
	// EscalationUnsupportedFormat: a file kind no adapter reads.
	EscalationUnsupportedFormat EscalationReason = "unsupported_format"
)

// EscalationReasons lists every reason the CHECK allows.
var EscalationReasons = []EscalationReason{
	EscalationNoGenerator, EscalationGeneratorFailed, EscalationDraftInvalid, EscalationTooManyQuestions,
	EscalationOrganizerRequest, EscalationOrganizerText, EscalationAnswers, EscalationUnsupportedFormat,
}

// Valid reports whether r is a known reason.
func (r EscalationReason) Valid() bool {
	for _, x := range EscalationReasons {
		if r == x {
			return true
		}
	}
	return false
}

// MaxEscalationText is the longest text an organizer may send with escalate
// (spec 4.6), in characters.
const MaxEscalationText = 1000

// MaxDraftSVGBytes is the biggest draft SVG (the SVG limit of spec 2.1): the
// admin draft routes and approve apply it, and so does the generator job to
// an SVG the generator returns.
const MaxDraftSVGBytes = 3 << 20

// SourceKind is venue_plan_intake_sources.kind.
type SourceKind string

const (
	SourcePDF  SourceKind = "pdf"
	SourceXLSX SourceKind = "xlsx"
	SourceXLS  SourceKind = "xls"
	SourceCSV  SourceKind = "csv"
	SourceJPEG SourceKind = "jpeg"
	SourcePNG  SourceKind = "png"
	SourceWEBP SourceKind = "webp"
	SourceHEIC SourceKind = "heic"
	SourceTIFF SourceKind = "tiff"
	SourceBMP  SourceKind = "bmp"
	SourceGIF  SourceKind = "gif"
	SourceSVG  SourceKind = "svg"
)

// SourceKinds lists every kind the CHECK allows.
var SourceKinds = []SourceKind{
	SourcePDF, SourceXLSX, SourceXLS, SourceCSV, SourceJPEG, SourcePNG, SourceWEBP,
	SourceHEIC, SourceTIFF, SourceBMP, SourceGIF, SourceSVG,
}

// Valid reports whether k is a known kind.
func (k SourceKind) Valid() bool {
	for _, x := range SourceKinds {
		if k == x {
			return true
		}
	}
	return false
}

// Author is venue_plan_drafts.author: who made a draft.
type Author string

const (
	AuthorGenerator        Author = "generator"
	AuthorOrganizerAnswers Author = "organizer_answers"
	AuthorOrganizerText    Author = "organizer_text"
	AuthorOperator         Author = "operator"
	AuthorOperatorSVG      Author = "operator_svg"
	AuthorSourceSVG        Author = "source_svg"
)

// CorrectionKind is venue_plan_corrections.kind.
type CorrectionKind string

const (
	CorrectionOrganizerText CorrectionKind = "organizer_text"
	CorrectionOperatorEdit  CorrectionKind = "operator_edit"
	CorrectionAnswer        CorrectionKind = "answer"
)

// CorrectionStatus is venue_plan_corrections.status.
type CorrectionStatus string

const (
	CorrectionPending  CorrectionStatus = "pending"
	CorrectionApplied  CorrectionStatus = "applied"
	CorrectionRejected CorrectionStatus = "rejected"
)

// NoticeKind is venue_plan_notices.kind: what the bot tells the creator.
type NoticeKind string

const (
	NoticeDraftReady NoticeKind = "draft_ready"
	NoticeReturned   NoticeKind = "returned"
	NoticeRejected   NoticeKind = "rejected"
	NoticeApproved   NoticeKind = "approved"
	// NoticeEscalated: the generator could not build a draft, an
	// administrator builds the plan now (migration 0217).
	NoticeEscalated NoticeKind = "escalated"
)

// VerificationState is seating_plans.verification_state. NULL (no value) is
// a plan never sent for verification.
type VerificationState string

const (
	VerificationUnverified VerificationState = "unverified"
	VerificationVerified   VerificationState = "verified"
	VerificationNeedsFix   VerificationState = "needs_fix"
)

// Valid reports whether v is a known verification state.
func (v VerificationState) Valid() bool {
	return v == VerificationUnverified || v == VerificationVerified || v == VerificationNeedsFix
}

// ── seams of the later waves ────────────────────────────────────────────────

// Answer is the organizer's answer to one question of a draft.
type Answer struct {
	QuestionID uuid.UUID       `json:"question_id"`
	Answer     json.RawMessage `json:"answer"`
}

// RenderInput is what a Renderer turns into a new draft: the model of draft
// FromDraft and the answers given on it.
type RenderInput struct {
	IntakeID  uuid.UUID
	FromDraft int
	Model     json.RawMessage
	Answers   []Answer
}

// RenderOutput is a new draft built by a Renderer.
type RenderOutput struct {
	// Model is the plan model (seating-json-v1).
	Model json.RawMessage
	// SVG is the authoring SVG; PreviewPNG may be empty.
	SVG        []byte
	PreviewPNG []byte
	// Report is the summary (seats, rows, sections, plan_type, warnings,
	// problems).
	Report           json.RawMessage
	GeneratorVersion string
}

// Renderer builds a new draft from a model and the organizer's answers (the
// generator's POST /render, spec 4.4 venue_plan.render). It is nil in wave 1:
// answers are stored and the intake goes to an administrator (escalation
// reason answers). The generator wave implements it.
type Renderer interface {
	Render(ctx context.Context, in RenderInput) (RenderOutput, error)
}

// TextInput is a text correction to interpret against the model of a draft.
type TextInput struct {
	IntakeID uuid.UUID
	Draft    int
	Model    json.RawMessage
	Text     string
	// Locale is the organizer's language (en, ru, he, ...).
	Locale string
}

// TextInterpreter turns a text correction ("row 5 has 22 seats") into model
// operations (spec 4.10). It is nil in wave 1: the text is stored and the
// intake goes to an administrator (escalation reason organizer_text). The
// text-to-operations wave implements it. A text it cannot read returns
// ErrNotUnderstood.
type TextInterpreter interface {
	Interpret(ctx context.Context, in TextInput) (operations json.RawMessage, err error)
}

// ErrNotUnderstood is returned by a TextInterpreter for a text it cannot turn
// into operations; the intake then goes to an administrator.
var ErrNotUnderstood = errors.New("venueplan: the text correction was not understood")
