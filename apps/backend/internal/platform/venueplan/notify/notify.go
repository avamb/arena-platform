// Package notify tells every platform administrator about the events of the
// hall plan intake (spec 08_architecture/64 sections 3.2 and 4.4): an
// organization made a venue, sent an intake, an intake went to an
// administrator, an organizer approved a plan that now waits for verification.
//
// The producer side is Enqueue (and Notifier, the venueplan.Notifier that
// calls it): it writes ONE worker job venue_plan.notify in the caller's
// transaction, so the job exists exactly when the change commits. The job
// handler (NewHandler) loads the facts, builds a Russian adminnotify.Message
// and, in ONE transaction, writes the dedup row of venue_plan_notify_log and
// queues the e-mail and Telegram jobs through adminnotify.Fanout. A retried job
// finds its dedup row and sends nothing. The message carries the organization
// name and the venue, never a person of the organization.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/adminnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// JobType is the worker_jobs.job_type of one notification of the intake.
const JobType = "venue_plan.notify"

// jobAttempts is how many times the worker tries one notify job.
const jobAttempts = 5

// Kind is what happened.
type Kind string

// The four events of spec 64 section 3.2, and a country added to the arena
// reference (spec 64 section 2.1.1).
const (
	KindVenueCreated    Kind = "venue_created"
	KindIntakeCreated   Kind = "intake_created"
	KindIntakeEscalated Kind = "intake_escalated"
	KindPlanApproved    Kind = "plan_approved"
	// KindCountryCreated: a venue or an approved organizer application
	// created a country row from the world table (internal/platform/worldgeo).
	KindCountryCreated Kind = "country_created"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case KindVenueCreated, KindIntakeCreated, KindIntakeEscalated, KindPlanApproved, KindCountryCreated:
		return true
	}
	return false
}

// Payload is the payload of a venue_plan.notify job. Nonce is made when the
// job is enqueued; kind + nonce is the dedup key of the message.
//
// country_created carries CountryID (the subject of the dedup row),
// CountryIso2, and VenueID when a venue created the country or OrgID when an
// approved organizer application did.
type Payload struct {
	Kind        Kind   `json:"kind"`
	IntakeID    string `json:"intake_id,omitempty"`
	VenueID     string `json:"venue_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
	CountryID   string `json:"country_id,omitempty"`
	CountryIso2 string `json:"country_iso2,omitempty"`
	OrgID       string `json:"org_id,omitempty"`
	Nonce       string `json:"nonce"`
}

// Enqueue writes one venue_plan.notify job on db (a transaction, or a pool for
// a caller that has none). A missing nonce is made here.
func Enqueue(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, p Payload) error {
	if !p.Kind.Valid() {
		return fmt.Errorf("venueplan notify: unknown kind %q", p.Kind)
	}
	if p.Nonce == "" {
		p.Nonce = uuid.NewString()
	}
	if _, err := worker.EnqueueInTx(ctx, db, JobType, p, jobAttempts); err != nil {
		return fmt.Errorf("venueplan notify: %w", err)
	}
	return nil
}

// EnqueueVenueCreated queues the notification of a venue an organization made.
func EnqueueVenueCreated(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, venueID uuid.UUID) error {
	return Enqueue(ctx, db, Payload{Kind: KindVenueCreated, VenueID: venueID.String()})
}

// EnqueueCountryCreatedByVenue queues the notification of a country a venue
// created; call it on the transaction that created both.
func EnqueueCountryCreatedByVenue(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, countryID uuid.UUID, iso2 string, venueID uuid.UUID) error {
	return Enqueue(ctx, db, Payload{Kind: KindCountryCreated, CountryID: countryID.String(), CountryIso2: iso2, VenueID: venueID.String()})
}

// EnqueueCountryCreatedByOrganization queues the notification of a country an
// approved organizer application created; call it on the approval transaction.
func EnqueueCountryCreatedByOrganization(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, countryID uuid.UUID, iso2 string, orgID uuid.UUID) error {
	return Enqueue(ctx, db, Payload{Kind: KindCountryCreated, CountryID: countryID.String(), CountryIso2: iso2, OrgID: orgID.String()})
}

// Notifier is the venueplan.Notifier of arena-api: each call enqueues one job
// in the transaction of the intake route.
type Notifier struct{}

var _ venueplan.Notifier = Notifier{}

// IntakeCreated queues intake_created.
func (Notifier) IntakeCreated(ctx context.Context, tx pgx.Tx, intakeID uuid.UUID) error {
	return enqueueIntake(ctx, tx, KindIntakeCreated, intakeID, "")
}

// IntakeEscalated queues intake_escalated. Reason no_generator queues nothing:
// in wave 1 every new intake without a source SVG goes to an administrator and
// intake_created already says so.
func (Notifier) IntakeEscalated(ctx context.Context, tx pgx.Tx, intakeID uuid.UUID, reason venueplan.EscalationReason) error {
	if reason == venueplan.EscalationNoGenerator {
		return nil
	}
	return enqueueIntake(ctx, tx, KindIntakeEscalated, intakeID, string(reason))
}

// PlanApproved queues plan_approved.
func (Notifier) PlanApproved(ctx context.Context, tx pgx.Tx, intakeID uuid.UUID) error {
	return enqueueIntake(ctx, tx, KindPlanApproved, intakeID, "")
}

func enqueueIntake(ctx context.Context, tx pgx.Tx, kind Kind, intakeID uuid.UUID, reason string) error {
	var venueID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT venue_id FROM venue_plan_intakes WHERE id = $1`, intakeID).Scan(&venueID); err != nil {
		return fmt.Errorf("venueplan notify: load intake: %w", err)
	}
	return Enqueue(ctx, tx, Payload{Kind: kind, IntakeID: intakeID.String(), VenueID: venueID.String(), Reason: reason})
}

