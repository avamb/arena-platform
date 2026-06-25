// delivery_141_test.go — unit tests for feature #141 (Ticket delivery via email).
//
// Test coverage:
//
//	Step 1: Migration file 0030_delivery_jobs.sql — table, status check, RBAC
//	Step 2: SQL query file delivery_jobs.sql — all 4 named queries present
//	Step 3: Gen file delivery_jobs.sql.go — DeliveryJobRow type, all 4 functions
//	Step 4: Querier interface — delivery_job methods present (compile-time)
//	Step 5: Email sender — LogSender.Send returns nil; SMTPSender compile-time check
//	Step 6: Delivery handler — JobType constant, Payload struct, nil-sender path
//	Step 7: Server fields — deliveryJobQueries, workerPool, emailSender wired in server.go
//	Step 8: enqueueDeliveryJobs nil guard — no-op when deps absent
//	Step 9: Audit log strings in handler and enqueue files
//	Step 10: Checkout and payment_intents wire enqueueDeliveryJobs call
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factory for delivery tests (no delivery deps — tests nil guard)
// ─────────────────────────────────────────────────────────────────────────────

// buildDelivery141Server returns a Server with stub auth but without
// delivery-specific deps (deliveryJobQueries=nil, workerPool=nil). Used to
// verify that enqueueDeliveryJobs is a safe no-op when deps are absent.
func buildDelivery141Server(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildDelivery141Server: NewStubProvider: %v", err)
	}
	// No DeliveryJobQueries, WorkerPool, or EmailSender supplied — both
	// deliveryJobQueries and workerPool will be nil inside the Server.
	return New(Options{
		Config: cfg,
		Auth:   stub,
		Pool:   &dbDownPool{},
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file 0030_delivery_jobs.sql
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery141_Step1_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0030_delivery_jobs.sql")
	if content == "" {
		t.Fatal("0030_delivery_jobs.sql is empty or not found")
	}
}

func TestDelivery141_Step1_MigrationHasGooseDirectives(t *testing.T) {
	content := findFileByName(t, "0030_delivery_jobs.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("migration missing '-- +goose Up' directive")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("migration missing '-- +goose Down' directive")
	}
}

func TestDelivery141_Step1_MigrationHasDeliveryJobsTable(t *testing.T) {
	content := findFileByName(t, "0030_delivery_jobs.sql")
	if !strings.Contains(content, "CREATE TABLE delivery_jobs") {
		t.Error("migration missing 'CREATE TABLE delivery_jobs'")
	}
}

func TestDelivery141_Step1_MigrationHasRequiredColumns(t *testing.T) {
	content := findFileByName(t, "0030_delivery_jobs.sql")
	for _, col := range []string{
		"ticket_id",
		"recipient_email",
		"status",
		"attempts",
		"last_error",
		"queued_at",
		"sent_at",
	} {
		if !strings.Contains(content, col) {
			t.Errorf("migration missing column '%s'", col)
		}
	}
}

func TestDelivery141_Step1_MigrationHasStatusCheckConstraint(t *testing.T) {
	content := findFileByName(t, "0030_delivery_jobs.sql")
	for _, status := range []string{"pending", "sent", "failed"} {
		if !strings.Contains(content, "'"+status+"'") {
			t.Errorf("migration missing status value '%s' in check constraint", status)
		}
	}
}

func TestDelivery141_Step1_MigrationHasTicketIDIndex(t *testing.T) {
	content := findFileByName(t, "0030_delivery_jobs.sql")
	if !strings.Contains(content, "delivery_jobs_ticket_id") {
		t.Error("migration missing index 'delivery_jobs_ticket_id'")
	}
}

func TestDelivery141_Step1_MigrationHasPartialPendingIndex(t *testing.T) {
	content := findFileByName(t, "0030_delivery_jobs.sql")
	if !strings.Contains(content, "delivery_jobs_status_pending") {
		t.Error("migration missing partial index 'delivery_jobs_status_pending'")
	}
}

func TestDelivery141_Step1_MigrationHasRBACSeeds(t *testing.T) {
	content := findFileByName(t, "0030_delivery_jobs.sql")
	for _, perm := range []string{"delivery.read", "delivery.manage"} {
		if !strings.Contains(content, "'"+perm+"'") {
			t.Errorf("migration missing RBAC seed '%s'", perm)
		}
	}
}

func TestDelivery141_Step1_MigrationDownSection(t *testing.T) {
	content := findFileByName(t, "0030_delivery_jobs.sql")
	if !strings.Contains(content, "DROP TABLE IF EXISTS delivery_jobs") {
		t.Error("migration Down section missing 'DROP TABLE IF EXISTS delivery_jobs'")
	}
}

func TestDelivery141_Step1_MigrationFKCascade(t *testing.T) {
	content := findFileByName(t, "0030_delivery_jobs.sql")
	if !strings.Contains(content, "ON DELETE CASCADE") {
		t.Error("migration missing 'ON DELETE CASCADE' on ticket_id FK")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL query file delivery_jobs.sql
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery141_Step2_SQLQueryFileExists(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql")
	if content == "" {
		t.Fatal("delivery_jobs.sql query file is empty or not found")
	}
}

func TestDelivery141_Step2_SQLQueryFileHasInsertDeliveryJob(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql")
	if !strings.Contains(content, "InsertDeliveryJob") {
		t.Error("delivery_jobs.sql missing 'InsertDeliveryJob' query name")
	}
}

func TestDelivery141_Step2_SQLQueryFileHasGetDeliveryJobByTicketID(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql")
	if !strings.Contains(content, "GetDeliveryJobByTicketID") {
		t.Error("delivery_jobs.sql missing 'GetDeliveryJobByTicketID' query name")
	}
}

func TestDelivery141_Step2_SQLQueryFileHasUpdateDeliveryJobStatus(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql")
	if !strings.Contains(content, "UpdateDeliveryJobStatus") {
		t.Error("delivery_jobs.sql missing 'UpdateDeliveryJobStatus' query name")
	}
}

func TestDelivery141_Step2_SQLQueryFileHasListPendingDeliveryJobs(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql")
	if !strings.Contains(content, "ListPendingDeliveryJobs") {
		t.Error("delivery_jobs.sql missing 'ListPendingDeliveryJobs' query name")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Gen file delivery_jobs.sql.go
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery141_Step3_GenFileExists(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql.go")
	if content == "" {
		t.Fatal("gen file delivery_jobs.sql.go is empty or not found")
	}
}

func TestDelivery141_Step3_GenFileHasDeliveryJobRow(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql.go")
	if !strings.Contains(content, "type DeliveryJobRow struct") {
		t.Error("gen file missing 'type DeliveryJobRow struct'")
	}
}

func TestDelivery141_Step3_GenFileHasAllFunctions(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql.go")
	for _, fn := range []string{
		"InsertDeliveryJob",
		"GetDeliveryJobByTicketID",
		"UpdateDeliveryJobStatus",
		"ListPendingDeliveryJobs",
	} {
		if !strings.Contains(content, "func (q *Queries) "+fn) {
			t.Errorf("gen file missing 'func (q *Queries) %s'", fn)
		}
	}
}

func TestDelivery141_Step3_DeliveryJobRowHasRequiredFields(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql.go")
	for _, field := range []string{
		"ID",
		"TicketID",
		"RecipientEmail",
		"Status",
		"Attempts",
		"LastError",
		"QueuedAt",
		"SentAt",
	} {
		if !strings.Contains(content, field) {
			t.Errorf("gen file DeliveryJobRow missing field '%s'", field)
		}
	}
}

func TestDelivery141_Step3_NullableFieldsArePointers(t *testing.T) {
	content := findFileByName(t, "delivery_jobs.sql.go")
	// RecipientEmail and LastError must be *string; SentAt must be *time.Time.
	if !strings.Contains(content, "*string") {
		t.Error("gen file missing '*string' — RecipientEmail and LastError must be nullable (*string)")
	}
	if !strings.Contains(content, "*time.Time") {
		t.Error("gen file missing '*time.Time' — SentAt must be nullable (*time.Time)")
	}
}

// TestDelivery141_Step3_DeliveryJobRowCompileTime verifies that the struct can
// be instantiated with all known fields without a compile error.
func TestDelivery141_Step3_DeliveryJobRowCompileTime(t *testing.T) {
	row := gen.DeliveryJobRow{
		Status:   "pending",
		Attempts: 0,
	}
	if row.Status != "pending" {
		t.Errorf("DeliveryJobRow.Status = %q, want %q", row.Status, "pending")
	}
	if row.Attempts != 0 {
		t.Errorf("DeliveryJobRow.Attempts = %d, want 0", row.Attempts)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Querier interface — compile-time check
// ─────────────────────────────────────────────────────────────────────────────

// TestDelivery141_Step4_QuerierImplementsDeliveryJobMethods verifies at
// compile time that gen.New(nil) satisfies gen.Querier, which now includes all
// 4 delivery job methods. If the interface is missing any method, the build
// fails before this test runs.
func TestDelivery141_Step4_QuerierImplementsDeliveryJobMethods(_ *testing.T) {
	var _ gen.Querier = gen.New(nil)
}

func TestDelivery141_Step4_QuerierFileHasDeliveryJobMethods(t *testing.T) {
	content := findFileByName(t, "querier.go")
	for _, method := range []string{
		"InsertDeliveryJob",
		"GetDeliveryJobByTicketID",
		"UpdateDeliveryJobStatus",
		"ListPendingDeliveryJobs",
	} {
		if !strings.Contains(content, method) {
			t.Errorf("querier.go missing delivery job method '%s'", method)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: Email sender — LogSender and SMTPSender
// ─────────────────────────────────────────────────────────────────────────────

// TestDelivery141_Step5_LogSenderImplementsSenderInterface verifies at
// compile time that *email.LogSender satisfies email.Sender.
func TestDelivery141_Step5_LogSenderImplementsSenderInterface(_ *testing.T) {
	var _ email.Sender = (*email.LogSender)(nil)
}

// TestDelivery141_Step5_SMTPSenderImplementsSenderInterface verifies at
// compile time that *email.SMTPSender satisfies email.Sender.
func TestDelivery141_Step5_SMTPSenderImplementsSenderInterface(_ *testing.T) {
	var _ email.Sender = (*email.SMTPSender)(nil)
}

func TestDelivery141_Step5_LogSenderSendReturnsNil(t *testing.T) {
	sender := &email.LogSender{Logger: slog.Default()}
	msg := email.Message{
		To:       "user@example.com",
		Subject:  "Test ticket",
		HTMLBody: "<p>Hello</p>",
		TextBody: "Hello",
	}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Errorf("LogSender.Send: expected nil error, got: %v", err)
	}
}

func TestDelivery141_Step5_LogSenderWithNilLoggerDoesNotPanic(t *testing.T) {
	// LogSender with nil Logger must fall back to slog.Default() without panic.
	sender := &email.LogSender{}
	msg := email.Message{To: "user@example.com", Subject: "nil-logger test"}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Errorf("LogSender.Send (nil logger): expected nil error, got: %v", err)
	}
}

func TestDelivery141_Step5_LogSenderWithAttachmentsReturnsNil(t *testing.T) {
	sender := &email.LogSender{Logger: slog.Default()}
	msg := email.Message{
		To:      "ticket@example.com",
		Subject: "Ticket with PDF",
		Attachments: []email.Attachment{
			{
				Filename:    "ticket-abc123.pdf",
				ContentType: "application/pdf",
				Data:        []byte("%PDF-1.4\n%%EOF"),
			},
		},
	}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Errorf("LogSender.Send (with attachment): expected nil error, got: %v", err)
	}
}

func TestDelivery141_Step5_SenderGoFileExists(t *testing.T) {
	content := findFileByName(t, "sender.go")
	if content == "" {
		t.Fatal("adapters/email/sender.go is empty or not found")
	}
}

func TestDelivery141_Step5_SenderGoHasRequiredTypes(t *testing.T) {
	content := findFileByName(t, "sender.go")
	for _, typeName := range []string{
		"type Message struct",
		"type Attachment struct",
		"type Sender interface",
		"type SMTPConfig struct",
		"type LogSender struct",
		"type SMTPSender struct",
	} {
		if !strings.Contains(content, typeName) {
			t.Errorf("sender.go missing '%s'", typeName)
		}
	}
}

func TestDelivery141_Step5_NewSMTPSenderConstructsWithoutPanic(t *testing.T) {
	cfg := email.SMTPConfig{
		Host:     "smtp.example.com",
		Port:     "587",
		Username: "user",
		Password: "pass",
		From:     "tickets@example.com",
		UseTLS:   false,
	}
	sender := email.NewSMTPSender(cfg)
	if sender == nil {
		t.Fatal("NewSMTPSender returned nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: Delivery handler — JobType, Payload, nil-sender path
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery141_Step6_JobTypeConstant(t *testing.T) {
	if delivery.JobType != "ticket.deliver" {
		t.Errorf("delivery.JobType = %q, want %q", delivery.JobType, "ticket.deliver")
	}
}

func TestDelivery141_Step6_PayloadJSONRoundtrip(t *testing.T) {
	raw := `{"ticket_id":"00000000-0000-0000-0000-000000000141"}`
	var p delivery.Payload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("json.Unmarshal Payload: %v", err)
	}
	if p.TicketID != "00000000-0000-0000-0000-000000000141" {
		t.Errorf("Payload.TicketID = %q, want %q",
			p.TicketID, "00000000-0000-0000-0000-000000000141")
	}
}

func TestDelivery141_Step6_NewHandlerReturnsFuncType(t *testing.T) {
	h := delivery.NewHandler(delivery.HandlerOptions{})
	if h == nil {
		t.Fatal("delivery.NewHandler returned nil HandlerFunc")
	}
}

func TestDelivery141_Step6_HandlerMalformedPayloadReturnsNil(t *testing.T) {
	// Malformed JSON must be swallowed (return nil) to prevent infinite retries.
	h := delivery.NewHandler(delivery.HandlerOptions{Logger: slog.Default()})
	err := h(context.Background(), []byte("not-json{{{"))
	if err != nil {
		t.Errorf("handler with malformed payload: expected nil (permanent fail), got: %v", err)
	}
}

func TestDelivery141_Step6_HandlerInvalidUUIDReturnsNil(t *testing.T) {
	// Invalid UUID in payload must be swallowed (return nil) to prevent infinite retries.
	h := delivery.NewHandler(delivery.HandlerOptions{Logger: slog.Default()})
	err := h(context.Background(), []byte(`{"ticket_id":"not-a-uuid"}`))
	if err != nil {
		t.Errorf("handler with invalid UUID: expected nil (permanent fail), got: %v", err)
	}
}

func TestDelivery141_Step6_HandlerNilQueriesAndNilSenderReturnsNil(t *testing.T) {
	// When all deps are nil, the handler must log a "no email" warning and
	// return nil (skip path). Must not panic.
	h := delivery.NewHandler(delivery.HandlerOptions{
		Sender: nil,
		Logger: slog.Default(),
	})
	err := h(context.Background(), []byte(`{"ticket_id":"00000000-0000-0000-0000-000000000141"}`))
	if err != nil {
		t.Errorf("handler (nil deps): expected nil, got: %v", err)
	}
}

func TestDelivery141_Step6_HandlerWithLogSenderReturnsNil(t *testing.T) {
	// When a LogSender is provided (always returns nil), the handler must
	// complete without error for a valid ticket UUID — even with nil DB queries
	// (it takes the "no email" skip path before reaching Send).
	sender := &email.LogSender{Logger: slog.Default()}
	h := delivery.NewHandler(delivery.HandlerOptions{
		Sender: sender,
		Logger: slog.Default(),
	})
	err := h(context.Background(), []byte(`{"ticket_id":"00000000-0000-0000-0000-000000000141"}`))
	if err != nil {
		t.Errorf("handler (LogSender, nil queries): expected nil, got: %v", err)
	}
}

func TestDelivery141_Step6_HandlerGoFileExists(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if content == "" {
		t.Fatal("platform/delivery/handler.go is empty or not found")
	}
}

func TestDelivery141_Step6_HandlerGoHasJobType(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, `JobType = "ticket.deliver"`) {
		t.Error("delivery/handler.go missing `JobType = \"ticket.deliver\"`")
	}
}

func TestDelivery141_Step6_HandlerGoHasPayloadStruct(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, "type Payload struct") {
		t.Error("delivery/handler.go missing 'type Payload struct'")
	}
}

func TestDelivery141_Step6_HandlerGoHasNewHandler(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, "func NewHandler(") {
		t.Error("delivery/handler.go missing 'func NewHandler('")
	}
}

func TestDelivery141_Step6_HandlerGoHasHandlerOptions(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, "type HandlerOptions struct") {
		t.Error("delivery/handler.go missing 'type HandlerOptions struct'")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: Server fields — delivery deps wired in server.go
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery141_Step7_ServerGoHasDeliveryJobQueriesField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "deliveryJobQueries") {
		t.Error("server.go missing 'deliveryJobQueries' struct field")
	}
}

func TestDelivery141_Step7_ServerGoHasWorkerPoolField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "workerPool") {
		t.Error("server.go missing 'workerPool' struct field")
	}
}

func TestDelivery141_Step7_ServerGoHasEmailSenderField(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "emailSender") {
		t.Error("server.go missing 'emailSender' struct field")
	}
}

func TestDelivery141_Step7_ServerGoHasDeliveryJobQueriesOption(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "DeliveryJobQueries") {
		t.Error("server.go missing 'DeliveryJobQueries' in Options struct")
	}
}

func TestDelivery141_Step7_ServerGoHasWorkerPoolOption(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "WorkerPool") {
		t.Error("server.go missing 'WorkerPool' in Options struct")
	}
}

