// delivery_367_test.go — tests for PR2-11: Enqueue ticket delivery email exactly once.
//
// Feature #367 (severity: MAJOR)
//
// Root cause: IssueTicketsForCheckout already called EnqueueDeliveryJobs
// internally, but checkout.go and payment_intents.go also called enqueueDelivery
// again after issueTickets returned — producing ≥2 delivery_jobs rows per
// ticket (and thus ≥2 emails per ticket).
//
// Fix:
//  1. Remove the redundant h.enqueueDelivery calls from checkout.go and
//     payment_intents.go. Delivery is now enqueued exclusively inside
//     IssueTicketsForCheckout / EnqueueDeliveryJobs (tickets.go /
//     delivery_enqueue.go).
//  2. Add a UNIQUE index on delivery_jobs(ticket_id) (migration 0067) so that
//     any future double-insert (e.g. webhook replay) is rejected at DB level.
//  3. Update InsertDeliveryJob SQL to use ON CONFLICT (ticket_id) DO UPDATE
//     (a no-op UPDATE) so the function always returns a row — making it
//     idempotent on webhook replay without producing a new row.
//
// Verification steps tested here:
//  Step 1 — InsertDeliveryJob uses ON CONFLICT (ticket_id)
//  Step 2 — Migration 0067 adds UNIQUE index on delivery_jobs(ticket_id)
//  Step 3 — checkout.go does NOT call h.enqueueDelivery separately
//  Step 4 — payment_intents.go does NOT call h.enqueueDelivery separately
//  Step 5 — hcheckout handler.go no longer declares enqueueDelivery field
//  Step 6 — hcheckout.New() no longer accepts enqueueDelivery parameter
//  Step 7 — EnqueueDeliveryJobs is called for newTickets only (not existing)
package httpserver

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: InsertDeliveryJob SQL uses ON CONFLICT (ticket_id)
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery367_Step1_InsertDeliveryJobHasOnConflict(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql")
	if !strings.Contains(content, "ON CONFLICT (ticket_id)") {
		t.Error("delivery_jobs.sql: InsertDeliveryJob must use ON CONFLICT (ticket_id) to prevent duplicate delivery jobs on webhook replay")
	}
}

func TestDelivery367_Step1_InsertDeliveryJobGenHasOnConflict(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql.go")
	if !strings.Contains(content, "ON CONFLICT (ticket_id)") {
		t.Error("delivery_jobs.sql.go: generated insertDeliveryJob constant must contain ON CONFLICT (ticket_id)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Migration 0067 adds UNIQUE index on delivery_jobs(ticket_id)
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery367_Step2_MigrationExists(t *testing.T) {
	content := findFileByName(t, "0067_delivery_jobs_unique_ticket.sql")
	if content == "" {
		t.Fatal("migration 0067_delivery_jobs_unique_ticket.sql not found")
	}
}

func TestDelivery367_Step2_MigrationCreatesUniqueIndex(t *testing.T) {
	content := findFileByName(t, "0067_delivery_jobs_unique_ticket.sql")
	if !strings.Contains(content, "CREATE UNIQUE INDEX") {
		t.Error("migration 0067 must CREATE UNIQUE INDEX on delivery_jobs(ticket_id)")
	}
}

func TestDelivery367_Step2_MigrationDeletesDuplicates(t *testing.T) {
	content := findFileByName(t, "0067_delivery_jobs_unique_ticket.sql")
	// Migration must remove pre-existing duplicate rows before adding unique constraint.
	if !strings.Contains(content, "DELETE FROM delivery_jobs") {
		t.Error("migration 0067 must DELETE duplicate delivery_jobs rows before adding UNIQUE index")
	}
}

func TestDelivery367_Step2_MigrationHasDownSection(t *testing.T) {
	content := findFileByName(t, "0067_delivery_jobs_unique_ticket.sql")
	if !strings.Contains(content, "+goose Down") {
		t.Error("migration 0067 must have a +goose Down section")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: checkout.go does NOT call h.enqueueDelivery after issueTickets
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery367_Step3_CheckoutGoNoDirectEnqueue(t *testing.T) {
	content := findFileByName(t, "checkout.go")
	if strings.Contains(content, "h.enqueueDelivery(") {
		t.Error("checkout.go must not call h.enqueueDelivery() — delivery is handled inside IssueTicketsForCheckout (feature #367)")
	}
}

func TestDelivery367_Step3_CheckoutGoDocumentsRemoval(t *testing.T) {
	content := findFileByName(t, "checkout.go")
	// The comment referencing feature #367 removal should be present for auditability.
	if !strings.Contains(content, "feature #367") {
		t.Error("checkout.go should contain a comment referencing feature #367 removal of the duplicate enqueueDelivery call")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: payment_intents.go does NOT call h.enqueueDelivery after issueTickets
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery367_Step4_PaymentIntentsGoNoDirectEnqueue(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if strings.Contains(content, "h.enqueueDelivery(") {
		t.Error("payment_intents.go must not call h.enqueueDelivery() — delivery is handled inside IssueTicketsForCheckout (feature #367)")
	}
}

func TestDelivery367_Step4_PaymentIntentsGoDocumentsRemoval(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if !strings.Contains(content, "feature #367") {
		t.Error("payment_intents.go should contain a comment referencing feature #367 removal of the duplicate enqueueDelivery call")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: hcheckout handler.go no longer declares enqueueDelivery field
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery367_Step5_HandlerGoNoEnqueueDeliveryField(t *testing.T) {
	content := findFileByName(t, "hcheckout_handler.go")
	if strings.Contains(content, "enqueueDelivery") {
		t.Error("hcheckout/handler.go must not declare an enqueueDelivery field — the callback was removed in feature #367")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: hcheckout.New() no longer accepts enqueueDelivery parameter
// (verified structurally via checkout_shims.go which calls hcheckout.New)
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery367_Step6_CheckoutShimsNoEnqueueDelivery(t *testing.T) {
	content := findFileByName(t, "checkout_shims.go")
	if strings.Contains(content, "enqueueDeliveryJobs") {
		t.Error("checkout_shims.go must not pass enqueueDeliveryJobs to hcheckout.New — parameter removed in feature #367")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: EnqueueDeliveryJobs is called for newTickets only inside tickets.go
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery367_Step7_TicketsGoEnqueuesOnlyNewTickets(t *testing.T) {
	content := findFileByName(t, "tickets.go")
	// tickets.go must call EnqueueDeliveryJobs with newTickets (not allTickets
	// or tickets) to avoid re-enqueueing delivery for already-issued tickets on
	// webhook replay / idempotent re-entry.
	if !strings.Contains(content, "EnqueueDeliveryJobs(ctx, newTickets)") {
		t.Error("tickets.go: IssueTicketsForCheckout must call EnqueueDeliveryJobs(ctx, newTickets) — passing all tickets would re-enqueue already-issued ones")
	}
}

func TestDelivery367_Step7_TicketsGoGuardsOnNewTickets(t *testing.T) {
	content := findFileByName(t, "tickets.go")
	// There must be a len(newTickets) > 0 guard before EnqueueDeliveryJobs.
	if !strings.Contains(content, "len(newTickets) > 0") {
		t.Error("tickets.go: must guard EnqueueDeliveryJobs call with len(newTickets) > 0 to skip on pure replay")
	}
}
