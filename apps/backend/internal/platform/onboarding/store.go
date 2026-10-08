package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Status values of an application.
const (
	StatusDraft           = "draft"
	StatusPendingApproval = "pending_approval"
	StatusInfoRequested   = "info_requested"
	StatusApproved        = "approved"
	StatusRejected        = "rejected"
	StatusExpired         = "expired"
)

// Statuses lists every status in queue order.
var Statuses = []string{StatusPendingApproval, StatusInfoRequested, StatusDraft, StatusApproved, StatusRejected, StatusExpired}

// Application is one row of onboarding_applications.
type Application struct {
	ID                 uuid.UUID  `json:"id"`
	Status             string     `json:"status"`
	Source             string     `json:"source"`
	Email              string     `json:"email"`
	EmailConfirmedAt   *time.Time `json:"email_confirmed_at"`
	ApplicantName      *string    `json:"applicant_name"`
	Phone              *string    `json:"phone"`
	Locale             string     `json:"locale"`
	Country            *string    `json:"country"`
	OrgName            *string    `json:"org_name"`
	LegalName          *string    `json:"legal_name"`
	CurrentStep        string     `json:"current_step"`
	ProgressPct        int        `json:"progress_pct"`
	Answers            Answers    `json:"answers"`
	RequestedFields    []string   `json:"requested_fields"`
	InfoRequestMessage *string    `json:"info_request_message"`
	TelegramUserID     *int64     `json:"telegram_user_id"`
	TermsVersion       *string    `json:"terms_version"`
	PrivacyVersion     *string    `json:"privacy_version"`
	ConsentedAt        *time.Time `json:"consented_at"`
	UTM                Answers    `json:"utm"`
	RemindersSent      int        `json:"reminders_sent"`
	LastActivityAt     time.Time  `json:"last_activity_at"`
	ExpiresAt          time.Time  `json:"expires_at"`
	SubmittedAt        *time.Time `json:"submitted_at"`
	ReviewedBy         *uuid.UUID `json:"reviewed_by"`
	ReviewedAt         *time.Time `json:"reviewed_at"`
	DecisionReason     *string    `json:"decision_reason"`
	OrgID              *uuid.UUID `json:"org_id"`
	PurgedAt           *time.Time `json:"purged_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

const appColumns = `id, status, source, applicant_email, email_confirmed_at, applicant_name, applicant_phone,
 locale, country, org_name, legal_name, current_step, progress_pct, answers, requested_fields,
 info_request_message, telegram_user_id, terms_version, privacy_version, consented_at, utm,
 reminders_sent, last_activity_at, expires_at, submitted_at, reviewed_by, reviewed_at,
 decision_reason, org_id, purged_at, created_at, updated_at`

// querier is satisfied by pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func scanApplication(row pgx.Row) (*Application, error) {
	var (
		a          Application
		answersRaw []byte
		utmRaw     []byte
		pct        int16
		reminders  int16
	)
	err := row.Scan(&a.ID, &a.Status, &a.Source, &a.Email, &a.EmailConfirmedAt, &a.ApplicantName, &a.Phone,
		&a.Locale, &a.Country, &a.OrgName, &a.LegalName, &a.CurrentStep, &pct, &answersRaw, &a.RequestedFields,
		&a.InfoRequestMessage, &a.TelegramUserID, &a.TermsVersion, &a.PrivacyVersion, &a.ConsentedAt, &utmRaw,
		&reminders, &a.LastActivityAt, &a.ExpiresAt, &a.SubmittedAt, &a.ReviewedBy, &a.ReviewedAt,
		&a.DecisionReason, &a.OrgID, &a.PurgedAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	a.ProgressPct, a.RemindersSent = int(pct), int(reminders)
	a.Answers = Answers{}
	if len(answersRaw) > 0 {
		if err := json.Unmarshal(answersRaw, &a.Answers); err != nil {
			return nil, fmt.Errorf("onboarding: decode answers: %w", err)
		}
	}
	a.UTM = Answers{}
	if len(utmRaw) > 0 {
		_ = json.Unmarshal(utmRaw, &a.UTM)
	}
	if a.RequestedFields == nil {
		a.RequestedFields = []string{}
	}
	return &a, nil
}

// ErrNotFound is returned for an unknown id, a wrong token or an expired link —
// the three are deliberately indistinguishable.
var ErrNotFound = errors.New("onboarding: application not found")

// Event is one entry of the application timeline.
type Event struct {
	ID        uuid.UUID      `json:"id"`
	Kind      string         `json:"kind"`
	ActorType string         `json:"actor_type"`
	ActorID   string         `json:"actor_id"`
	Detail    map[string]any `json:"detail"`
	CreatedAt time.Time      `json:"created_at"`
}

// Note is an operator's private note.
type Note struct {
	ID        uuid.UUID  `json:"id"`
	AuthorID  *uuid.UUID `json:"author_id"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"created_at"`
}

// Check is the result of one automatic check.
type Check struct {
	Key       string    `json:"key"`
	Result    string    `json:"result"`
	Detail    string    `json:"detail"`
	CheckedAt time.Time `json:"checked_at"`
}

// Actor is who did something: the applicant, an operator or the system.
type Actor struct {
	Type string // applicant | operator | system
	ID   string
}

// Convenience actors.
var (
	ActorApplicant = Actor{Type: "applicant"}
	ActorSystem    = Actor{Type: "system"}
)

func operatorActor(id uuid.UUID) Actor { return Actor{Type: "operator", ID: id.String()} }

func addEvent(ctx context.Context, q querier, appID uuid.UUID, kind string, actor Actor, detail map[string]any, snapshot Answers) error {
	if detail == nil {
		detail = map[string]any{}
	}
	d, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	var snap []byte
	if snapshot != nil {
		if snap, err = json.Marshal(snapshot); err != nil {
			return err
		}
	}
	_, err = q.Exec(ctx, `INSERT INTO onboarding_application_events (application_id, kind, actor_type, actor_id, detail, snapshot)
VALUES ($1, $2, $3, $4, $5, $6)`, appID, kind, actor.Type, actor.ID, d, snap)
	return err
}

// Settings is the single onboarding_settings row.
type Settings struct {
	ApprovalMode   string    `json:"approval_mode"`
	DraftTTLDays   int       `json:"draft_ttl_days"`
	PurgeAfterDays int       `json:"purge_after_days"`
	Countries      []string  `json:"countries"`
	MaxNewPerDay   int       `json:"max_new_per_day"`
	TermsVersion   string    `json:"terms_version"`
	PrivacyVersion string    `json:"privacy_version"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Approval modes.
const (
	ModeManual           = "manual"
	ModeAutoWhenComplete = "auto_when_complete"
)

func loadSettings(ctx context.Context, q querier) (Settings, error) {
	var s Settings
	err := q.QueryRow(ctx, `SELECT approval_mode, draft_ttl_days, purge_after_days, countries, max_new_per_day,
 terms_version, privacy_version, updated_at FROM onboarding_settings WHERE id`).
		Scan(&s.ApprovalMode, &s.DraftTTLDays, &s.PurgeAfterDays, &s.Countries, &s.MaxNewPerDay,
			&s.TermsVersion, &s.PrivacyVersion, &s.UpdatedAt)
	if err != nil {
		return s, fmt.Errorf("onboarding: load settings: %w", err)
	}
	if s.Countries == nil {
		s.Countries = []string{}
	}
	return s, nil
}

// allowedCountry reports whether the settings accept a country.
func (s Settings) allowedCountry(c string) bool {
	if len(s.Countries) == 0 {
		return true
	}
	return contains(s.Countries, c)
}
