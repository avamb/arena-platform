package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// Sources of an application.
const (
	SourceSite     = "site"
	SourceTelegram = "telegram"
	SourceOperator = "operator"
)

const (
	// resumeLinkTTL is how long the e-mailed confirm/continue link works.
	resumeLinkTTL = 14 * 24 * time.Hour
	// emailJobAttempts is the retry budget of an onboarding e-mail.
	emailJobAttempts = 5

	maxStartsPerEmailPerHour = 5
	maxStartsPerIPPerHour    = 10
	maxStartsPerTGPerDay     = 3
	maxResendsPerAppPerHour  = 3
)

// Errors the transport layer maps to HTTP statuses.
var (
	ErrLocked            = errors.New("onboarding: the application is locked")
	ErrEmailNotConfirmed = errors.New("onboarding: e-mail is not confirmed")
	ErrRateLimited       = errors.New("onboarding: rate limited")
	ErrWrongState        = errors.New("onboarding: the application is not in a state that allows this")
)

// FieldsError carries per-field problems. Code is "invalid_field" for a bad
// value and "incomplete" for required fields that are still empty.
type FieldsError struct {
	Code   string
	Fields FieldErrors
}

func (e *FieldsError) Error() string {
	return fmt.Sprintf("onboarding: %s: %d field(s)", e.Code, len(e.Fields))
}

// Service owns the application lifecycle.
type Service struct {
	pool     *pgxpool.Pool
	notifier opsalert.Notifier
	audit    audit.Writer
	logger   *slog.Logger
	adminURL string
	now      func() time.Time
}

// Options configures a Service.
type Options struct {
	Pool     *pgxpool.Pool
	Notifier opsalert.Notifier
	Audit    audit.Writer
	Logger   *slog.Logger
	// AdminURL is the admin-web origin, used for the "open in the console"
	// link in operator messages.
	AdminURL string
	Now      func() time.Time
}

// New builds a Service.
func New(o Options) *Service {
	s := &Service{pool: o.Pool, notifier: o.Notifier, audit: o.Audit, logger: o.Logger,
		adminURL: strings.TrimRight(o.AdminURL, "/"), now: o.Now}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.notifier == nil {
		s.notifier = opsalert.New("", "", "", s.logger)
	}
	return s
}

// Settings returns the current settings.
func (s *Service) Settings(ctx context.Context) (Settings, error) { return loadSettings(ctx, s.pool) }

// Schema returns the form in a language with the current settings applied.
func (s *Service) Schema(ctx context.Context, locale string) (FormSchema, error) {
	set, err := loadSettings(ctx, s.pool)
	if err != nil {
		return FormSchema{}, err
	}
	return BuildSchema(locale, set.Countries, set.TermsVersion, set.PrivacyVersion), nil
}

// StartInput is the first step of the form.
type StartInput struct {
	FirstName, LastName, Email, Phone string
	Locale, Source                    string
	UTM                               map[string]string
	// IPHash is a salted hash of the client address, "" when unknown.
	IPHash         string
	TelegramUserID *int64
}

// Started is the result of Start.
type Started struct {
	App         *Application
	AccessToken string
}

func newToken() (raw, hash string, err error) {
	raw, err = users.GenerateVerificationToken()
	if err != nil {
		return "", "", err
	}
	return raw, users.TokenHash(raw), nil
}

