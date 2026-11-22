package contenttranslate

// job.go - the worker job content.translate (spec 63 section 16.4): machine
// translation of an event's texts into the requested locales.
//
// Rules per locale, in this order:
//   - the content locale of the event is skipped;
//   - an existing MANUAL row is ALWAYS skipped (owner decision 22.11: there is
//     no overwrite flag; to replace a manual text it is deleted first);
//   - an existing machine row made from the current original (same source_hash)
//     is skipped;
//   - only non-empty original fields are translated.
//
// A refused answer (altered name, text the sanitizer refuses, a cut answer) is
// logged and not retried; a transient failure (network, 5xx, rate limit) makes
// the job return an error, and the worker runs it again: locales already
// translated are skipped by the hash.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// JobType is the worker_jobs.job_type of a machine translation.
const JobType = "content.translate"

// JobAttempts is max_attempts of one content.translate row.
const JobAttempts = 3

// Payload is the job payload.
type Payload struct {
	OwnerType   string     `json:"owner_type"`
	OwnerID     uuid.UUID  `json:"owner_id"`
	OrgID       uuid.UUID  `json:"org_id"`
	Locales     []string   `json:"locales"`
	RequestedBy *uuid.UUID `json:"requested_by,omitempty"`
}

// Enqueue queues a content.translate job and returns its id.
func Enqueue(ctx context.Context, db DB, p Payload) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("content.translate: encode payload: %w", err)
	}
	var id string
	if err := db.QueryRow(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, $2::jsonb, $3, 'pending', now())
		RETURNING id::text`, JobType, body, JobAttempts).Scan(&id); err != nil {
		return "", fmt.Errorf("content.translate: enqueue: %w", err)
	}
	return id, nil
}

// Store is what the job needs of the database; PGStore is the real one.
type Store interface {
	LoadEvent(ctx context.Context, orgID, eventID uuid.UUID) (EventOriginal, error)
	Get(ctx context.Context, ownerType string, ownerID uuid.UUID, locale string) (Translation, error)
	PutMachine(ctx context.Context, t Translation) (bool, error)
}

// PGStore is Store over a pool or a transaction.
type PGStore struct{ DB DB }

// LoadEvent implements Store.
func (s PGStore) LoadEvent(ctx context.Context, orgID, eventID uuid.UUID) (EventOriginal, error) {
	return LoadEventOriginal(ctx, s.DB, orgID, eventID)
}

// Get implements Store.
func (s PGStore) Get(ctx context.Context, ownerType string, ownerID uuid.UUID, locale string) (Translation, error) {
	return Get(ctx, s.DB, ownerType, ownerID, locale)
}

// PutMachine implements Store.
func (s PGStore) PutMachine(ctx context.Context, t Translation) (bool, error) {
	return PutMachine(ctx, s.DB, t)
}

// Options builds the handler. A nil Translator makes the handler log and
// complete without writing.
type Options struct {
	Store      Store
	Translator Translator
	Logger     *slog.Logger
}

// Decision is what the job does with one locale.
type Decision string

// Decisions of one locale.
const (
	DecisionTranslate     Decision = "translate"
	DecisionSkipContent   Decision = "skip_content_locale"
	DecisionSkipManual    Decision = "skip_manual"
	DecisionSkipUpToDate  Decision = "skip_up_to_date"
	DecisionSkipNoContent Decision = "skip_nothing_to_translate"
)

// Decide chooses what to do with one locale. existing is nil when the owner has
// no row for it.
func Decide(orig EventOriginal, locale string, existing *Translation) Decision {
	if locale == orig.ContentLocale {
		return DecisionSkipContent
	}
	if existing != nil {
		if existing.Source == SourceManual {
			return DecisionSkipManual
		}
		if existing.SourceHash == orig.Hash() {
			return DecisionSkipUpToDate
		}
	}
	f := orig.Fields()
	if f.ShortDescription == nil && f.DescriptionHTML == nil && f.ReportTextHTML == nil {
		return DecisionSkipNoContent
	}
	return DecisionTranslate
}

// NewHandler returns the content.translate handler.
func NewHandler(opts Options) func(context.Context, []byte) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, payload []byte) error {
		var p Payload
		if err := json.Unmarshal(payload, &p); err != nil {
			logger.Error("content.translate: bad payload, dropped", slog.String("error", err.Error()))
			return nil
		}
		return Run(ctx, opts.Store, opts.Translator, p, logger)
	}
}

// Run translates the payload's locales. It returns an error only for a
// transient failure.
func Run(ctx context.Context, store Store, tr Translator, p Payload, logger *slog.Logger) error {
	if tr == nil {
		logger.Warn("content.translate: no translator configured (TRANSLATE_LLM_API_KEY is empty), nothing written",
			slog.String("owner_id", p.OwnerID.String()))
		return nil
	}
	if p.OwnerType != OwnerEvent {
		logger.Error("content.translate: unsupported owner type, dropped", slog.String("owner_type", p.OwnerType))
		return nil
	}
	orig, err := store.LoadEvent(ctx, p.OrgID, p.OwnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		logger.Warn("content.translate: event not found, dropped", slog.String("event_id", p.OwnerID.String()))
		return nil
	}
	if err != nil {
		return fmt.Errorf("content.translate: load event: %w", err)
	}
	hash := orig.Hash()
	var transient error
	for _, raw := range p.Locales {
		locale, ok := NormalizeLocale(raw)
		if !ok {
			logger.Error("content.translate: unsupported locale, skipped", slog.String("locale", raw))
			continue
		}
		var existing *Translation
		got, err := store.Get(ctx, OwnerEvent, orig.EventID, locale)
		switch {
		case err == nil:
			existing = &got
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return fmt.Errorf("content.translate: read %s: %w", locale, err)
		}
		d := Decide(orig, locale, existing)
		if d != DecisionTranslate {
			logger.Info("content.translate: locale skipped", slog.String("event_id", orig.EventID.String()),
				slog.String("locale", locale), slog.String("reason", string(d)))
			continue
		}
		of := orig.Fields()
		out, err := tr.Translate(ctx, TranslateInput{
			From: orig.ContentLocale, To: locale, Name: orig.Name,
			Fields: Fields{ShortDescription: of.ShortDescription, DescriptionHTML: of.DescriptionHTML, ReportTextHTML: of.ReportTextHTML},
		})
		if err == nil {
			// The check is repeated here so that no translator can write a text
			// that lost the event name.
			err = CheckName(orig.Name, of, out)
		}
		if err != nil {
			if Permanent(err) {
				logger.Error("content.translate: translation refused, not retried",
					slog.String("event_id", orig.EventID.String()), slog.String("locale", locale), slog.String("error", err.Error()))
				continue
			}
			logger.Warn("content.translate: translation failed, will retry",
				slog.String("event_id", orig.EventID.String()), slog.String("locale", locale), slog.String("error", err.Error()))
			if transient == nil {
				transient = fmt.Errorf("content.translate: %s: %w", locale, err)
			}
			continue
		}
		out.Name = nil
		written, err := store.PutMachine(ctx, Translation{
			OwnerType: OwnerEvent, OwnerID: orig.EventID, OrgID: orig.OrgID, Locale: locale,
			Fields: out, SourceHash: hash, UpdatedBy: p.RequestedBy,
		})
		if err != nil {
			var fe *FieldError
			if errors.As(err, &fe) {
				logger.Error("content.translate: translation refused by the sanitizer, not retried",
					slog.String("locale", locale), slog.String("error", err.Error()))
				continue
			}
			return fmt.Errorf("content.translate: write %s: %w", locale, err)
		}
		logger.Info("content.translate: locale done", slog.String("event_id", orig.EventID.String()),
			slog.String("locale", locale), slog.Bool("written", written))
	}
	return transient
}