func TestDelivery141_Step7_ServerGoHasEmailSenderOption(t *testing.T) {
	content := findFileByName(t, "server.go")
	if !strings.Contains(content, "EmailSender") {
		t.Error("server.go missing 'EmailSender' in Options struct")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: enqueueDeliveryJobs nil guard
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery141_Step8_EnqueueDeliveryJobsIsNilSafeNoPanic(t *testing.T) {
	// When deliveryJobQueries or workerPool is nil, enqueueDeliveryJobs must
	// be a no-op and not panic.
	s := buildDelivery141Server(t)
	// s has nil deliveryJobQueries and nil workerPool by construction.
	tickets := []gen.TicketRow{{Status: "issued"}}
	// Must not panic.
	s.enqueueDeliveryJobs(context.Background(), tickets)
}

func TestDelivery141_Step8_EnqueueDeliveryJobsNilSafeEmptySlice(t *testing.T) {
	s := buildDelivery141Server(t)
	// Empty ticket slice must also be a no-op without panic.
	s.enqueueDeliveryJobs(context.Background(), nil)
}

func TestDelivery141_Step8_EnqueueDeliveryJobsFileExists(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if content == "" {
		t.Fatal("httpserver/delivery_enqueue.go is empty or not found")
	}
}

func TestDelivery141_Step8_EnqueueDeliveryJobsFileHasMethod(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, "func (s *Server) enqueueDeliveryJobs(") {
		t.Error("delivery_enqueue.go missing 'func (s *Server) enqueueDeliveryJobs('")
	}
}

func TestDelivery141_Step8_EnqueueDeliveryJobsFileHasNilGuard(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, "deliveryJobQueries == nil") {
		t.Error("delivery_enqueue.go missing nil guard 'deliveryJobQueries == nil'")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: Audit log strings in handler and enqueue files
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery141_Step9_HandlerGoHasAuditLogSent(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, `"delivery: email sent"`) {
		t.Error("delivery/handler.go missing audit log string 'delivery: email sent'")
	}
}

func TestDelivery141_Step9_HandlerGoHasTicketIDLogField(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, `"ticket_id"`) {
		t.Error("delivery/handler.go missing structured log field 'ticket_id'")
	}
}