// ── the job handler ─────────────────────────────────────────────────────────

// TxBeginner starts a transaction (*pgxpool.Pool).
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Options configures NewHandler.
type Options struct {
	Pool TxBeginner
	// AppURL is APP_PUBLIC_URL, the origin of the admin console. Empty = no link.
	AppURL string
	// Telegram is true when the worker can send Telegram messages.
	Telegram bool
	Logger   *slog.Logger
}

// reasonWords says a reason in words for the administrator.
var reasonWords = map[string]string{
	string(venueplan.EscalationNoGenerator):       "для этого формата нет программы чтения",
	string(venueplan.EscalationGeneratorFailed):   "программа чтения не справилась с файлом",
	string(venueplan.EscalationDraftInvalid):      "черновик не прошёл проверку",
	string(venueplan.EscalationTooManyQuestions):  "к схеме слишком много вопросов",
	string(venueplan.EscalationOrganizerRequest):  "организатор позвал администратора",
	string(venueplan.EscalationOrganizerText):     "правку текстом не удалось применить",
	string(venueplan.EscalationAnswers):           "ответы организатора не удалось применить",
	string(venueplan.EscalationUnsupportedFormat): "формат файла не читается",
}

const (
	whatToDoIntake   = "Что делать: откройте заявку, нажмите «Взять» и постройте схему."
	whatToDoApproved = "Что делать: откройте схему, сравните её с исходниками и верифицируйте."
	whatToDoCountry  = "Что проверить: приём платежей, схему налогового номера и часовые пояса страны."
	maxTextRunes     = 1000
)

