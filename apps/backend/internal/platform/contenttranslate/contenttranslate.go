// Package contenttranslate keeps organizer-editable translations of an event's
// (later a series') content: name, short description, description and the
// photo-report text, one row per locale in content_translations (migration
// 0218, spec 08_architecture/63 §16).
//
// A translation is written by hand (source manual) or by the machine
// translator (source machine). source_hash is the hash of the original fields
// at the moment of translation; a translation whose hash differs from the
// current original is stale (Stale) but is still served.
package contenttranslate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventtext"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// Owner types of a translation.
const (
	OwnerEvent  = "event"
	OwnerSeries = "series"
)

// Sources of a translation.
const (
	SourceManual  = "manual"
	SourceMachine = "machine"
)

// Field limits in runes.
const (
	MaxNameRunes             = 200
	MaxShortDescriptionRunes = 500
)

// Supported is the platform list of content locales (spec 63 §16.2). It must
// match the CHECK of content_translations.locale in migration 0218.
var Supported = []string{"cs", "de", "en", "es", "et", "fi", "fr", "he", "hu", "it", "lt", "lv", "pl", "ru", "uk"}

// NormalizeLocale turns "cs-CZ", "CS" or "cs_CZ" into "cs". ok is false for a
// language outside Supported.
func NormalizeLocale(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(s, "-_"); i >= 0 {
		s = s[:i]
	}
	if slices.Contains(Supported, s) {
		return s, true
	}
	return "", false
}

