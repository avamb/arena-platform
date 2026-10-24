// Package eventtext is the single write path of the two formatted texts of an
// event (spec 43, RT-02): the description (events.description and the
// i18n_text rows of namespace event.description) and the organizer's own
// letter text (event_email_notes.note).
//
// Every writer passes the text through NormalizeDescription or NormalizeNote,
// which are richtext.Normalize with the right profile, and stores the result
// with the format flag "art". The flag of an event covers its translations
// too, so a writer that stores ART into an event whose flag is still "text"
// must first convert the rest of the event's descriptions: UpgradeDescriptions
// does it, inside the writer's transaction, before the new values go in.
package eventtext

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// ParseFormat reads the optional format field of a request: "" means "text".
// ok is false for anything but "", "text" and "art".
func ParseFormat(s string) (format string, ok bool) {
	switch s {
	case "", richtext.FormatText:
		return richtext.FormatText, true
	case richtext.FormatART:
		return richtext.FormatART, true
	}
	return "", false
}

// NormalizeDescription is richtext.Normalize with the description profile:
// the text to store (ART) or a *richtext.Error.
func NormalizeDescription(input, format string) (string, error) {
	return richtext.Normalize(input, format, richtext.ProfileDescription)
}

// NormalizeNote is richtext.Normalize with the letter-text profile. An empty
// result means "no note".
func NormalizeNote(input, format string) (string, error) {
	return richtext.Normalize(input, format, richtext.ProfileEmailNote)
}

// Reason is the short stable word of a refusal, for the API's error details.
func Reason(err error) string { return string(richtext.ReasonOf(err)) }

// UpgradeError says which stored description could not be converted.
type UpgradeError struct {
	// Locale is "" for the base description.
	Locale string
	Err    error
}

func (e *UpgradeError) Error() string {
	if e.Locale == "" {
		return "the stored description cannot be converted: " + e.Err.Error()
	}
	return "the stored description (" + e.Locale + ") cannot be converted: " + e.Err.Error()
}

func (e *UpgradeError) Unwrap() error { return e.Err }

// UpgradeDescriptions makes sure every description of the event is ART before
// a writer stores new ART into one of them: when the event's flag is "text",
// its base description and its translations are converted with
// richtext.Normalize and the flag becomes "art". Rows the writer is about to
// overwrite (skipBase, skipLocales) are not converted. A flag that is already
// "art" is left alone. Run it inside the writer's transaction: the event row
// is locked. pgx.ErrNoRows means no such active event of the organization;
// a *UpgradeError means a stored text cannot be converted (too long, rejected
// characters) and nothing was changed.
func UpgradeDescriptions(ctx context.Context, q *gen.Queries, eventID, orgID uuid.UUID, skipBase bool, skipLocales ...string) error {
	state, err := q.LockEventDescription(ctx, eventID, orgID)
	if err != nil {
		return err
	}
	if state.Format == richtext.FormatART {
		return nil
	}
	skip := make(map[string]bool, len(skipLocales))
	for _, l := range skipLocales {
		skip[l] = true
	}
	var base *string
	if state.Description != nil {
		v := *state.Description
		if !skipBase && v != "" {
			v, err = NormalizeDescription(v, richtext.FormatText)
			if err != nil {
				return &UpgradeError{Err: err}
			}
		}
		base = &v
	}
	translations, err := q.ListEventI18nDescriptions(ctx, eventID.String())
	if err != nil {
		return fmt.Errorf("list event descriptions: %w", err)
	}
	for _, t := range translations {
		if skip[t.Locale] || t.Value == "" {
			continue
		}
		v, nErr := NormalizeDescription(t.Value, richtext.FormatText)
		if nErr != nil {
			return &UpgradeError{Locale: t.Locale, Err: nErr}
		}
		if err := q.UpsertEventI18nDescription(ctx, eventID.String(), t.Locale, v); err != nil {
			return fmt.Errorf("convert event description (%s): %w", t.Locale, err)
		}
	}
	if err := q.SetEventDescriptionART(ctx, eventID, base); err != nil {
		return fmt.Errorf("convert event description: %w", err)
	}
	return nil
}

// AsUpgradeError unwraps err to the *UpgradeError, if it is one.
func AsUpgradeError(err error) (*UpgradeError, bool) {
	var ue *UpgradeError
	if errors.As(err, &ue) {
		return ue, true
	}
	return nil, false
}