// NewHandler is the worker handler of venue_plan.notify.
func NewHandler(opts Options) func(context.Context, []byte) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	base := strings.TrimRight(opts.AppURL, "/")
	return func(ctx context.Context, raw []byte) error {
		var p Payload
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("venueplan notify: decode payload: %w", err)
		}
		subject, err := p.subject()
		if err != nil {
			return err
		}
		tx, err := opts.Pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("venueplan notify: begin: %w", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck

		msg, found, err := buildMessage(ctx, tx, p, subject, base)
		if err != nil {
			return err
		}
		if !found {
			logger.Warn("venueplan notify: the subject is gone, nothing to send", slog.String("kind", string(p.Kind)), slog.String("subject_id", subject.String()))
			return nil
		}
		// The dedup row and the fan-out share ONE transaction: a retry either
		// finds the row (and sends nothing) or finds neither (and sends all).
		isNew, err := gen.New(tx).InsertVenuePlanNotifyLog(ctx, subject, string(p.Kind)+":"+p.Nonce)
		if err != nil {
			return fmt.Errorf("venueplan notify: dedup row: %w", err)
		}
		if !isNew {
			return nil
		}
		admins, err := adminnotify.Fanout(ctx, tx, msg, adminnotify.Options{Telegram: opts.Telegram, Logger: logger})
		if err != nil {
			return fmt.Errorf("venueplan notify: fan-out: %w", err)
		}
		if admins == 0 {
			logger.Warn("venueplan notify: there is no platform administrator to tell", slog.String("kind", string(p.Kind)))
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("venueplan notify: commit: %w", err)
		}
		return nil
	}
}

// subject is the id the dedup row hangs on: the venue for venue_created, the
// intake for the other kinds.
func (p Payload) subject() (uuid.UUID, error) {
	if !p.Kind.Valid() {
		return uuid.Nil, fmt.Errorf("venueplan notify: unknown kind %q", p.Kind)
	}
	if strings.TrimSpace(p.Nonce) == "" {
		return uuid.Nil, errors.New("venueplan notify: payload has no nonce")
	}
	raw := p.IntakeID
	switch p.Kind {
	case KindVenueCreated:
		raw = p.VenueID
	case KindCountryCreated:
		raw = p.CountryID
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("venueplan notify: payload of %s has no valid subject id", p.Kind)
	}
	return id, nil
}