// Start always creates a NEW draft (so the answer never reveals whether an
// address already has applications) and e-mails the confirm/continue link.
func (s *Service) Start(ctx context.Context, in StartInput) (*Started, error) {
	errs := FieldErrors{}
	norm := Answers{}
	for key, raw := range map[string]string{"first_name": in.FirstName, "last_name": in.LastName, "email": in.Email, "phone": in.Phone} {
		f, _ := LookupField(key)
		v, reason := NormalizeValue(f, raw)
		switch {
		case reason != "":
			errs[key] = reason
		case v == nil:
			errs[key] = "required"
		default:
			norm[key] = v
		}
	}
	if len(errs) > 0 {
		return nil, &FieldsError{Code: "invalid_field", Fields: errs}
	}
	source := in.Source
	if source != SourceTelegram && source != SourceOperator {
		source = SourceSite
	}
	locale := NormalizeLocale(in.Locale)
	email := norm.String("email")

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	set, err := loadSettings(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := s.checkStartLimits(ctx, tx, set, email, in); err != nil {
		return nil, err
	}

	now := s.now()
	access, accessHash, err := newToken()
	if err != nil {
		return nil, err
	}
	resume, resumeHash, err := newToken()
	if err != nil {
		return nil, err
	}
	pct, step := Progress(norm)
	answers, _ := json.Marshal(norm)
	utm, _ := json.Marshal(cleanUTM(in.UTM))
	name := norm.String("first_name") + " " + norm.String("last_name")

	row := tx.QueryRow(ctx, `
INSERT INTO onboarding_applications
    (source, applicant_email, applicant_name, applicant_phone, locale, current_step, progress_pct,
     answers, access_token_hash, resume_token_hash, resume_token_expires_at, telegram_user_id,
     utm, ip_hash, last_activity_at, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
RETURNING `+appColumns,
		source, email, name, norm.String("phone"), locale, step, pct, answers, accessHash, resumeHash,
		now.Add(resumeLinkTTL), in.TelegramUserID, utm, nullIfEmpty(in.IPHash), now,
		now.AddDate(0, 0, set.DraftTTLDays))
	app, err := scanApplication(row)
	if err != nil {
		return nil, fmt.Errorf("onboarding: insert application: %w", err)
	}
	if err := addEvent(ctx, tx, app.ID, "created", ActorApplicant, map[string]any{"source": source, "locale": locale}, nil); err != nil {
		return nil, err
	}
	if err := s.queueEmail(ctx, tx, authemail.OnboardingKindConfirm, app, resume, nil, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Started{App: app, AccessToken: access}, nil
}

func (s *Service) checkStartLimits(ctx context.Context, q querier, set Settings, email string, in StartInput) error {
	var perEmail, perIP, perTG, today int
	err := q.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE applicant_email = $1 AND created_at > now() - interval '1 hour'),
  count(*) FILTER (WHERE $2 <> '' AND ip_hash = $2 AND created_at > now() - interval '1 hour'),
  count(*) FILTER (WHERE $3::bigint IS NOT NULL AND telegram_user_id = $3 AND created_at > now() - interval '1 day'),
  count(*) FILTER (WHERE created_at > now() - interval '1 day')
FROM onboarding_applications WHERE created_at > now() - interval '1 day'`,
		email, in.IPHash, in.TelegramUserID).Scan(&perEmail, &perIP, &perTG, &today)
	if err != nil {
		return fmt.Errorf("onboarding: start limits: %w", err)
	}
	if perEmail >= maxStartsPerEmailPerHour || perIP >= maxStartsPerIPPerHour ||
		perTG >= maxStartsPerTGPerDay || today >= set.MaxNewPerDay {
		return ErrRateLimited
	}
	return nil
}

// Get returns the application when the access token matches.
func (s *Service) Get(ctx context.Context, id uuid.UUID, token string) (*Application, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	app, err := scanApplication(s.pool.QueryRow(ctx,
		`SELECT `+appColumns+` FROM onboarding_applications WHERE id = $1 AND access_token_hash = $2 AND purged_at IS NULL`,
		id, users.TokenHash(token)))
	return orNotFound(app, err)
}

func orNotFound(app *Application, err error) (*Application, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return app, err
}

// SaveAnswers merges a partial update. Nothing is saved unless every key is valid.
// An expired draft that has not been purged comes back to life.
func (s *Service) SaveAnswers(ctx context.Context, id uuid.UUID, token string, patch map[string]any) (*Application, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	app, err := scanApplication(tx.QueryRow(ctx,
		`SELECT `+appColumns+` FROM onboarding_applications WHERE id = $1 AND access_token_hash = $2 AND purged_at IS NULL FOR UPDATE`,
		id, users.TokenHash(token)))
	if app, err = orNotFound(app, err); err != nil {
		return nil, err
	}
	switch app.Status {
	case StatusDraft, StatusExpired:
	case StatusInfoRequested:
	default:
		return nil, ErrLocked
	}
	set, err := loadSettings(ctx, tx)
	if err != nil {
		return nil, err
	}
	allowed := func(key string) bool {
		if app.Status != StatusInfoRequested {
			return true
		}
		return contains(app.RequestedFields, key)
	}
	clean, ferrs := ValidatePatch(patch, allowed)
	if ferrs == nil {
		if c, ok := clean["country"].(string); ok && !set.allowedCountry(c) {
			ferrs = FieldErrors{"country": "not_allowed"}
		}
	}
	if ferrs != nil {
		return nil, &FieldsError{Code: "invalid_field", Fields: ferrs}
	}
	for k, v := range clean {
		if v == nil {
			delete(app.Answers, k)
		} else {
			app.Answers[k] = v
		}
	}
	now := s.now()
	if err := s.persist(ctx, tx, app, now, set); err != nil {
		return nil, err
	}
	if app.Status == StatusExpired {
		if _, err := tx.Exec(ctx, `UPDATE onboarding_applications SET status = 'draft' WHERE id = $1`, app.ID); err != nil {
			return nil, err
		}
		app.Status = StatusDraft
		if err := addEvent(ctx, tx, app.ID, "revived", ActorApplicant, nil, nil); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return app, nil
}

// persist writes the answers, the mirror columns and the progress.
func (s *Service) persist(ctx context.Context, q querier, app *Application, now time.Time, set Settings) error {
	app.ProgressPct, app.CurrentStep = Progress(app.Answers)
	raw, err := json.Marshal(app.Answers)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(app.Answers.String("first_name") + " " + app.Answers.String("last_name"))
	app.ApplicantName = nullIfEmptyPtr(name)
	app.Phone = nullIfEmptyPtr(app.Answers.String("phone"))
	app.Country = nullIfEmptyPtr(app.Answers.String("country"))
	app.OrgName = nullIfEmptyPtr(app.Answers.String("org_name"))
	app.LegalName = nullIfEmptyPtr(app.Answers.String("legal_name"))
	app.LastActivityAt = now
	if app.Status == StatusDraft || app.Status == StatusExpired {
		app.ExpiresAt = now.AddDate(0, 0, set.DraftTTLDays)
	}
	_, err = q.Exec(ctx, `
UPDATE onboarding_applications SET answers = $2, applicant_name = $3, applicant_phone = $4, country = $5,
  org_name = $6, legal_name = $7, current_step = $8, progress_pct = $9, last_activity_at = $10,
  expires_at = $11, updated_at = $10
WHERE id = $1`, app.ID, raw, app.ApplicantName, app.Phone, app.Country, app.OrgName, app.LegalName,
		app.CurrentStep, app.ProgressPct, now, app.ExpiresAt)
	return err
}

// Confirm consumes the e-mailed link: it confirms the address, issues a fresh
// access token for the browser that opened the link and revives an expired draft.
func (s *Service) Confirm(ctx context.Context, resumeToken string) (*Started, error) {
	if resumeToken == "" {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	app, err := scanApplication(tx.QueryRow(ctx,
		`SELECT `+appColumns+` FROM onboarding_applications
 WHERE resume_token_hash = $1 AND resume_token_expires_at > now() AND purged_at IS NULL FOR UPDATE`,
		users.TokenHash(resumeToken)))
	if app, err = orNotFound(app, err); err != nil {
		return nil, err
	}
	set, err := loadSettings(ctx, tx)
	if err != nil {
		return nil, err
	}
	access, accessHash, err := newToken()
	if err != nil {
		return nil, err
	}
	now := s.now()
	firstConfirm := app.EmailConfirmedAt == nil
	if firstConfirm {
		app.EmailConfirmedAt = &now
	}
	revived := app.Status == StatusExpired
	if revived {
		app.Status = StatusDraft
	}
	if app.Status == StatusDraft {
		app.ExpiresAt = now.AddDate(0, 0, set.DraftTTLDays)
	}
	app.LastActivityAt = now
	if _, err := tx.Exec(ctx, `
UPDATE onboarding_applications SET access_token_hash = $2, email_confirmed_at = $3, status = $4,
  last_activity_at = $5, expires_at = $6, updated_at = $5 WHERE id = $1`,
		app.ID, accessHash, app.EmailConfirmedAt, app.Status, now, app.ExpiresAt); err != nil {
		return nil, err
	}
	kind := "resumed"
	if firstConfirm {
		kind = "email_confirmed"
	}
	if err := addEvent(ctx, tx, app.ID, kind, ActorApplicant, map[string]any{"revived": revived}, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Started{App: app, AccessToken: access}, nil
}

// Resume e-mails a fresh continue link for the newest open application of an
// address. It reports nothing about whether one exists.
func (s *Service) Resume(ctx context.Context, email string) error {
	f, _ := LookupField("email")
	v, reason := NormalizeValue(f, email)
	addr, _ := v.(string)
	if reason != "" || addr == "" {
		return nil
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
SELECT id FROM onboarding_applications
WHERE applicant_email = $1 AND purged_at IS NULL AND status IN ('draft', 'info_requested', 'expired')
ORDER BY last_activity_at DESC LIMIT 1`, addr).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	err = s.IssueLink(ctx, id, ActorApplicant)
	if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// IssueLink rotates the resume token of an application and e-mails it. It is
// the applicant's "send me the link again" and the operator's "resend".
func (s *Service) IssueLink(ctx context.Context, id uuid.UUID, actor Actor) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	app, err := scanApplication(tx.QueryRow(ctx,
		`SELECT `+appColumns+` FROM onboarding_applications WHERE id = $1 AND purged_at IS NULL FOR UPDATE`, id))
	if app, err = orNotFound(app, err); err != nil {
		return err
	}
	if app.Status == StatusApproved || app.Status == StatusRejected {
		return ErrWrongState
	}
	var recent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM onboarding_application_events
 WHERE application_id = $1 AND kind = 'link_sent' AND created_at > now() - interval '1 hour'`, id).Scan(&recent); err != nil {
		return err
	}
	if recent >= maxResendsPerAppPerHour {
		return ErrRateLimited
	}
	resume, hash, err := newToken()
	if err != nil {
		return err
	}
	now := s.now()
	if _, err := tx.Exec(ctx, `UPDATE onboarding_applications SET resume_token_hash = $2, resume_token_expires_at = $3 WHERE id = $1`,
		id, hash, now.Add(resumeLinkTTL)); err != nil {
		return err
	}
	kind := authemail.OnboardingKindResume
	if app.EmailConfirmedAt == nil {
		kind = authemail.OnboardingKindConfirm
	}
	if err := s.queueEmail(ctx, tx, kind, app, resume, nil, ""); err != nil {
		return err
	}
	if err := addEvent(ctx, tx, id, "link_sent", actor, nil, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Submit validates completeness and hands the application to the operator.
func (s *Service) Submit(ctx context.Context, id uuid.UUID, token string) (*Application, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	app, err := scanApplication(tx.QueryRow(ctx,
		`SELECT `+appColumns+` FROM onboarding_applications WHERE id = $1 AND access_token_hash = $2 AND purged_at IS NULL FOR UPDATE`,
		id, users.TokenHash(token)))
	if app, err = orNotFound(app, err); err != nil {
		return nil, err
	}
	if app.Status != StatusDraft && app.Status != StatusInfoRequested && app.Status != StatusExpired {
		return nil, ErrLocked
	}
	if app.EmailConfirmedAt == nil {
		return nil, ErrEmailNotConfirmed
	}
	set, err := loadSettings(ctx, tx)
	if err != nil {
		return nil, err
	}
	ApplyDefaults(app.Answers)
	if missing := Missing(app.Answers); len(missing) > 0 {
		errs := FieldErrors{}
		for _, k := range missing {
			errs[k] = "required"
		}
		return nil, &FieldsError{Code: "incomplete", Fields: errs}
	}
	if c := app.Answers.String("country"); !set.allowedCountry(c) {
		return nil, &FieldsError{Code: "invalid_field", Fields: FieldErrors{"country": "not_allowed"}}
	}
	resubmitted := app.Status == StatusInfoRequested
	now := s.now()
	if err := s.persist(ctx, tx, app, now, set); err != nil {
		return nil, err
	}
	app.Status = StatusPendingApproval
	app.SubmittedAt = &now
	app.TermsVersion, app.PrivacyVersion = &set.TermsVersion, &set.PrivacyVersion
	app.ConsentedAt = &now
	app.RequestedFields, app.InfoRequestMessage = []string{}, nil
	if _, err := tx.Exec(ctx, `
UPDATE onboarding_applications SET status = 'pending_approval', submitted_at = $2, terms_version = $3,
  privacy_version = $4, consented_at = $2, requested_fields = '{}', info_request_message = NULL, updated_at = $2
WHERE id = $1`, app.ID, now, set.TermsVersion, set.PrivacyVersion); err != nil {
		return nil, err
	}
	kind := "submitted"
	if resubmitted {
		kind = "resubmitted"
	}
	if err := addEvent(ctx, tx, app.ID, kind, ActorApplicant, nil, app.Answers); err != nil {
		return nil, err
	}
	checks, err := s.runChecks(ctx, tx, app, set)
	if err != nil {
		return nil, err
	}
	if err := s.queueEmail(ctx, tx, authemail.OnboardingKindReceived, app, "", nil, ""); err != nil {
		return nil, err
	}
	would := WouldApprove(checks)
	notifyKind := NotifySubmitted
	if resubmitted {
		notifyKind = NotifyResubmitted
	}
	if err := s.queueNotify(ctx, tx, NotifyPayload{Kind: notifyKind, ApplicationID: app.ID.String(), WouldApprove: would}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	if set.ApprovalMode == ModeAutoWhenComplete && would {
		if _, err := s.Approve(ctx, app.ID, ActorSystem, uuid.Nil); err != nil {
			s.logger.Error("onboarding: automatic approval failed", slog.String("application_id", app.ID.String()), slog.Any("error", err))
		}
	}
	return app, nil
}

// queueEmail enqueues an onboarding.email job on tx.
func (s *Service) queueEmail(ctx context.Context, tx pgx.Tx, kind string, app *Application, resumeToken string, fields []string, message string) error {
	name := ""
	if app.Answers != nil {
		name = app.Answers.String("first_name")
	}
	org := ""
	if app.OrgName != nil {
		org = *app.OrgName
	}
	_, err := worker.EnqueueInTx(ctx, tx, authemail.JobTypeOnboardingEmail, authemail.OnboardingEmailPayload{
		Kind:          kind,
		ApplicationID: app.ID.String(),
		Email:         app.Email,
		FirstName:     name,
		Locale:        app.Locale,
		OrgName:       org,
		Token:         resumeToken,
		Message:       message,
		Fields:        fields,
	}, emailJobAttempts)
	if err != nil {
		return fmt.Errorf("onboarding: queue %s e-mail: %w", kind, err)
	}
	return nil
}

func (s *Service) writeAudit(ctx context.Context, actor Actor, action string, appID uuid.UUID, meta map[string]any) {
	if s.audit == nil {
		return
	}
	actorType, actorID := "system", ""
	if actor.Type == "operator" {
		actorType, actorID = "user", actor.ID
	}
	ev := audit.NewEvent(actorType, actorID, action, "onboarding_application", appID.String())
	ev.OccurredAt = s.now()
	ev.Metadata = meta
	if err := s.audit.Write(ctx, ev); err != nil {
		s.logger.Warn("onboarding: audit write failed", slog.String("action", action), slog.Any("error", err))
	}
}

func cleanUTM(in map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"source", "medium", "campaign", "term", "content", "ref", "referrer_host"} {
		if v := strings.TrimSpace(in[k]); v != "" {
			if len(v) > 120 {
				v = v[:120]
			}
			out[k] = v
		}
	}
	return out
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