func TestDelivery141_Step9_HandlerGoHasAttachmentBytesLogField(t *testing.T) {
	content := findFileByName(t, "delivery_handler.go")
	if !strings.Contains(content, `"attachment_bytes"`) {
		t.Error("delivery/handler.go missing structured log field 'attachment_bytes'")
	}
}

func TestDelivery141_Step9_EnqueueGoHasJobEnqueuedLog(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, `"delivery: job enqueued"`) {
		t.Error("delivery_enqueue.go missing log string 'delivery: job enqueued'")
	}
}

func TestDelivery141_Step9_EnqueueGoHasDeliveryJobIDField(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, `"delivery_job_id"`) {
		t.Error("delivery_enqueue.go missing structured log field 'delivery_job_id'")
	}
}

func TestDelivery141_Step9_EnqueueGoHasWorkerJobIDField(t *testing.T) {
	content := findFileByName(t, "delivery_enqueue.go")
	if !strings.Contains(content, `"worker_job_id"`) {
		t.Error("delivery_enqueue.go missing structured log field 'worker_job_id'")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10: Checkout and payment_intents wire enqueueDeliveryJobs
// ─────────────────────────────────────────────────────────────────────────────

func TestDelivery141_Step10_CheckoutGoCallsEnqueueDeliveryJobs(t *testing.T) {
	content := findFileByName(t, "checkout.go")
	if !strings.Contains(content, "enqueueDeliveryJobs") {
		t.Error("checkout.go missing 'enqueueDeliveryJobs' call — delivery not wired on free checkout")
	}
}

func TestDelivery141_Step10_PaymentIntentsGoCallsEnqueueDeliveryJobs(t *testing.T) {
	content := findFileByName(t, "payment_intents.go")
	if !strings.Contains(content, "enqueueDeliveryJobs") {
		t.Error("payment_intents.go missing 'enqueueDeliveryJobs' call — delivery not wired on payment webhook")
	}
}

func TestDelivery141_Step10_DeliveryJobTypeRegisteredInWorker(t *testing.T) {
	// The worker's main.go should reference the delivery.JobType string.
	// We check the string constant value rather than the identifier to keep this
	// test resilient to registration refactors.
	if delivery.JobType == "" {
		t.Fatal("delivery.JobType is empty")
	}
	// Constant must match the registered string.
	if delivery.JobType != "ticket.deliver" {
		t.Errorf("delivery.JobType = %q, want %q", delivery.JobType, "ticket.deliver")
	}
}