// DB is what the store needs: a pool or an open transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Fields are the translatable texts. nil means "not set" (for Name: the
// original name is used).
type Fields struct {
	Name             *string
	ShortDescription *string
	DescriptionHTML  *string
	ReportTextHTML   *string
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// SourceHash is the hex sha256 of the canonical JSON of the four fields. A nil
// field and an empty one hash the same.
func SourceHash(f Fields) string {
	canon := struct {
		Name             string `json:"name"`
		ShortDescription string `json:"short_description"`
		DescriptionHTML  string `json:"description_html"`
		ReportTextHTML   string `json:"report_text_html"`
	}{deref(f.Name), deref(f.ShortDescription), deref(f.DescriptionHTML), deref(f.ReportTextHTML)}
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Reasons of a FieldError.
const (
	ReasonNameTooLong             = "name_too_long"
	ReasonShortDescriptionTooLong = "short_description_too_long"
	ReasonInvalidLocale           = "invalid_locale"
	ReasonInvalidOwnerType        = "invalid_owner_type"
	ReasonMissingSourceHash       = "missing_source_hash"
)

// FieldError is a refused field. Reason is a short stable word for the API;
// for an HTML field it is the richtext reason (too_long, html_too_long, ...).
type FieldError struct {
	Field  string
	Reason string
	Err    error
}

func (e *FieldError) Error() string {
	msg := "contenttranslate: " + e.Field + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *FieldError) Unwrap() error { return e.Err }

func trimOrNil(p *string) *string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(*p)
	if s == "" {
		return nil
	}
	return &s
}

func sanitizeHTML(field string, p *string) (*string, error) {
	p = trimOrNil(p)
	if p == nil {
		return nil, nil
	}
	out, err := richtext.Normalize(*p, richtext.FormatART, richtext.ProfileDescription)
	if err != nil {
		return nil, &FieldError{Field: field, Reason: string(richtext.ReasonOf(err)), Err: err}
	}
	return trimOrNil(&out), nil
}

// SanitizeFields trims every field (empty becomes nil), checks the lengths of
// the plain fields and runs both HTML fields through the richtext description
// profile, so no script or foreign markup survives.
func SanitizeFields(f Fields) (Fields, error) {
	var out Fields
	out.Name = trimOrNil(f.Name)
	if out.Name != nil && utf8.RuneCountInString(*out.Name) > MaxNameRunes {
		return Fields{}, &FieldError{Field: "name", Reason: ReasonNameTooLong}
	}
	out.ShortDescription = trimOrNil(f.ShortDescription)
	if out.ShortDescription != nil && utf8.RuneCountInString(*out.ShortDescription) > MaxShortDescriptionRunes {
		return Fields{}, &FieldError{Field: "short_description", Reason: ReasonShortDescriptionTooLong}
	}
	var err error
	if out.DescriptionHTML, err = sanitizeHTML("description_html", f.DescriptionHTML); err != nil {
		return Fields{}, err
	}
	if out.ReportTextHTML, err = sanitizeHTML("report_text_html", f.ReportTextHTML); err != nil {
		return Fields{}, err
	}
	return out, nil
}

// Translation is one row of content_translations.
type Translation struct {
	OwnerType  string
	OwnerID    uuid.UUID
	OrgID      uuid.UUID
	Locale     string
	Fields     Fields
	Source     string
	SourceHash string
	UpdatedBy  *uuid.UUID
	UpdatedAt  time.Time
}

// Stale reports whether t was made from another original than the current one.
func Stale(t Translation, currentHash string) bool {
	return t.SourceHash != currentHash
}

const selectCols = `owner_type, owner_id, org_id, locale, name, short_description,
       description_html, report_text_html, source, source_hash, updated_by, updated_at`

func scan(row pgx.Row) (Translation, error) {
	var t Translation
	err := row.Scan(&t.OwnerType, &t.OwnerID, &t.OrgID, &t.Locale,
		&t.Fields.Name, &t.Fields.ShortDescription, &t.Fields.DescriptionHTML, &t.Fields.ReportTextHTML,
		&t.Source, &t.SourceHash, &t.UpdatedBy, &t.UpdatedAt)
	return t, err
}

// List returns the translations of one owner, ordered by locale.
func List(ctx context.Context, db DB, ownerType string, ownerID uuid.UUID) ([]Translation, error) {
	rows, err := db.Query(ctx, `SELECT `+selectCols+`
  FROM content_translations
 WHERE owner_type = $1 AND owner_id = $2
 ORDER BY locale`, ownerType, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Translation
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListForOwners returns the translations of many owners of one type, keyed by
// owner id, each list ordered by locale. Owners without a translation are
// absent from the map.
func ListForOwners(ctx context.Context, db DB, ownerType string, ids []uuid.UUID) (map[uuid.UUID][]Translation, error) {
	out := make(map[uuid.UUID][]Translation)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `SELECT `+selectCols+`
  FROM content_translations
 WHERE owner_type = $1 AND owner_id = ANY($2)
 ORDER BY owner_id, locale`, ownerType, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out[t.OwnerID] = append(out[t.OwnerID], t)
	}
	return out, rows.Err()
}

// Get returns one translation, pgx.ErrNoRows when there is none.
func Get(ctx context.Context, db DB, ownerType string, ownerID uuid.UUID, locale string) (Translation, error) {
	return scan(db.QueryRow(ctx, `SELECT `+selectCols+`
  FROM content_translations
 WHERE owner_type = $1 AND owner_id = $2 AND locale = $3`, ownerType, ownerID, locale))
}

// prepare checks the key fields and sanitizes the texts of t.
func prepare(t Translation) (Translation, error) {
	if t.OwnerType != OwnerEvent && t.OwnerType != OwnerSeries {
		return t, &FieldError{Field: "owner_type", Reason: ReasonInvalidOwnerType}
	}
	loc, ok := NormalizeLocale(t.Locale)
	if !ok {
		return t, &FieldError{Field: "locale", Reason: ReasonInvalidLocale}
	}
	t.Locale = loc
	if strings.TrimSpace(t.SourceHash) == "" {
		return t, &FieldError{Field: "source_hash", Reason: ReasonMissingSourceHash}
	}
	f, err := SanitizeFields(t.Fields)
	if err != nil {
		return t, err
	}
	t.Fields = f
	return t, nil
}

// PutManual writes a hand-made translation (insert or replace); the row
// always ends with source manual. The texts are sanitized first.
func PutManual(ctx context.Context, db DB, t Translation) (Translation, error) {
	t, err := prepare(t)
	if err != nil {
		return Translation{}, err
	}
	return scan(db.QueryRow(ctx, `INSERT INTO content_translations
    (owner_type, owner_id, org_id, locale, name, short_description, description_html,
     report_text_html, source, source_hash, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'manual', $9, $10)
ON CONFLICT (owner_type, owner_id, locale) DO UPDATE SET
    name              = EXCLUDED.name,
    short_description = EXCLUDED.short_description,
    description_html  = EXCLUDED.description_html,
    report_text_html  = EXCLUDED.report_text_html,
    source            = 'manual',
    source_hash       = EXCLUDED.source_hash,
    updated_by        = EXCLUDED.updated_by,
    updated_at        = now()
WHERE content_translations.org_id = EXCLUDED.org_id
RETURNING `+selectCols,
		t.OwnerType, t.OwnerID, t.OrgID, t.Locale, t.Fields.Name, t.Fields.ShortDescription,
		t.Fields.DescriptionHTML, t.Fields.ReportTextHTML, t.SourceHash, t.UpdatedBy))
}

// PutMachine writes a machine translation in ONE statement. An existing manual
// row is NEVER replaced (owner decision 22.11, spec 63 §16.4: there is no
// overwrite flag; delete the manual row first); written is false then.
func PutMachine(ctx context.Context, db DB, t Translation) (bool, error) {
	t, err := prepare(t)
	if err != nil {
		return false, err
	}
	tag, err := db.Exec(ctx, `INSERT INTO content_translations
    (owner_type, owner_id, org_id, locale, name, short_description, description_html,
     report_text_html, source, source_hash, updated_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'machine', $9, $10)
ON CONFLICT (owner_type, owner_id, locale) DO UPDATE SET
    name              = EXCLUDED.name,
    short_description = EXCLUDED.short_description,
    description_html  = EXCLUDED.description_html,
    report_text_html  = EXCLUDED.report_text_html,
    source            = 'machine',
    source_hash       = EXCLUDED.source_hash,
    updated_by        = EXCLUDED.updated_by,
    updated_at        = now()
WHERE content_translations.org_id = EXCLUDED.org_id
  AND content_translations.source = 'machine'`,
		t.OwnerType, t.OwnerID, t.OrgID, t.Locale, t.Fields.Name, t.Fields.ShortDescription,
		t.Fields.DescriptionHTML, t.Fields.ReportTextHTML, t.SourceHash, t.UpdatedBy)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Delete removes one translation of an owner of orgID; false when there was
// none.
func Delete(ctx context.Context, db DB, orgID uuid.UUID, ownerType string, ownerID uuid.UUID, locale string) (bool, error) {
	tag, err := db.Exec(ctx, `DELETE FROM content_translations
 WHERE org_id = $1 AND owner_type = $2 AND owner_id = $3 AND locale = $4`, orgID, ownerType, ownerID, locale)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// EventOriginal is the original content of an event, the source of its
// translations.
type EventOriginal struct {
	OrgID             uuid.UUID
	EventID           uuid.UUID
	ContentLocale     string
	Name              string
	ShortDescription  *string
	Description       *string
	DescriptionFormat string
	ReportTextHTML    *string
}

// LoadEventOriginal reads a live (not deleted) event of orgID, pgx.ErrNoRows
// otherwise.
func LoadEventOriginal(ctx context.Context, db DB, orgID, eventID uuid.UUID) (EventOriginal, error) {
	var o EventOriginal
	err := db.QueryRow(ctx, `SELECT org_id, id, content_locale, name, short_description,
       description, description_format, report_text_html
  FROM events
 WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL`, orgID, eventID).Scan(
		&o.OrgID, &o.EventID, &o.ContentLocale, &o.Name, &o.ShortDescription,
		&o.Description, &o.DescriptionFormat, &o.ReportTextHTML)
	return o, err
}

// Fields are the original texts in the shape of a translation; the
// description is rendered to HTML whatever its stored format.
func (o EventOriginal) Fields() Fields {
	name := o.Name
	f := Fields{Name: &name, ShortDescription: o.ShortDescription, ReportTextHTML: o.ReportTextHTML}
	if o.Description != nil && strings.TrimSpace(*o.Description) != "" {
		html := eventtext.ViewDescription(*o.Description, o.DescriptionFormat).HTML
		f.DescriptionHTML = &html
	}
	return f
}

// Hash is SourceHash of the original fields.
func (o EventOriginal) Hash() string { return SourceHash(o.Fields()) }

// SetEventReportText writes the sanitized photo-report text of the original
// event; nil or an empty text clears it. pgx.ErrNoRows when no live event of
// orgID has that id.
func SetEventReportText(ctx context.Context, db DB, orgID, eventID uuid.UUID, html *string) error {
	clean, err := sanitizeHTML("report_text_html", html)
	if err != nil {
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE events SET report_text_html = $3, updated_at = now()
 WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL`, orgID, eventID, clean)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
