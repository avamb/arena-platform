package hiam

// Unit tests for the onboarding emails of POST /v1/admin/users and the
// new-email invitation branch of POST /v1/admin/organizations/{org_id}/members.
//
// Both handlers must (1) store only SHA-256(token) in password_reset_tokens,
// (2) enqueue an auth.password_reset_email worker job inside the same
// transaction — the delivery path of the self-service password reset — with
// the right purpose and recipient, and (3) never write the token or a link to
// the logs.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

// ─── fake transaction ─────────────────────────────────────────────────────────

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("fakeRow: scan %d dest, have %d values", len(dest), len(r.values))
	}
	for i, d := range dest {
		reflect.ValueOf(d).Elem().Set(reflect.ValueOf(r.values[i]))
	}
	return nil
}

type fakeCall struct {
	sql  string
	args []any
}

// onboardingFakeTx implements the pgx.Tx surface the two handlers use. Any
// other method panics through the nil embedded interface, which fails the test
// loudly rather than silently passing.
type onboardingFakeTx struct {
	pgx.Tx
	orgName string

	mu         sync.Mutex
	execs      []fakeCall
	queries    []fakeCall
	committed  bool
	rolledBack bool
}

func (tx *onboardingFakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	tx.execs = append(tx.execs, fakeCall{sql: sql, args: args})
	return pgconn.CommandTag{}, nil
}

func (tx *onboardingFakeTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	tx.queries = append(tx.queries, fakeCall{sql: sql, args: args})
	now := time.Now().UTC()
	switch {
	case strings.Contains(sql, "INSERT INTO users"):
		return fakeRow{values: []any{uuid.New(), int64(42), args[0].(string), args[2].(string), now, (*time.Time)(nil)}}
	case strings.Contains(sql, "INSERT INTO user_roles"):
		return fakeRow{values: []any{uuid.New()}}
	case strings.Contains(sql, "INSERT INTO memberships"):
		return fakeRow{values: []any{uuid.New(), args[0].(uuid.UUID), args[1].(uuid.UUID), args[2].(string), "active", now}}
	case strings.Contains(sql, "FROM users"):
		return fakeRow{err: pgx.ErrNoRows}
	case strings.Contains(sql, "FROM organizations"):
		return fakeRow{values: []any{tx.orgName}}
	case strings.Contains(sql, "INSERT INTO worker_jobs"):
		return fakeRow{values: []any{"job-" + uuid.NewString()}}
	}
	return fakeRow{err: fmt.Errorf("onboardingFakeTx: unexpected QueryRow %q", sql)}
}

func (tx *onboardingFakeTx) Commit(context.Context) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	tx.committed = true
	return nil
}

func (tx *onboardingFakeTx) Rollback(context.Context) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if !tx.committed {
		tx.rolledBack = true
	}
	return nil
}

type fakeTxStarter struct{ tx *onboardingFakeTx }

func (s fakeTxStarter) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) { return s.tx, nil }

// ─── helpers ──────────────────────────────────────────────────────────────────

// newOnboardingHandler returns a Handler over the fake tx whose own logger AND
// the process default logger both write into the returned buffer, so a stray
// slog.Info anywhere in the handler is caught too. Tests using it must not run
// in parallel (slog.SetDefault is global).
func newOnboardingHandler(t *testing.T, tx *onboardingFakeTx) (*Handler, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })
	h := New(nil, gen.New(nil), nil, fakeTxStarter{tx: tx}, nil, logger, nil, nil)
	return h, &buf
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// onlyWorkerJob returns the single worker_jobs insert and its decoded payload.
func onlyWorkerJob(t *testing.T, tx *onboardingFakeTx) (string, authemail.PasswordResetEmailPayload) {
	t.Helper()
	var jobs []fakeCall
	for _, q := range tx.queries {
		if strings.Contains(q.sql, "INSERT INTO worker_jobs") {
			jobs = append(jobs, q)
		}
	}
	if len(jobs) != 1 {
		t.Fatalf("worker_jobs inserts = %d; want exactly 1", len(jobs))
	}
	jobType, _ := jobs[0].args[0].(string)
	raw, ok := jobs[0].args[1].([]byte)
	if !ok {
		t.Fatalf("worker_jobs payload arg is %T; want []byte", jobs[0].args[1])
	}
	var p authemail.PasswordResetEmailPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode job payload: %v", err)
	}
	return jobType, p
}

// storedResetToken returns the token value written to password_reset_tokens.
func storedResetToken(t *testing.T, tx *onboardingFakeTx) string {
	t.Helper()
	for _, e := range tx.execs {
		if strings.Contains(e.sql, "INSERT INTO password_reset_tokens") {
			s, _ := e.args[0].(string)
			return s
		}
	}
	t.Fatal("no password_reset_tokens insert recorded")
	return ""
}

func assertNoSecretInLogs(t *testing.T, logs, token string) {
	t.Helper()
	for _, needle := range []string{token, users.TokenHash(token), "token=", "reset_url", "invite_url", "/accept-invite", "/v1/auth/password-reset"} {
		if strings.Contains(logs, needle) {
			t.Errorf("logs contain %q; logs:\n%s", needle, logs)
		}
	}
}

