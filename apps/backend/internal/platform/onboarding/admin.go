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

	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/provisioning"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// ListFilter selects applications for the operator's queue.
type ListFilter struct {
	Status string
	Query  string
	Limit  int
	Offset int
}

// ListItem is one row of the queue.
type ListItem struct {
	Application
	WouldApprove bool `json:"would_approve"`
}

// ListResult is a page plus the per-status counters of the tabs.
type ListResult struct {
	Items  []ListItem     `json:"items"`
	Total  int            `json:"total"`
	Counts map[string]int `json:"counts"`
}

// visibleSQL hides drafts whose e-mail was never confirmed: until then the
// operator neither sees nor is told about them.
const visibleSQL = `(status NOT IN ('draft', 'expired') OR email_confirmed_at IS NOT NULL)`

// List returns a page of applications and the tab counters.
func (s *Service) List(ctx context.Context, f ListFilter) (*ListResult, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	res := &ListResult{Items: []ListItem{}, Counts: map[string]int{}}
	for _, st := range Statuses {
		res.Counts[st] = 0
	}
	rows, err := s.pool.Query(ctx, `SELECT status, count(*) FROM onboarding_applications WHERE `+visibleSQL+` GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		res.Counts[st] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	q := "%" + strings.ToLower(strings.TrimSpace(f.Query)) + "%"
	where := ` WHERE ` + visibleSQL + ` AND ($1 = '' OR status = $1)
 AND ($2 = '%%' OR lower(coalesce(org_name,'') || ' ' || applicant_email || ' ' || coalesce(applicant_name,'') || ' ' || coalesce(country,'')) LIKE $2)`
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM onboarding_applications`+where, f.Status, q).Scan(&res.Total); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT `+appColumns+` FROM onboarding_applications`+where+
		` ORDER BY last_activity_at DESC LIMIT $3 OFFSET $4`, f.Status, q, f.Limit, f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		a, err := scanApplication(rows)
		if err != nil {
			return nil, err
		}
		res.Items = append(res.Items, ListItem{Application: *a})
		ids = append(ids, a.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		verdict, err := s.verdicts(ctx, ids)
		if err != nil {
			return nil, err
		}
		for i := range res.Items {
			res.Items[i].WouldApprove = verdict[res.Items[i].ID]
		}
	}
	return res, nil
}

// verdicts says, per application, whether every stored check passed.
func (s *Service) verdicts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := s.pool.Query(ctx, `
SELECT application_id, bool_and(result = 'pass'), count(*)
FROM onboarding_application_checks WHERE application_id = ANY($1) GROUP BY application_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		var all bool
		var n int
		if err := rows.Scan(&id, &all, &n); err != nil {
			return nil, err
		}
		out[id] = all && n == len(checkKeys)
	}
	return out, rows.Err()
}

// Detail is the operator's card of one application.
type Detail struct {
	Application  Application `json:"application"`
	Checks       []Check     `json:"checks"`
	WouldApprove bool        `json:"would_approve"`
	Events       []Event     `json:"events"`
	Notes        []Note      `json:"notes"`
}

