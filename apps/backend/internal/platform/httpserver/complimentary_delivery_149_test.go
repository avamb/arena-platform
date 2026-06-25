// complimentary_delivery_149_test.go — unit tests for feature #149
// (Complimentary delivery — invitation email template).
//
// Feature: Same delivery service as Wave 8 but template flagged as invitation.
// Batch email delivery for all recipients in a complimentary issuance.
//
// Test coverage:
//
//	Step 1: Invitation email template — constants + renderers in delivery/handler.go
//	Step 2: Payload.Template field in delivery package
//	Step 3: enqueueComplimentaryDeliveryJobs — exists in delivery_enqueue.go
//	Step 4: enqueueComplimentaryDeliveryJobs nil guard (no-op when deps absent)
//	Step 5: enqueueComplimentaryDeliveryJobs sets template="invitation" in payload
//	Step 6: handleCreateComplimentaryIssuance calls enqueueComplimentaryDeliveryJobs
//	Step 7: delivery handler branches on Template field (invitation vs ticket)
//	Step 8: Audit log strings for complimentary delivery
//	Step 9: Source file content checks for invitation renderers
package httpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Invitation email template constants
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryDelivery149_Step1_TemplateInvitationConstantExists(t *testing.T) {
	if delivery.TemplateInvitation == "" {
		t.Fatal("delivery.TemplateInvitation constant must be non-empty")
	}
}

func TestComplimentaryDelivery149_Step1_TemplateTicketConstantExists(t *testing.T) {
	if delivery.TemplateTicket == "" {
		t.Fatal("delivery.TemplateTicket constant must be non-empty")
	}
}

func TestComplimentaryDelivery149_Step1_TemplateConstantsAreDifferent(t *testing.T) {
	if delivery.TemplateTicket == delivery.TemplateInvitation {
		t.Fatalf("TemplateTicket and TemplateInvitation must be distinct values, both are %q",
			delivery.TemplateTicket)
	}
}

func TestComplimentaryDelivery149_Step1_TemplateInvitationValue(t *testing.T) {
	if delivery.TemplateInvitation != "invitation" {
		t.Errorf("expected TemplateInvitation=%q, got %q", "invitation", delivery.TemplateInvitation)
	}
}