func buildMessage(ctx context.Context, tx pgx.Tx, p Payload, subject uuid.UUID, base string) (adminnotify.Message, bool, error) {
	if p.Kind == KindCountryCreated {
		return countryMessage(ctx, tx, p, subject, base)
	}
	if p.Kind == KindVenueCreated {
		var org, venue, city string
		err := tx.QueryRow(ctx, `
SELECT o.name, v.name, COALESCE(t_city.value, ci.slug, '')
FROM   venues v
JOIN   organizations o ON o.id = v.org_id
LEFT JOIN cities ci    ON ci.id = v.city_id
LEFT JOIN i18n_text t_city ON t_city.namespace = 'geo.cities' AND t_city.key = ci.slug AND t_city.locale = 'en'
WHERE  v.id = $1`, subject).Scan(&org, &venue, &city)
		if errors.Is(err, pgx.ErrNoRows) {
			return adminnotify.Message{}, false, nil
		}
		if err != nil {
			return adminnotify.Message{}, false, fmt.Errorf("venueplan notify: load venue: %w", err)
		}
		msg := adminnotify.Message{
			Title:       "Организация добавила площадку",
			Details:     []string{"Организация: " + org, "Площадка: " + venue},
			ButtonLabel: "Открыть",
		}
		if city != "" {
			msg.Details = append(msg.Details, "Город: "+city)
		}
		if base != "" {
			msg.Link = base + "/venues"
		}
		return msg, true, nil
	}

	var (
		org, venue, city, state string
		reason, text            *string
		planID                  *uuid.UUID
		planVersion             *int32
	)
	err := tx.QueryRow(ctx, `
SELECT o.name, v.name, COALESCE(t_city.value, ci.slug, ''), i.state,
       i.escalation_reason, i.escalation_text, i.plan_id, i.plan_version
FROM   venue_plan_intakes i
JOIN   organizations o ON o.id = i.org_id
JOIN   venues v        ON v.id = i.venue_id
LEFT JOIN cities ci    ON ci.id = v.city_id
LEFT JOIN i18n_text t_city ON t_city.namespace = 'geo.cities' AND t_city.key = ci.slug AND t_city.locale = 'en'
WHERE  i.id = $1`, subject).Scan(&org, &venue, &city, &state, &reason, &text, &planID, &planVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return adminnotify.Message{}, false, nil
	}
	if err != nil {
		return adminnotify.Message{}, false, fmt.Errorf("venueplan notify: load intake: %w", err)
	}
	kinds, err := sourceKinds(ctx, tx, subject)
	if err != nil {
		return adminnotify.Message{}, false, err
	}

	msg := adminnotify.Message{
		Details:     []string{"Организация: " + org, "Площадка: " + venue},
		ButtonLabel: "Открыть",
	}
	if city != "" {
		msg.Details = append(msg.Details, "Город: "+city)
	}
	if len(kinds) > 0 {
		msg.Details = append(msg.Details, fmt.Sprintf("Файлов: %d (%s)", len(kinds), strings.Join(distinct(kinds), ", ")))
	}
	if base != "" {
		msg.Link = base + "/venue-plans/" + subject.String()
	}

	switch p.Kind {
	case KindIntakeCreated:
		msg.Title = "Новая заявка на схему зала"
		if state == string(venueplan.StateOperator) {
			if reason != nil {
				msg.Details = append(msg.Details, "Заявка сразу ушла администратору, причина: "+words(*reason))
			} else {
				msg.Details = append(msg.Details, "Заявка сразу ушла администратору.")
			}
			msg.WhatToDo = whatToDoIntake
		} else {
			msg.Details = append(msg.Details, "Организатор проверяет черновик сам, действий пока не нужно.")
		}
	case KindIntakeEscalated:
		msg.Title = "Заявка на схему зала ждёт администратора"
		r := p.Reason
		if r == "" && reason != nil {
			r = *reason
		}
		msg.Details = append(msg.Details, "Причина: "+words(r))
		if r == string(venueplan.EscalationOrganizerRequest) && text != nil && strings.TrimSpace(*text) != "" {
			msg.Details = append(msg.Details, "Текст организатора: "+clip(strings.TrimSpace(*text)))
		}
		msg.WhatToDo = whatToDoIntake
	case KindPlanApproved:
		msg.Title = "Схема зала утверждена и ждёт верификации"
		if planID != nil && planVersion != nil {
			var seated, standing int32
			err := tx.QueryRow(ctx, `SELECT capacity_seated, capacity_standing FROM seating_plan_versions
WHERE seating_plan_id = $1 AND version_number = $2`, *planID, *planVersion).Scan(&seated, &standing)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return adminnotify.Message{}, false, fmt.Errorf("venueplan notify: load plan version: %w", err)
			}
			if err == nil {
				msg.Details = append(msg.Details, fmt.Sprintf("Мест на схеме: %d", seated))
				if standing > 0 {
					msg.Details = append(msg.Details, fmt.Sprintf("Мест без номера: %d", standing))
				}
			}
		}
		msg.Details = append(msg.Details, "Статус: ждёт верификации")
		msg.WhatToDo = whatToDoApproved
	}
	return msg, true, nil
}

func sourceKinds(ctx context.Context, tx pgx.Tx, intakeID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT kind FROM venue_plan_intake_sources WHERE intake_id = $1 ORDER BY position`, intakeID)
	if err != nil {
		return nil, fmt.Errorf("venueplan notify: load sources: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("venueplan notify: scan source: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func distinct(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func words(reason string) string {
	if w, ok := reasonWords[reason]; ok {
		return w
	}
	if reason == "" {
		return "не указана"
	}
	return reason
}

func clip(s string) string {
	r := []rune(s)
	if len(r) <= maxTextRunes {
		return s
	}
	return string(r[:maxTextRunes]) + "…"
}