// Get returns the card. An unconfirmed draft is still readable by id.
func (s *Service) Detail(ctx context.Context, id uuid.UUID) (*Detail, error) {
	app, err := scanApplication(s.pool.QueryRow(ctx, `SELECT `+appColumns+` FROM onboarding_applications WHERE id = $1`, id))
	if app, err = orNotFound(app, err); err != nil {
		return nil, err
	}
	d := &Detail{Application: *app, Events: []Event{}, Notes: []Note{}}
	if d.Checks, err = s.Checks(ctx, id); err != nil {
		return nil, err
	}
	d.WouldApprove = len(d.Checks) == len(checkKeys) && WouldApprove(d.Checks)

	rows, err := s.pool.Query(ctx, `SELECT id, kind, actor_type, actor_id, detail, created_at
FROM onboarding_application_events WHERE application_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e Event
		var raw []byte
		if err := rows.Scan(&e.ID, &e.Kind, &e.ActorType, &e.ActorID, &raw, &e.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(raw, &e.Detail)
		d.Events = append(d.Events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	nrows, err := s.pool.Query(ctx, `SELECT id, author_id, body, created_at FROM onboarding_application_notes
WHERE application_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	defer nrows.Close()
	for nrows.Next() {
		var n Note
		if err := nrows.Scan(&n.ID, &n.AuthorID, &n.Body, &n.CreatedAt); err != nil {
			return nil, err
		}
		d.Notes = append(d.Notes, n)
	}
	return d, nrows.Err()
}

// RecheckAndGet recomputes the checks and returns the card.
func (s *Service) Recheck(ctx context.Context, id uuid.UUID) (*Detail, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	app, err := scanApplication(tx.QueryRow(ctx, `SELECT `+appColumns+` FROM onboarding_applications WHERE id = $1 FOR UPDATE`, id))
	if app, err = orNotFound(app, err); err != nil {
		return nil, err
	}
	set, err := loadSettings(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, err := s.runChecks(ctx, tx, app, set); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Detail(ctx, id)
}

// lockForDecision loads an application FOR UPDATE and requires one of the statuses.
func (s *Service) lockForDecision(ctx context.Context, tx pgx.Tx, id uuid.UUID, statuses ...string) (*Application, error) {
	app, err := scanApplication(tx.QueryRow(ctx, `SELECT `+appColumns+` FROM onboarding_applications WHERE id = $1 FOR UPDATE`, id))
	if app, err = orNotFound(app, err); err != nil {
		return nil, err
	}
	if !contains(statuses, app.Status) || app.PurgedAt != nil {
		return nil, ErrWrongState
	}
	return app, nil
}

// ApproveResult reports what the approval created.
type ApproveResult struct {
	OrgID        uuid.UUID `json:"org_id"`
	OrgSlug      string    `json:"org_slug"`
	ChannelID    uuid.UUID `json:"channel_id"`
	OwnerID      uuid.UUID `json:"owner_id"`
	OwnerCreated bool      `json:"owner_created"`
	Closed       int       `json:"closed_duplicates"`
}

// Approve turns an application into a workspace in one transaction. reviewer
// is the operator's user id, uuid.Nil for the system's automatic approval.
func (s *Service) Approve(ctx context.Context, id uuid.UUID, actor Actor, reviewer uuid.UUID) (*ApproveResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	app, err := s.lockForDecision(ctx, tx, id, StatusPendingApproval)
	if err != nil {
		return nil, err
	}
	a := app.Answers
	now := s.now()
	ws, err := provisioning.CreateWorkspace(ctx, tx, provisioning.WorkspaceInput{
		OrgName:            a.String("org_name"),
		LegalName:          a.String("legal_name"),
		Country:            a.String("country"),
		Locale:             app.Locale,
		TaxID:              a.String("tax_id"),
		TaxIDScheme:        a.String("tax_id_scheme"),
		RegistrationNumber: a.String("registration_number"),
		AddressLine1:       a.String("address_line1"),
		AddressPostalCode:  a.String("address_postal_code"),
		AddressCity:        a.String("address_city"),
		AddressCountry:     firstNonEmpty(a.String("address_country"), a.String("country")),
		Website:            a.String("website"),
		ContactEmail:       app.Email,
		ContactPhone:       a.String("phone"),
		OwnerEmail:         app.Email,
		OwnerFirstName:     a.String("first_name"),
		OwnerLastName:      a.String("last_name"),
		PaymentProvider:    a.String("payment_provider"),
		TelegramUserID:     app.TelegramUserID,
		TelegramUsername:   nullIfEmptyPtr(a.String("telegram_username")),
		Now:                now,
	})
	if err != nil {
		return nil, err
	}
	var reviewerPtr *uuid.UUID
	if reviewer != uuid.Nil {
		reviewerPtr = &reviewer
	}
	if _, err := tx.Exec(ctx, `
UPDATE onboarding_applications SET status = 'approved', org_id = $2, reviewed_by = $3, reviewed_at = $4,
  last_activity_at = $4, updated_at = $4 WHERE id = $1`, app.ID, ws.OrgID, reviewerPtr, now); err != nil {
		return nil, err
	}
	closed, err := tx.Exec(ctx, `
UPDATE onboarding_applications SET status = 'rejected', decision_reason = $3, reviewed_at = $4, updated_at = $4
WHERE applicant_email = $2 AND id <> $1 AND purged_at IS NULL AND status IN ('draft', 'pending_approval', 'info_requested')`,
		app.ID, app.Email, "duplicate of "+app.ID.String(), now)
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"org_id": ws.OrgID.String(), "org_slug": ws.Slug, "automatic": actor.Type == "system"}
	if err := addEvent(ctx, tx, app.ID, "approved", actor, detail, nil); err != nil {
		return nil, err
	}
	if err := s.queueEmail(ctx, tx, authemail.OnboardingKindApproved, app, "", nil, ""); err != nil {
		return nil, err
	}
	if err := s.queueNotify(ctx, tx, NotifyPayload{Kind: NotifyApproved, ApplicationID: app.ID.String(),
		OrgSlug: ws.Slug, Automatic: actor.Type == "system"}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.writeAudit(ctx, actor, "v1.admin.onboarding.approve", app.ID, map[string]any{
		"org_id": ws.OrgID.String(), "org_slug": ws.Slug, "automatic": actor.Type == "system",
	})
	return &ApproveResult{OrgID: ws.OrgID, OrgSlug: ws.Slug, ChannelID: ws.ChannelID, OwnerID: ws.OwnerID,
		OwnerCreated: ws.OwnerCreated, Closed: int(closed.RowsAffected())}, nil
}

// Reject closes an application. reason is the operator's own note; message, when
// given, is added to the e-mail the applicant receives.
func (s *Service) Reject(ctx context.Context, id, reviewer uuid.UUID, reason, message string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return &FieldsError{Code: "invalid_field", Fields: FieldErrors{"reason": "required"}}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	app, err := s.lockForDecision(ctx, tx, id, StatusPendingApproval, StatusInfoRequested, StatusDraft)
	if err != nil {
		return err
	}
	now := s.now()
	if _, err := tx.Exec(ctx, `
UPDATE onboarding_applications SET status = 'rejected', decision_reason = $2, reviewed_by = $3, reviewed_at = $4,
  last_activity_at = $4, updated_at = $4 WHERE id = $1`, id, reason, reviewer, now); err != nil {
		return err
	}
	if err := addEvent(ctx, tx, id, "rejected", operatorActor(reviewer), map[string]any{"reason": reason}, nil); err != nil {
		return err
	}
	if app.EmailConfirmedAt != nil {
		if err := s.queueEmail(ctx, tx, authemail.OnboardingKindRejected, app, "", nil, strings.TrimSpace(message)); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.writeAudit(ctx, operatorActor(reviewer), "v1.admin.onboarding.reject", id, map[string]any{"reason": reason})
	return nil
}

// RequestInfo asks the applicant for specific fields. The application goes to
// info_requested and the applicant gets a fresh continue link.
func (s *Service) RequestInfo(ctx context.Context, id, reviewer uuid.UUID, fields []string, message string) error {
	message = strings.TrimSpace(message)
	errs := FieldErrors{}
	if message == "" {
		errs["message"] = "required"
	}
	if len(fields) == 0 {
		errs["fields"] = "required"
	}
	for _, k := range fields {
		if f, ok := LookupField(k); !ok || f.ReadOnly {
			errs["fields"] = "invalid"
		}
	}
	if len(errs) > 0 {
		return &FieldsError{Code: "invalid_field", Fields: errs}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	app, err := s.lockForDecision(ctx, tx, id, StatusPendingApproval, StatusInfoRequested)
	if err != nil {
		return err
	}
	resume, hash, err := newToken()
	if err != nil {
		return err
	}
	now := s.now()
	if _, err := tx.Exec(ctx, `
UPDATE onboarding_applications SET status = 'info_requested', requested_fields = $2, info_request_message = $3,
  resume_token_hash = $4, resume_token_expires_at = $5, reviewed_by = $6, last_activity_at = $7, updated_at = $7
WHERE id = $1`, id, fields, message, hash, now.Add(resumeLinkTTL), reviewer, now); err != nil {
		return err
	}
	if err := addEvent(ctx, tx, id, "info_requested", operatorActor(reviewer), map[string]any{"fields": fields}, nil); err != nil {
		return err
	}
	if err := s.queueEmail(ctx, tx, authemail.OnboardingKindInfoRequested, app, resume, fields, message); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.writeAudit(ctx, operatorActor(reviewer), "v1.admin.onboarding.request_info", id, map[string]any{"fields": fields})
	return nil
}

// Extend gives a draft more time (days counted from now) and revives an expired one.
func (s *Service) Extend(ctx context.Context, id, reviewer uuid.UUID, days int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := s.lockForDecision(ctx, tx, id, StatusDraft, StatusExpired, StatusInfoRequested); err != nil {
		return err
	}
	set, err := loadSettings(ctx, tx)
	if err != nil {
		return err
	}
	if days <= 0 {
		days = set.DraftTTLDays
	}
	if days > 1095 {
		days = 1095
	}
	now := s.now()
	if _, err := tx.Exec(ctx, `
UPDATE onboarding_applications SET expires_at = $2::timestamptz + make_interval(days => $3),
  status = CASE WHEN status = 'expired' THEN 'draft' ELSE status END, updated_at = $2 WHERE id = $1`, id, now, days); err != nil {
		return err
	}
	if err := addEvent(ctx, tx, id, "extended", operatorActor(reviewer), map[string]any{"days": days}, nil); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.writeAudit(ctx, operatorActor(reviewer), "v1.admin.onboarding.extend", id, map[string]any{"days": days})
	return nil
}

// Purge erases the personal data of an application (the applicant asked, or the
// operator decided to). An approved application keeps its answers: they belong
// to the organization now.
func (s *Service) Purge(ctx context.Context, id, reviewer uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := s.lockForDecision(ctx, tx, id, StatusDraft, StatusPendingApproval, StatusInfoRequested, StatusRejected, StatusExpired); err != nil {
		return err
	}
	if err := purgeRow(ctx, tx, id, s.now()); err != nil {
		return err
	}
	if err := addEvent(ctx, tx, id, "purged", operatorActor(reviewer), nil, nil); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.writeAudit(ctx, operatorActor(reviewer), "v1.admin.onboarding.purge", id, nil)
	return nil
}

// purgeRow blanks every personal field of an application and its history.
func purgeRow(ctx context.Context, q querier, id uuid.UUID, now time.Time) error {
	if _, err := q.Exec(ctx, `
UPDATE onboarding_applications SET applicant_email = 'purged+' || id::text || '@invalid.local',
  email_confirmed_at = NULL, applicant_name = NULL, applicant_phone = NULL, country = NULL, org_name = NULL,
  legal_name = NULL, answers = '{}', requested_fields = '{}', info_request_message = NULL,
  access_token_hash = NULL, resume_token_hash = NULL, resume_token_expires_at = NULL, telegram_user_id = NULL,
  utm = '{}', ip_hash = NULL, purged_at = $2, updated_at = $2 WHERE id = $1`, id, now); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE onboarding_application_events SET snapshot = NULL, detail = '{}' WHERE application_id = $1 AND kind <> 'purged'`, id); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `DELETE FROM onboarding_application_notes WHERE application_id = $1`, id); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM onboarding_application_checks WHERE application_id = $1`, id)
	return err
}

// AddNote stores a private note.
func (s *Service) AddNote(ctx context.Context, id, author uuid.UUID, body string) (*Note, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 4000 {
		return nil, &FieldsError{Code: "invalid_field", Fields: FieldErrors{"body": "invalid"}}
	}
	var n Note
	err := s.pool.QueryRow(ctx, `
INSERT INTO onboarding_application_notes (application_id, author_id, body)
SELECT id, $2, $3 FROM onboarding_applications WHERE id = $1 AND purged_at IS NULL
RETURNING id, author_id, body, created_at`, id, author, body).Scan(&n.ID, &n.AuthorID, &n.Body, &n.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &n, err
}

// SettingsUpdate is a partial settings change; nil leaves a value alone.
type SettingsUpdate struct {
	ApprovalMode   *string
	DraftTTLDays   *int
	PurgeAfterDays *int
	Countries      *[]string
	MaxNewPerDay   *int
	TermsVersion   *string
	PrivacyVersion *string
}

// UpdateSettings validates and stores a settings change.
func (s *Service) UpdateSettings(ctx context.Context, u SettingsUpdate, by uuid.UUID) (Settings, error) {
	cur, err := loadSettings(ctx, s.pool)
	if err != nil {
		return cur, err
	}
	errs := FieldErrors{}
	if u.ApprovalMode != nil {
		if *u.ApprovalMode != ModeManual && *u.ApprovalMode != ModeAutoWhenComplete {
			errs["approval_mode"] = "not_allowed"
		} else {
			cur.ApprovalMode = *u.ApprovalMode
		}
	}
	setInt := func(key string, p *int, dst *int, lo, hi int) {
		if p == nil {
			return
		}
		if *p < lo || *p > hi {
			errs[key] = "invalid"
			return
		}
		*dst = *p
	}
	setInt("draft_ttl_days", u.DraftTTLDays, &cur.DraftTTLDays, 7, 1095)
	setInt("purge_after_days", u.PurgeAfterDays, &cur.PurgeAfterDays, 30, 3650)
	setInt("max_new_per_day", u.MaxNewPerDay, &cur.MaxNewPerDay, 1, 100000)
	if u.Countries != nil {
		list := make([]string, 0, len(*u.Countries))
		for _, c := range *u.Countries {
			c = strings.ToUpper(strings.TrimSpace(c))
			if !countryPattern.MatchString(c) {
				errs["countries"] = "invalid"
				break
			}
			list = append(list, c)
		}
		cur.Countries = list
	}
	if u.TermsVersion != nil {
		if v := strings.TrimSpace(*u.TermsVersion); v == "" || len(v) > 40 {
			errs["terms_version"] = "invalid"
		} else {
			cur.TermsVersion = v
		}
	}
	if u.PrivacyVersion != nil {
		if v := strings.TrimSpace(*u.PrivacyVersion); v == "" || len(v) > 40 {
			errs["privacy_version"] = "invalid"
		} else {
			cur.PrivacyVersion = v
		}
	}
	if len(errs) > 0 {
		return cur, &FieldsError{Code: "invalid_field", Fields: errs}
	}
	_, err = s.pool.Exec(ctx, `
UPDATE onboarding_settings SET approval_mode = $1, draft_ttl_days = $2, purge_after_days = $3, countries = $4,
  max_new_per_day = $5, terms_version = $6, privacy_version = $7, updated_by = $8, updated_at = now() WHERE id`,
		cur.ApprovalMode, cur.DraftTTLDays, cur.PurgeAfterDays, cur.Countries, cur.MaxNewPerDay,
		cur.TermsVersion, cur.PrivacyVersion, by)
	if err != nil {
		return cur, err
	}
	s.writeAudit(ctx, operatorActor(by), "v1.admin.onboarding.settings", uuid.Nil, map[string]any{
		"approval_mode": cur.ApprovalMode, "draft_ttl_days": cur.DraftTTLDays, "purge_after_days": cur.PurgeAfterDays,
		"countries": cur.Countries, "max_new_per_day": cur.MaxNewPerDay,
	})
	return loadSettings(ctx, s.pool)
}

// ─── operator messages ───────────────────────────────────────────────────────

func (s *Service) link(id uuid.UUID) string {
	if s.adminURL == "" {
		return ""
	}
	return s.adminURL + "/onboarding/" + id.String()
}

// Operator-message kinds of an onboarding.notify job.
const (
	NotifySubmitted   = "submitted"
	NotifyResubmitted = "resubmitted"
	NotifyApproved    = "approved"
)

// JobTypeNotify is the worker_jobs.job_type of an operator message. The
// message is sent from arena-worker, which holds the ops bot credentials.
const JobTypeNotify = "onboarding.notify"

// NotifyPayload is the payload of an onboarding.notify job.
type NotifyPayload struct {
	Kind          string `json:"kind"`
	ApplicationID string `json:"application_id"`
	WouldApprove  bool   `json:"would_approve,omitempty"`
	OrgSlug       string `json:"org_slug,omitempty"`
	Automatic     bool   `json:"automatic,omitempty"`
}

func (s *Service) queueNotify(ctx context.Context, tx pgx.Tx, p NotifyPayload) error {
	if _, err := worker.EnqueueInTx(ctx, tx, JobTypeNotify, p, emailJobAttempts); err != nil {
		return fmt.Errorf("onboarding: queue operator message: %w", err)
	}
	return nil
}

// HandleNotify is the worker handler for onboarding.notify.
func (s *Service) HandleNotify(ctx context.Context, payload []byte) error {
	var p NotifyPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("onboarding: decode notify payload: %w", err)
	}
	id, err := uuid.Parse(p.ApplicationID)
	if err != nil {
		return fmt.Errorf("onboarding: notify payload: %w", err)
	}
	app, err := scanApplication(s.pool.QueryRow(ctx, `SELECT `+appColumns+` FROM onboarding_applications WHERE id = $1`, id))
	if err != nil {
		return fmt.Errorf("onboarding: notify: load application: %w", err)
	}
	switch p.Kind {
	case NotifySubmitted:
		s.notifySubmitted(ctx, app, p.WouldApprove, false)
	case NotifyResubmitted:
		s.notifySubmitted(ctx, app, p.WouldApprove, true)
	case NotifyApproved:
		actor := Actor{Type: "operator"}
		if p.Automatic {
			actor = ActorSystem
		}
		s.notifyDecision(ctx, app, p.OrgSlug, actor)
	default:
		return fmt.Errorf("onboarding: unknown notify kind %q", p.Kind)
	}
	return nil
}

func (s *Service) notifySubmitted(ctx context.Context, app *Application, would, again bool) {
	e := opsalert.EscapeHTML
	title := "🆕 <b>Новая заявка на онбординг</b>"
	if again {
		title = "🔁 <b>Заявка отправлена повторно после уточнений</b>"
	}
	val := func(p *string) string {
		if p == nil || *p == "" {
			return "—"
		}
		return *p
	}
	verdict := "нет"
	if would {
		verdict = "да"
	}
	lines := []string{
		title,
		"Организация: <b>" + e(val(app.OrgName)) + "</b>",
		"Страна: " + e(val(app.Country)),
		"Заявитель: " + e(val(app.ApplicantName)),
		"E-mail: " + e(app.Email),
		"Телефон: " + e(val(app.Phone)),
		"Источник: " + e(app.Source),
		"Система одобрила бы: " + verdict,
	}
	if l := s.link(app.ID); l != "" {
		lines = append(lines, l)
	}
	s.send(ctx, strings.Join(lines, "\n"))
}

func (s *Service) notifyDecision(ctx context.Context, app *Application, slug string, actor Actor) {
	who := "оператором"
	if actor.Type == "system" {
		who = "системой автоматически"
	}
	org := ""
	if app.OrgName != nil {
		org = *app.OrgName
	}
	s.send(ctx, fmt.Sprintf("✅ Заявка одобрена %s: <b>%s</b> (%s)", who, opsalert.EscapeHTML(org), opsalert.EscapeHTML(slug)))
}

func (s *Service) send(ctx context.Context, text string) {
	if err := s.notifier.Send(ctx, text); err != nil {
		s.logger.Warn("onboarding: operator notification failed", slog.Any("error", err))
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