func TestComplimentaryDelivery149_Step1_TemplateTicketValue(t *testing.T) {
	if delivery.TemplateTicket != "ticket" {
		t.Errorf("expected TemplateTicket=%q, got %q", "ticket", delivery.TemplateTicket)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Payload.Template field
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryDelivery149_Step2_PayloadHasTemplateField(t *testing.T) {
	p := delivery.Payload{
		TicketID: "550e8400-e29b-41d4-a716-446655440000",
		Template: delivery.TemplateInvitation,
	}
	if p.Template != delivery.TemplateInvitation {
		t.Errorf("Payload.Template not set correctly: got %q, want %q",
			p.Template, delivery.TemplateInvitation)
	}
	if p.TicketID == "" {
		t.Errorf("Payload.TicketID must round-trip; got empty")
	}
}

func TestComplimentaryDelivery149_Step2_PayloadTemplateOmitEmpty(t *testing.T) {
	// Template field should be omitempty in JSON
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, `Template string`) {
		t.Error("delivery_handler.go: Payload struct must have Template string field")
	}
	if !strings.Contains(content, `omitempty`) {
		t.Error("delivery_handler.go: Payload.Template field should use omitempty JSON tag")
	}
}

func TestComplimentaryDelivery149_Step2_PayloadDefaultTemplateIsEmpty(t *testing.T) {
	// A Payload without Template set should have empty Template (defaults to ticket)
	p := delivery.Payload{TicketID: "test-id"}
	if p.TicketID == "" {
		t.Errorf("Payload.TicketID must round-trip; got empty")
	}
	if p.Template != "" {
		t.Errorf("default Payload.Template should be empty, got %q", p.Template)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: enqueueComplimentaryDeliveryJobs — exists in delivery_enqueue.go
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryDelivery149_Step3_EnqueueFunctionExists(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, "enqueueComplimentaryDeliveryJobs") {
		t.Error("delivery_enqueue.go must contain enqueueComplimentaryDeliveryJobs function")
	}
}

func TestComplimentaryDelivery149_Step3_EnqueueFunctionTakesComplimentaryTicketRow(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, "ComplimentaryTicketRow") {
		t.Error("delivery_enqueue.go: enqueueComplimentaryDeliveryJobs must accept []gen.ComplimentaryTicketRow")
	}
}

func TestComplimentaryDelivery149_Step3_EnqueueFunctionIsMethod(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, "func (s *Server) enqueueComplimentaryDeliveryJobs") {
		t.Error("delivery_enqueue.go: enqueueComplimentaryDeliveryJobs must be a *Server method")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: enqueueComplimentaryDeliveryJobs nil guard
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryDelivery149_Step4_NilGuardNoPanic(t *testing.T) {
	// Server with no delivery deps — must not panic.
	s := buildComplimentaryDelivery149Server(t)
	tickets := []gen.ComplimentaryTicketRow{
		{
			ID:          uuid.New(),
			HolderEmail: strPtr("guest@example.com"),
		},
	}
	// Must be a safe no-op when deliveryJobQueries and workerPool are nil.
	s.enqueueComplimentaryDeliveryJobs(context.Background(), tickets)
}

func TestComplimentaryDelivery149_Step4_NilGuardEmptySlice(t *testing.T) {
	s := buildComplimentaryDelivery149Server(t)
	// Must not panic even with empty slice.
	s.enqueueComplimentaryDeliveryJobs(context.Background(), nil)
}

func TestComplimentaryDelivery149_Step4_NilGuardInSourceFile(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, "s.deliveryJobQueries == nil || s.workerPool == nil") {
		t.Error("delivery_enqueue.go: enqueueComplimentaryDeliveryJobs must have nil guard for deliveryJobQueries and workerPool")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: enqueueComplimentaryDeliveryJobs sets template="invitation"
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryDelivery149_Step5_PayloadUsesInvitationTemplate(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, "TemplateInvitation") {
		t.Error("delivery_enqueue.go: enqueueComplimentaryDeliveryJobs must set Template=delivery.TemplateInvitation")
	}
}

func TestComplimentaryDelivery149_Step5_PayloadTemplateFieldSet(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, "Template: delivery.TemplateInvitation") {
		t.Error("delivery_enqueue.go: Payload must have Template: delivery.TemplateInvitation in enqueueComplimentaryDeliveryJobs")
	}
}

func TestComplimentaryDelivery149_Step5_InvitationTemplateInDeliveryPackage(t *testing.T) {
	// Verify the delivery package is imported in enqueue file
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, `"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"`) {
		t.Error("delivery_enqueue.go: must import delivery package for TemplateInvitation constant")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: handleCreateComplimentaryIssuance calls enqueueComplimentaryDeliveryJobs
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryDelivery149_Step6_ComplimentaryGoCallsEnqueue(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	if !strings.Contains(content, "enqueueComplimentaryDeliveryJobs") {
		t.Error("complimentary.go: handleCreateComplimentaryIssuance must call s.enqueueComplimentaryDeliveryJobs")
	}
}

func TestComplimentaryDelivery149_Step6_EnqueueCalledAfterCommit(t *testing.T) {
	// The call to enqueueComplimentaryDeliveryJobs should appear after tx.Commit in the source.
	content := findFileByName(t, "complimentary.go")
	commitIdx := strings.Index(content, "tx.Commit")
	enqueueIdx := strings.Index(content, "enqueueComplimentaryDeliveryJobs")
	if commitIdx < 0 {
		t.Error("complimentary.go: expected tx.Commit call")
	}
	if enqueueIdx < 0 {
		t.Error("complimentary.go: expected enqueueComplimentaryDeliveryJobs call")
	}
	if commitIdx >= 0 && enqueueIdx >= 0 && enqueueIdx < commitIdx {
		t.Error("complimentary.go: enqueueComplimentaryDeliveryJobs must be called AFTER tx.Commit (post-commit, best-effort)")
	}
}

func TestComplimentaryDelivery149_Step6_EnqueueCalledWithTickets(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	// The call should pass the tickets slice to the enqueue function.
	if !strings.Contains(content, "enqueueComplimentaryDeliveryJobs(ctx, tickets)") {
		t.Error("complimentary.go: must call enqueueComplimentaryDeliveryJobs(ctx, tickets)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: delivery handler branches on Template field
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryDelivery149_Step7_HandlerHasTemplateBranch(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, "p.Template") {
		t.Error("delivery_handler.go: handler must branch on p.Template field")
	}
}

func TestComplimentaryDelivery149_Step7_HandlerHasInvitationCase(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, "TemplateInvitation") {
		t.Error("delivery_handler.go: handler must handle TemplateInvitation case")
	}
}

func TestComplimentaryDelivery149_Step7_HandlerHasDefaultCase(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	// Default case handles TemplateTicket and empty template
	if !strings.Contains(content, "default:") {
		t.Error("delivery_handler.go: handler switch must have a default case")
	}
}

func TestComplimentaryDelivery149_Step7_HandlerHasDistinctSubjects(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	// Invitation subject should differ from ticket subject
	if !strings.Contains(content, "You're invited") && !strings.Contains(content, "invited") {
		t.Error("delivery_handler.go: invitation email must have invitation-specific subject")
	}
	if !strings.Contains(content, "Your ticket") {
		t.Error("delivery_handler.go: standard delivery email must still have ticket subject")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: Audit log strings for complimentary delivery
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryDelivery149_Step8_EnqueueFileHasComplimentaryAuditLog(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, "complimentary delivery: invitation job enqueued") &&
		!strings.Contains(content, "invitation job enqueued") {
		t.Error("delivery_enqueue.go: must log 'invitation job enqueued' for complimentary delivery")
	}
}

func TestComplimentaryDelivery149_Step8_EnqueueFileHasTemplateLogField(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, `"template"`) {
		t.Error("delivery_enqueue.go: complimentary delivery log must include template field")
	}
}

func TestComplimentaryDelivery149_Step8_ComplimentaryGoHasBestEffortComment(t *testing.T) {
	content := findFileByName(t, "complimentary.go")
	// Best-effort comment documents that delivery failures don't roll back issuance
	if !strings.Contains(content, "best-effort") && !strings.Contains(content, "Best-effort") {
		t.Error("complimentary.go: must document that enqueueComplimentaryDeliveryJobs is best-effort")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: Source file content checks for invitation renderers
// ─────────────────────────────────────────────────────────────────────────────

func TestComplimentaryDelivery149_Step9_InvitationHTMLRendererExists(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, "renderInvitationEmailHTML") {
		t.Error("delivery_handler.go: must contain renderInvitationEmailHTML function")
	}
}

func TestComplimentaryDelivery149_Step9_InvitationTextRendererExists(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, "renderInvitationEmailText") {
		t.Error("delivery_handler.go: must contain renderInvitationEmailText function")
	}
}

func TestComplimentaryDelivery149_Step9_InvitationHTMLContainsInvitedText(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, "invited") {
		t.Error("delivery_handler.go: invitation email HTML must contain 'invited' text")
	}
}

func TestComplimentaryDelivery149_Step9_InvitationTextContainsComplimentaryText(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, "complimentary") {
		t.Error("delivery_handler.go: invitation email text must reference 'complimentary' nature of ticket")
	}
}

func TestComplimentaryDelivery149_Step9_InvitationEmailDistinctFromTicketEmail(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	// Invitation renderer must be a separate function from the ticket renderer
	if !strings.Contains(content, "renderInvitationEmailHTML") ||
		!strings.Contains(content, "renderTicketEmailHTML") {
		t.Error("delivery_handler.go: must have both renderInvitationEmailHTML and renderTicketEmailHTML as separate functions")
	}
}

func TestComplimentaryDelivery149_Step9_DeliveryPackageHasTemplateConstants(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, `TemplateTicket = "ticket"`) {
		t.Error("delivery_handler.go: must define TemplateTicket constant")
	}
	if !strings.Contains(content, `TemplateInvitation = "invitation"`) {
		t.Error("delivery_handler.go: must define TemplateInvitation constant")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helper: build a Server with no delivery deps for nil-guard tests
// ─────────────────────────────────────────────────────────────────────────────

// buildComplimentaryDelivery149Server returns a Server without delivery deps
// to verify that enqueueComplimentaryDeliveryJobs is a safe no-op.
func buildComplimentaryDelivery149Server(t *testing.T) *Server {
	t.Helper()
	// Reuse the existing complimentary server builder (no delivery deps).
	return buildComplimentaryServer(t)
}

// strPtr is a helper that returns a pointer to the given string.
// It avoids importing a helper package just for this.
func strPtr(s string) *string { return &s }