// ─── POST /v1/admin/users ─────────────────────────────────────────────────────

func TestAdminCreateUser_EnqueuesAccountSetupEmailInTx_NoTokenInLogs(t *testing.T) {
	tx := &onboardingFakeTx{}
	h, logs := newOnboardingHandler(t, tx)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/users",
		strings.NewReader(`{"email":"New.Operator@Example.test","role":"platform_operator"}`))
	req.Host = "api.arenasoldout.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "onboarding")
	rec := httptest.NewRecorder()
	h.HandleAdminCreateUser(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	if !tx.committed {
		t.Fatal("transaction was not committed")
	}

	jobType, p := onlyWorkerJob(t, tx)
	if jobType != authemail.JobTypePasswordResetEmail {
		t.Errorf("job_type = %q; want %q (the self-service reset delivery path)", jobType, authemail.JobTypePasswordResetEmail)
	}
	if p.Purpose != authemail.PurposeAccountSetup {
		t.Errorf("purpose = %q; want %q", p.Purpose, authemail.PurposeAccountSetup)
	}
	if p.Email != "new.operator@example.test" {
		t.Errorf("recipient = %q; want the normalized address", p.Email)
	}
	if p.Token == "" || p.UserID == "" || p.ExpiresAt.IsZero() {
		t.Errorf("payload incomplete: user_id=%q token_set=%v expires_at=%v", p.UserID, p.Token != "", p.ExpiresAt)
	}

	// The DB must hold SHA-256(token): /v1/auth/password-reset/confirm hashes
	// the raw token from the link before its lookup.
	if got, want := storedResetToken(t, tx), users.TokenHash(p.Token); got != want {
		t.Errorf("stored token = %q; want SHA-256 of the emailed token %q", got, want)
	}

	var resp adminCreateUserResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Onboarding.PasswordResetIssued || resp.Onboarding.Delivery != "email" {
		t.Errorf("onboarding = %+v; want issued via email", resp.Onboarding)
	}
	if strings.Contains(rec.Body.String(), p.Token) {
		t.Error("response body leaks the setup token")
	}

	out := logs.String()
	assertNoSecretInLogs(t, out, p.Token)
	if !strings.Contains(out, "password setup email job enqueued") || !strings.Contains(out, p.UserID) {
		t.Errorf("expected an identifiers-only enqueue log line; logs:\n%s", out)
	}
	if strings.Contains(out, "api.arenasoldout.com") {
		t.Errorf("logs contain a request-derived URL; logs:\n%s", out)
	}
}

// ─── POST /v1/admin/organizations/{org_id}/members ────────────────────────────

func TestAdminAddMember_NewEmail_EnqueuesInvitationEmailInTx_NoTokenInLogs(t *testing.T) {
	tx := &onboardingFakeTx{orgName: "Lampyris s.r.o."}
	h, logs := newOnboardingHandler(t, tx)

	orgID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/organizations/"+orgID.String()+"/members",
		strings.NewReader(`{"email":"Invitee@Example.test","role":"organizer"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "invite")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.HandleAdminAddMember(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	if !tx.committed {
		t.Fatal("transaction was not committed")
	}

	jobType, p := onlyWorkerJob(t, tx)
	if jobType != authemail.JobTypePasswordResetEmail {
		t.Errorf("job_type = %q; want %q", jobType, authemail.JobTypePasswordResetEmail)
	}
	if p.Purpose != authemail.PurposeOrgInvitation {
		t.Errorf("purpose = %q; want %q", p.Purpose, authemail.PurposeOrgInvitation)
	}
	if p.Email != "invitee@example.test" {
		t.Errorf("recipient = %q; want the normalized address", p.Email)
	}
	if p.OrgName != "Lampyris s.r.o." {
		t.Errorf("org_name = %q; want the inviting organization", p.OrgName)
	}
	if got, want := storedResetToken(t, tx), users.TokenHash(p.Token); got != want {
		t.Errorf("stored token = %q; want SHA-256 of the emailed token %q", got, want)
	}

	var resp struct {
		Invitation struct {
			Issued   bool   `json:"issued"`
			Delivery string `json:"delivery"`
		} `json:"invitation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Invitation.Issued || resp.Invitation.Delivery != "email" {
		t.Errorf("invitation = %+v; want issued via email", resp.Invitation)
	}

	out := logs.String()
	assertNoSecretInLogs(t, out, p.Token)
	if !strings.Contains(out, "invitation email job enqueued") {
		t.Errorf("expected an identifiers-only enqueue log line; logs:\n%s", out)
	}
}

func TestAdminAddMember_ExistingUserID_EnqueuesNoEmail(t *testing.T) {
	tx := &onboardingFakeTx{}
	h, _ := newOnboardingHandler(t, tx)

	orgID := uuid.New()
	body := fmt.Sprintf(`{"user_id":%q,"role":"organizer"}`, uuid.NewString())
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/organizations/"+orgID.String()+"/members", strings.NewReader(body))
	req.Header.Set("X-Admin-Reason", "grant")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.HandleAdminAddMember(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	for _, q := range tx.queries {
		if strings.Contains(q.sql, "INSERT INTO worker_jobs") {
			t.Fatal("an existing-user membership grant must not enqueue an invitation email")
		}
	}
}
