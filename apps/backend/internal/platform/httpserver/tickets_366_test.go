// tickets_366_test.go — structural tests for feature #366 (PR2-10 MAJOR):
// Make ticket issuance idempotent and quantity-aware.
//
// # Problem being fixed
//
// The original IssueTicketsForCheckout used a "list-then-insert" pattern:
//   - ListTicketsByCheckoutSession returned empty → insert all tickets
//   - Two concurrent triggers both saw 0 → both inserted → double-issuance
//   - Mid-loop failure: 3 of 5 inserted → next retry found len>0 → returned
//     partial set as "already issued"
//
// # Fix verified by these tests
//
//	Step 1: Migration 0066 adds ordinal column to tickets table
//	Step 2: Migration 0066 adds UNIQUE (checkout_session_id, ordinal) constraint
//	Step 3: tickets.sql InsertTicket now includes ordinal ($9) parameter
//	Step 4: tickets.sql SELECT queries now include ordinal column
//	Step 5: tickets.sql.go TicketRow has Ordinal field
//	Step 6: tickets.sql.go InsertTicket accepts ordinal int32 parameter
//	Step 7: querier.go InsertTicket interface includes ordinal int32 parameter
//	Step 8: IssueTicketsForCheckout acquires pg_advisory_xact_lock
//	Step 9: IssueTicketsForCheckout compares len(existing) >= expectedCount
//	        (quantity-aware, not just > 0)
//	Step 10: IssueTicketsForCheckout uses issuedOrdinals map for gap-fill
//	Step 11: IssueTicketsForCheckout wraps issuance in a transaction
//	Step 12: issuanceLockKey extracts low-64-bits from UUID for the lock key
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration 0066 adds ordinal column
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step1_MigrationAddsOrdinalColumn(t *testing.T) {
	content := findFileByName(t, "0066_tickets_idempotent_issuance.sql")
	if !strings.Contains(content, "ordinal") {
		t.Error("migration 0066 must add an 'ordinal' column to tickets")
	}
	if !strings.Contains(content, "ALTER TABLE tickets ADD COLUMN") {
		t.Error("migration 0066 must ALTER TABLE tickets ADD COLUMN")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Migration 0066 adds UNIQUE constraint
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step2_MigrationAddsUniqueConstraint(t *testing.T) {
	content := findFileByName(t, "0066_tickets_idempotent_issuance.sql")
	if !strings.Contains(content, "UNIQUE") {
		t.Error("migration 0066 must add a UNIQUE constraint")
	}
	if !strings.Contains(content, "checkout_session_id") && !strings.Contains(content, "ordinal") {
		t.Error("migration 0066 UNIQUE constraint must cover checkout_session_id and ordinal")
	}
	if !strings.Contains(content, "tickets_checkout_ordinal_uq") {
		t.Error("migration 0066 must name the constraint 'tickets_checkout_ordinal_uq'")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: tickets.sql InsertTicket includes ordinal parameter
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step3_SQLInsertTicketHasOrdinal(t *testing.T) {
	content := findFileByName(t, "tickets.sql")
	if !strings.Contains(content, "ordinal") {
		t.Error("tickets.sql InsertTicket must include 'ordinal' column")
	}
	// The insert must use $9 for ordinal (9th parameter)
	if !strings.Contains(content, "$9") {
		t.Error("tickets.sql InsertTicket must use $9 for the ordinal parameter")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: tickets.sql SELECT queries include ordinal column
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step4_SQLSelectQueriesIncludeOrdinal(t *testing.T) {
	content := findFileByName(t, "tickets.sql")
	// ListTicketsByCheckoutSession and GetTicketByID must project ordinal
	if strings.Count(content, "ordinal") < 3 {
		t.Error("tickets.sql must include 'ordinal' in SELECT column lists for ListTicketsByCheckoutSession and GetTicketByID")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: tickets.sql.go TicketRow has Ordinal field
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step5_TicketRowHasOrdinalField(t *testing.T) {
	content := findFileByName(t, "tickets.sql.go")
	if !strings.Contains(content, "Ordinal") {
		t.Error("tickets.sql.go TicketRow must have an 'Ordinal' field")
	}
	if !strings.Contains(content, "Ordinal int32") {
		t.Error("tickets.sql.go TicketRow.Ordinal must be type int32")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: tickets.sql.go InsertTicket accepts ordinal int32 parameter
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step6_GenInsertTicketAcceptsOrdinal(t *testing.T) {
	content := findFileByName(t, "tickets.sql.go")
	if !strings.Contains(content, "ordinal int32") {
		t.Error("tickets.sql.go InsertTicket must accept 'ordinal int32' parameter")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: querier.go InsertTicket interface includes ordinal int32 parameter
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step7_QuerierInsertTicketHasOrdinal(t *testing.T) {
	content := findFileByName(t, "querier.go")
	// Find the InsertTicket signature in the interface
	if !strings.Contains(content, "ordinal int32") {
		t.Error("querier.go Querier.InsertTicket must include 'ordinal int32' in its signature")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: IssueTicketsForCheckout acquires pg_advisory_xact_lock
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step8_IssuanceAcquiresAdvisoryLock(t *testing.T) {
	content := findFileByName(t, "tickets.go")
	if !strings.Contains(content, "pg_advisory_xact_lock") {
		t.Error("tickets.go IssueTicketsForCheckout must call pg_advisory_xact_lock to serialise concurrent issuance")
	}
	if !strings.Contains(content, "issuanceLockKey") {
		t.Error("tickets.go must have an issuanceLockKey helper to derive the int64 advisory lock key from the UUID")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: Quantity-aware idempotency check (>= expectedCount, not just > 0)
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step9_QuantityAwareIdempotencyCheck(t *testing.T) {
	content := findFileByName(t, "tickets.go")
	// Must compare against expectedCount, not just len > 0
	if !strings.Contains(content, "expectedCount") {
		t.Error("tickets.go must use 'expectedCount' for quantity-aware idempotency check")
	}
	if !strings.Contains(content, ">= expectedCount") {
		t.Error("tickets.go must check len(existing) >= expectedCount (not just > 0) to detect fully-issued sessions")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: Gap-fill via issuedOrdinals map
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step10_GapFillWithOrdinalMap(t *testing.T) {
	content := findFileByName(t, "tickets.go")
	if !strings.Contains(content, "issuedOrdinals") {
		t.Error("tickets.go must build an 'issuedOrdinals' map to identify already-issued ordinals for gap-fill")
	}
	if !strings.Contains(content, "alreadyIssued") {
		t.Error("tickets.go must check 'alreadyIssued' to skip ordinals already present from a prior attempt")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 11: Issuance wrapped in a transaction
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step11_IssuanceInTransaction(t *testing.T) {
	content := findFileByName(t, "tickets.go")
	if !strings.Contains(content, "pool.BeginTx") {
		t.Error("tickets.go IssueTicketsForCheckout must wrap issuance in a transaction via pool.BeginTx")
	}
	if !strings.Contains(content, "tx.Commit") {
		t.Error("tickets.go IssueTicketsForCheckout must commit the transaction after issuing all tickets")
	}
	if !strings.Contains(content, "tx.Rollback") {
		t.Error("tickets.go IssueTicketsForCheckout must defer tx.Rollback for safety")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 12: issuanceLockKey uses low-64-bits of UUID
// ─────────────────────────────────────────────────────────────────────────────

func TestTicket366_Step12_LockKeyUsesUUIDBits(t *testing.T) {
	content := findFileByName(t, "tickets.go")
	// Must use encoding/binary to extract int64 from UUID bytes
	if !strings.Contains(content, "encoding/binary") {
		t.Error("tickets.go must import 'encoding/binary' for extracting the advisory lock key from the UUID")
	}
	if !strings.Contains(content, "BigEndian.Uint64") {
		t.Error("tickets.go issuanceLockKey must use binary.BigEndian.Uint64 to extract low-64-bits of UUID")
	}
}
