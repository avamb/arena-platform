package venueplan

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

// TestCanTransition_FullTable checks every (from, to) pair of the 7×7 table
// against the diagram of spec 64 section 4.2 plus the wave-1 additions.
func TestCanTransition_FullTable(t *testing.T) {
	allowed := map[[2]State]bool{
		{StateReceived, StateGenerating}: true,
		{StateReceived, StateReview}:     true, // a clean source SVG is draft 1
		{StateReceived, StateOperator}:   true, // no generator / unsupported format
		{StateReceived, StateCancelled}:  true,

		{StateGenerating, StateReview}:    true,
		{StateGenerating, StateOperator}:  true,
		{StateGenerating, StateCancelled}: true,

		{StateReview, StateGenerating}: true, // answers rendered into a new draft
		{StateReview, StateOperator}:   true, // escalate
		{StateReview, StateApproved}:   true,
		{StateReview, StateCancelled}:  true,

		{StateOperator, StateReview}:    true, // returned to the organizer
		{StateOperator, StateApproved}:  true,
		{StateOperator, StateRejected}:  true,
		{StateOperator, StateCancelled}: true,
	}
	if len(States) != 7 {
		t.Fatalf("States has %d values, want 7", len(States))
	}
	for _, from := range States {
		for _, to := range States {
			want := allowed[[2]State{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
	if CanTransition("", StateReview) || CanTransition(StateReview, "bogus") || CanTransition("bogus", StateCancelled) {
		t.Error("an unknown state must never transition")
	}
}

func TestFinalStates(t *testing.T) {
	final := map[State]bool{StateApproved: true, StateRejected: true, StateCancelled: true}
	for _, s := range States {
		if IsFinal(s) != final[s] {
			t.Errorf("IsFinal(%s) = %v, want %v", s, IsFinal(s), final[s])
		}
		// Every non-final state can be cancelled; a final state goes nowhere.
		if !final[s] && !CanTransition(s, StateCancelled) {
			t.Errorf("%s cannot be cancelled", s)
		}
		if final[s] {
			for _, to := range States {
				if CanTransition(s, to) {
					t.Errorf("final %s -> %s allowed", s, to)
				}
			}
		}
		if !s.Valid() {
			t.Errorf("%s not Valid", s)
		}
	}
	if State("bogus").Valid() || IsFinal("bogus") {
		t.Error("unknown state accepted")
	}
}

// migration0216Up is the Up section of the migration that defines the
// intake tables; every Go value list must match its CHECK constraints.
func migration0216Up(t *testing.T) string {
	t.Helper()
	return migrationUp(t, "0216_venue_plan_intake.sql")
}

// migrationUp is the Up section of one migration file.
func migrationUp(t *testing.T, name string) string {
	t.Helper()
	raw, err := migrations.FS.ReadFile(migrations.Dir + "/" + name)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	up := string(raw)
	if i := strings.Index(up, "-- +goose Down"); i >= 0 {
		up = up[:i]
	}
	return up
}

// checkBody returns the text of the named CHECK constraint up to the next
// CONSTRAINT keyword or the end of the statement.
func checkBody(t *testing.T, up, name string) string {
	t.Helper()
	i := strings.Index(up, "CONSTRAINT "+name)
	if i < 0 {
		t.Fatalf("constraint %s not found", name)
	}
	rest := up[i+len("CONSTRAINT "+name):]
	if j := strings.Index(rest, "CONSTRAINT "); j >= 0 {
		rest = rest[:j]
	}
	if j := strings.Index(rest, ";"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestValues_MatchMigrationChecks(t *testing.T) {
	up := migration0216Up(t)
	expect := func(constraint string, values []string) {
		t.Helper()
		body := checkBody(t, up, constraint)
		for _, v := range values {
			if !strings.Contains(body, "'"+v+"'") {
				t.Errorf("%s does not allow %q", constraint, v)
			}
		}
		if got := strings.Count(body, "'"); got != 2*len(values) {
			t.Errorf("%s lists %d values, Go has %d", constraint, got/2, len(values))
		}
	}
	var s []string
	for _, x := range States {
		s = append(s, string(x))
	}
	expect("venue_plan_intakes_state_check", s)
	expect("venue_plan_intakes_channel_check", []string{string(ChannelTelegramBot), string(ChannelEventCenterWeb)})
	s = nil
	for _, x := range EscalationReasons {
		s = append(s, string(x))
	}
	expect("venue_plan_intakes_escalation_reason_check", s)
	s = nil
	for _, x := range SourceKinds {
		s = append(s, string(x))
	}
	expect("venue_plan_intake_sources_kind_check", s)
	expect("venue_plan_drafts_author_check", []string{
		string(AuthorGenerator), string(AuthorOrganizerAnswers), string(AuthorOrganizerText),
		string(AuthorOperator), string(AuthorOperatorSVG), string(AuthorSourceSVG),
	})
	expect("venue_plan_corrections_kind_check", []string{
		string(CorrectionOrganizerText), string(CorrectionOperatorEdit), string(CorrectionAnswer),
	})
	expect("venue_plan_corrections_status_check", []string{
		string(CorrectionPending), string(CorrectionApplied), string(CorrectionRejected),
	})
	// Migration 0217 widened the notice kinds by escalated.
	up0217 := migrationUp(t, "0217_venue_plan_notice_escalated.sql")
	if i := strings.Index(up0217, "ADD CONSTRAINT"); i >= 0 {
		up0217 = up0217[i:] // past the DROP of the old CHECK
	}
	body := checkBody(t, up0217, "venue_plan_notices_kind_check")
	notices := []string{
		string(NoticeDraftReady), string(NoticeReturned), string(NoticeRejected), string(NoticeApproved),
		string(NoticeEscalated),
	}
	for _, v := range notices {
		if !strings.Contains(body, "'"+v+"'") {
			t.Errorf("venue_plan_notices_kind_check (0217) does not allow %q", v)
		}
	}
	if got := strings.Count(body, "'"); got != 2*len(notices) {
		t.Errorf("venue_plan_notices_kind_check (0217) lists %d values, Go has %d", got/2, len(notices))
	}
	expect("seating_plans_verification_state_check", []string{
		string(VerificationUnverified), string(VerificationVerified), string(VerificationNeedsFix),
	})
}

func TestValidators(t *testing.T) {
	if !ChannelTelegramBot.Valid() || Channel("email").Valid() {
		t.Error("Channel.Valid")
	}
	if !EscalationAnswers.Valid() || EscalationReason("other").Valid() {
		t.Error("EscalationReason.Valid")
	}
	if !SourceHEIC.Valid() || SourceKind("docx").Valid() {
		t.Error("SourceKind.Valid")
	}
	if !VerificationNeedsFix.Valid() || VerificationState("").Valid() {
		t.Error("VerificationState.Valid")
	}
}
